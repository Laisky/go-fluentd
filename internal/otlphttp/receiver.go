package otlphttp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"time"

	"gofluentd/library/otlpwire"
)

// Admission returns nil only after the immutable request and its frozen
// destination plan have been journaled and synchronized. It must honor ctx.
// A returned error can be an unknown outcome, never an atomic rollback promise.
type Admission func(context.Context, *otlpwire.Request) error

type ReceiverConfig struct {
	Limits                   otlpwire.Limits
	BearerToken              string
	MaxConcurrent            int
	Timeout, BodyReadTimeout time.Duration
}

type Receiver struct {
	cfg    ReceiverConfig
	admit  Admission
	active chan struct{}
}

// NewReceiver creates a standalone http.Handler for the three exact OTLP paths.
// It neither starts a listener nor registers YAML. Mount on a dedicated server
// with TLS, header limits and ReadHeaderTimeout; MaxConcurrent limits active
// body decoding/admission, not the HTTP server's idle connection count.
func NewReceiver(c ReceiverConfig, admit Admission) (*Receiver, error) {
	if admit == nil {
		return nil, errors.New("OTLP admission callback required")
	}
	if c.Limits == (otlpwire.Limits{}) {
		c.Limits = otlpwire.DefaultLimits()
	}
	if c.Limits.WireBytes <= 0 || c.Limits.WireBytes > 64<<20 || c.Limits.DecodedBytes <= 0 || c.Limits.DecodedBytes > 64<<20 || c.Limits.Items <= 0 {
		return nil, errors.New("invalid OTLP receiver limits")
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 16
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.BodyReadTimeout == 0 {
		c.BodyReadTimeout = 10 * time.Second
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 1024 || c.Timeout <= 0 || c.Timeout > time.Hour || c.BodyReadTimeout <= 0 || c.BodyReadTimeout > c.Timeout || !validToken(c.BearerToken) {
		return nil, errors.New("invalid OTLP admission policy")
	}
	return &Receiver{cfg: c, admit: admit, active: make(chan struct{}, c.MaxConcurrent)}, nil
}

func (h *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Prefer protobuf for failures whose unsupported request type is unknown.
	ct := otlpwire.Protobuf
	if m, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && m == otlpwire.JSON {
		ct = otlpwire.JSON
	}
	var s otlpwire.Signal
	switch r.URL.Path {
	case "/v1/logs":
		s = otlpwire.Logs
	case "/v1/metrics":
		s = otlpwire.Metrics
	case "/v1/traces":
		s = otlpwire.Traces
	default:
		writeFailure(w, ct, 404, "unknown OTLP endpoint")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeFailure(w, ct, 405, "OTLP requires POST")
		return
	}
	if h.cfg.BearerToken != "" {
		a, b := sha256.Sum256([]byte(r.Header.Get("Authorization"))), sha256.Sum256([]byte("Bearer "+h.cfg.BearerToken))
		if len(r.Header.Values("Authorization")) != 1 || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
			writeFailure(w, ct, 401, "OTLP authentication failed")
			return
		}
	}
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("Content-Encoding")) > 1 {
		writeFailure(w, ct, 415, "ambiguous OTLP representation")
		return
	}
	select {
	case h.active <- struct{}{}:
		defer func() { <-h.active }()
	default:
		w.Header().Set("Retry-After", "1")
		writeFailure(w, ct, 503, "OTLP admission capacity exhausted")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
	defer cancel()
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(h.cfg.BodyReadTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	// Real HTTP servers support this. Wrappers must expose Unwrap, or enforce
	// equivalent read deadlines themselves. A context alone cannot unblock Read.
	err := rc.SetReadDeadline(deadline)
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeFailure(w, ct, 503, "cannot set body deadline")
		return
	}
	req, err := otlpwire.ReadRequest(s, r.Header.Get("Content-Type"), r.Header.Get("Content-Encoding"), r.Body, h.cfg.Limits)
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil {
		code, msg := 400, "invalid OTLP request"
		var networkErr net.Error
		switch {
		case errors.Is(err, otlpwire.ErrTooLarge):
			code, msg = 413, "OTLP request exceeds limits"
		case errors.Is(err, otlpwire.ErrMediaType):
			code, msg = 415, "unsupported OTLP representation"
		case ctx.Err() != nil || (errors.As(err, &networkErr) && networkErr.Timeout()):
			code, msg = 503, "OTLP admission canceled"
		}
		writeFailure(w, ct, code, msg)
		return
	}
	if err := ctx.Err(); err != nil {
		writeFailure(w, ct, 503, "OTLP admission canceled")
		return
	}
	// This callback owns durable admission, not merely a send on an in-memory queue.
	if err := h.admit(ctx, req); err != nil {
		writeFailure(w, ct, 503, "OTLP durable admission failed")
		return
	}
	if err := ctx.Err(); err != nil {
		writeFailure(w, ct, 503, "OTLP admission canceled")
		return
	}
	body, _ := otlpwire.SuccessBody(s, req.ContentType())
	w.Header().Set("Content-Type", req.ContentType())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// google.rpc.Status.message is protobuf field 2. OTLP permits omitting code;
// emit that minimal Status rather than an Export response or HTML on failures.
// Tests decode these bytes with the independent generated Status message.
func writeFailure(w http.ResponseWriter, ct string, code int, message string) {
	var body []byte
	if ct == otlpwire.JSON {
		body, _ = json.Marshal(struct {
			Message string `json:"message"`
		}{message})
	} else {
		body = binary.AppendUvarint([]byte{0x12}, uint64(len(message)))
		body = append(body, message...)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// Keep the accepted encodings in the protocol parser; never guess a signal from
// protobuf field numbers. The request's endpoint determines the signal.
var _ http.Handler = (*Receiver)(nil)
