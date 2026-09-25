// Package otlphttp implements bounded OTLP HTTP transports. It does not own a
// journal or disposition store, register configuration, or turn nil errors into
// delivery acknowledgements. Bind Exporter.Send through Store.DoDelivery.
package otlphttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// ExporterConfig names exact per-signal URLs (including /v1/<signal> or a custom
// path). No credentials in URLs; BearerToken is not persisted in receipts.
// Timeout covers the entire Send call, including retries and backoff.
type ExporterConfig struct {
	Endpoints                  map[otlpwire.Signal]string
	BearerToken                string
	Gzip                       bool
	RootCAs                    *x509.CertPool
	RequestLimits              otlpwire.Limits
	ResponseBytes              int64
	Timeout                    time.Duration
	MaxAttempts                int
	InitialBackoff, MaxBackoff time.Duration
}

// Exporter can be shared across producer workers. The per-signal server delay
// also applies to subsequent Send calls in this process. It is not a durable
// retry schedule and is lost on restart. Close releases idle connections only;
// cancel active calls via their contexts before closing the owning pipeline.
type Exporter struct {
	cfg       ExporterConfig
	client    *http.Client
	mu        sync.Mutex
	notBefore map[otlpwire.Signal]time.Time
}

func NewExporter(c ExporterConfig) (*Exporter, error) {
	if len(c.Endpoints) == 0 || len(c.Endpoints) > 3 {
		return nil, errors.New("configure 1 to 3 OTLP signal endpoints")
	}
	endpoints := make(map[otlpwire.Signal]string, len(c.Endpoints))
	for s, endpoint := range c.Endpoints {
		if _, err := s.Path(); err != nil {
			return nil, err
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil, errors.New("OTLP endpoint must be HTTP(S) without URL credentials, query or fragment")
		}
		endpoints[s] = u.String()
	}
	c.Endpoints = endpoints
	if !validToken(c.BearerToken) {
		return nil, errors.New("invalid OTLP bearer token")
	}
	if c.RequestLimits == (otlpwire.Limits{}) {
		c.RequestLimits = otlpwire.DefaultLimits()
	}
	if c.RequestLimits.WireBytes <= 0 || c.RequestLimits.WireBytes > 64<<20 || c.RequestLimits.DecodedBytes <= 0 || c.RequestLimits.DecodedBytes > 64<<20 || c.RequestLimits.Items <= 0 {
		return nil, errors.New("invalid OTLP request limits")
	}
	if c.ResponseBytes == 0 {
		c.ResponseBytes = 1 << 20
	}
	if c.ResponseBytes < 1 || c.ResponseBytes > 8<<20 {
		return nil, errors.New("invalid OTLP response limit")
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = 100 * time.Millisecond
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 5 * time.Second
	}
	if c.Timeout <= 0 || c.Timeout > time.Hour || c.MaxAttempts < 1 || c.MaxAttempts > 10 || c.InitialBackoff <= 0 || c.MaxBackoff < c.InitialBackoff || c.MaxBackoff > time.Minute {
		return nil, errors.New("invalid OTLP timeout or retry policy")
	}
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second,
	}
	tr.DisableCompression = true // Explicitly bound wire AND decoded response bytes.
	tr.MaxResponseHeaderBytes = 32 << 10
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if c.RootCAs != nil {
		tr.TLSClientConfig.RootCAs = c.RootCAs.Clone()
	}
	return &Exporter{cfg: c, client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, notBefore: make(map[otlpwire.Signal]time.Time)}, nil
}

func validToken(s string) bool {
	for _, r := range s {
		if r < 33 || r > 126 {
			return false
		}
	}
	return len(s) <= 4096
}
func (e *Exporter) Close() { e.client.CloseIdleConnections() }
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (e *Exporter) waitAllowed(ctx context.Context, s otlpwire.Signal) error {
	for {
		e.mu.Lock()
		d := time.Until(e.notBefore[s])
		e.mu.Unlock()
		if d <= 0 {
			return ctx.Err()
		}
		if err := wait(ctx, d); err != nil {
			return err
		}
	}
}
func (e *Exporter) deferSignal(s otlpwire.Signal, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := time.Now().Add(d)
	if t.After(e.notBefore[s]) {
		e.notBefore[s] = t
	}
}
func clipped(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Send validates and exports one immutable request without cross-encoding or
// splitting it. OTLP partial/permanent/invalid responses return a terminal kind,
// even alongside a response-decoding error; the store must retain that outcome.
// Only transport failures without a response and protocol-retryable statuses
// are retried. Exhaustion never implies delivery. Caller buffers must not be
// mutated concurrently with this call.
func (e *Exporter) Send(ctx context.Context, in otlpstate.Envelope) (otlpstate.Outcome, error) {
	if ctx == nil {
		return otlpstate.Outcome{}, errors.New("nil OTLP context")
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return otlpstate.Outcome{}, err
	}
	s := otlpwire.Signal(in.Signal)
	endpoint, ok := e.cfg.Endpoints[s]
	if !ok {
		return otlpstate.Outcome{}, errors.New("OTLP signal endpoint not configured")
	}
	// Decoded input is not compressed yet. Wire limit applies after encoding.
	limits := e.cfg.RequestLimits
	limits.WireBytes = limits.DecodedBytes
	parsed, err := otlpwire.ReadRequest(s, in.ContentType, "", bytes.NewReader(in.Payload), limits)
	if err != nil || int64(parsed.Items()) != in.Items {
		return otlpstate.Outcome{}, errors.New("invalid OTLP envelope or item count")
	}
	body := parsed.Payload()
	if e.cfg.Gzip {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		if _, err = w.Write(body); err != nil {
			return otlpstate.Outcome{}, err
		}
		if err = w.Close(); err != nil {
			return otlpstate.Outcome{}, err
		}
		body = b.Bytes()
	}
	if int64(len(body)) > e.cfg.RequestLimits.WireBytes {
		return otlpstate.Outcome{}, otlpwire.ErrTooLarge
	}
	delay := e.cfg.InitialBackoff
	var last otlpstate.Outcome
	var lastErr error
	for attempt := 0; attempt < e.cfg.MaxAttempts; attempt++ {
		if err = e.waitAllowed(ctx, s); err != nil {
			return last, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return last, err
		}
		req.Header.Set("Content-Type", parsed.ContentType())
		req.Header.Set("Accept-Encoding", "gzip")
		if e.cfg.Gzip {
			req.Header.Set("Content-Encoding", "gzip")
		}
		if e.cfg.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+e.cfg.BearerToken)
		}
		resp, sendErr := e.client.Do(req)
		if sendErr != nil {
			last = otlpstate.Outcome{}
			lastErr = sendErr
			if resp != nil {
				resp.Body.Close()
			}
		} else {
			// Read one extra byte to detect overflow; retain only a bounded prefix.
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, e.cfg.ResponseBytes+1))
			resp.Body.Close()
			over := int64(len(raw)) > e.cfg.ResponseBytes
			if over {
				raw = raw[:e.cfg.ResponseBytes]
			}
			last = otlpstate.Outcome{Kind: otlpstate.Invalid, HTTPStatus: resp.StatusCode, ResponseContentType: clipped(resp.Header.Get("Content-Type"), 256), Response: raw, Truncated: over || readErr != nil}
			prefix := ""
			if enc := resp.Header.Get("Content-Encoding"); enc != "" {
				prefix = "response content-encoding=" + clipped(enc, 128) + "; "
			}
			if over || readErr != nil {
				last.Diagnostic = prefix + "incomplete or oversized OTLP response"
				if over {
					readErr = otlpwire.ErrTooLarge
				}
				return last, readErr
			}
			// Multiple representation headers are ambiguous, not permission to retry.
			if len(resp.Header.Values("Content-Type")) > 1 || len(resp.Header.Values("Content-Encoding")) > 1 {
				last.Diagnostic = "ambiguous OTLP response representation"
				return last, errors.New(last.Diagnostic)
			}
			out, decodeErr := otlpwire.ReadResponse(s, parsed.ContentType(), resp.StatusCode, resp.Header, bytes.NewReader(raw), otlpwire.Limits{WireBytes: e.cfg.ResponseBytes, DecodedBytes: e.cfg.ResponseBytes, Items: 1}, in.Items, time.Now())
			last.RejectedItems = out.Rejected
			last.Diagnostic = clipped(prefix+out.Diagnostic, 4096)
			switch out.Disposition {
			case otlpwire.Accepted:
				last.Kind = otlpstate.Accepted
			case otlpwire.PartiallyRejected:
				last.Kind = otlpstate.Partial
			case otlpwire.PermanentlyRejected:
				last.Kind = otlpstate.Permanent
			case otlpwire.Retryable:
				last.Kind = otlpstate.Retryable
			default:
				last.Kind = otlpstate.Invalid
			}
			if decodeErr != nil {
				last.Diagnostic = clipped(prefix+decodeErr.Error(), 4096)
			}
			if !out.MayRetry() {
				return last, decodeErr
			}
			lastErr = decodeErr
			if out.RetryAfter > 0 {
				e.deferSignal(s, out.RetryAfter)
			}
		}
		// A rejected response remains recorded in last. No hidden fourth attempt.
		if attempt+1 == e.cfg.MaxAttempts {
			break
		}
		jitter := time.Duration(rand.Int64N(int64(delay/5) + 1))
		if err = wait(ctx, delay+jitter); err != nil {
			return last, err
		}
		if delay < e.cfg.MaxBackoff/2 {
			delay *= 2
		} else {
			delay = e.cfg.MaxBackoff
		}
	}
	return last, lastErr
}
