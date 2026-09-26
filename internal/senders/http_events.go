package senders

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gofluentd/library"
	"gofluentd/library/streamformat"
)

// HTTPEventsSenderCfg describes an explicit whole-request acknowledgement
// contract. The recipient must accept every event before returning a 2xx status.
// No response body, redirect or partial per-item result is interpreted as ACK.
type HTTPEventsSenderCfg struct {
	Name, Addr, Format, Mode, BearerToken         string
	Tags                                          []string
	BatchSize, InChanSize, NFork, MaxAttempts     int
	MaxBodySize, MaxResponseBytes                 int64
	MaxWait, Timeout, RetryBackoff, MaxRetryDelay time.Duration
}

type HTTPEventsSender struct {
	BaseSender
	cfg        HTTPEventsSenderCfg
	httpClient *http.Client
}

func NewHTTPEventsSender(cfg HTTPEventsSenderCfg) (*HTTPEventsSender, error) {
	u, err := url.Parse(cfg.Addr)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("HTTP events sender requires an absolute HTTP(S) URL without userinfo or fragment")
	}
	if strings.TrimSpace(cfg.Name) == "" || len(cfg.Tags) == 0 || !streamformat.ValidFormat(cfg.Format) {
		return nil, fmt.Errorf("HTTP events sender requires name, tags and a supported format")
	}
	for _, tag := range cfg.Tags {
		if strings.TrimSpace(tag) == "" {
			return nil, fmt.Errorf("empty sender tag")
		}
	}
	if strings.ContainsAny(cfg.BearerToken, " \t\r\n") {
		return nil, fmt.Errorf("invalid bearer token")
	}
	if cfg.BatchSize < 0 || cfg.InChanSize < 0 || cfg.NFork < 0 || cfg.MaxAttempts < 0 || cfg.MaxBodySize < 0 || cfg.MaxResponseBytes < 0 || cfg.MaxWait < 0 || cfg.Timeout < 0 || cfg.RetryBackoff < 0 || cfg.MaxRetryDelay < 0 {
		return nil, fmt.Errorf("HTTP events sender limits must not be negative")
	}
	if cfg.Format == streamformat.CloudEvents {
		if cfg.Mode == "" {
			cfg.Mode = streamformat.Structured
		}
		if cfg.Mode != streamformat.Structured && cfg.Mode != streamformat.Binary && cfg.Mode != streamformat.Batch {
			return nil, fmt.Errorf("invalid CloudEvents mode")
		}
		if cfg.Mode != streamformat.Batch {
			if cfg.BatchSize > 1 {
				return nil, fmt.Errorf("structured and binary modes require batch size 1")
			}
			cfg.BatchSize = 1
		}
	} else if cfg.Mode != "" {
		return nil, fmt.Errorf("mode only applies to CloudEvents")
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 64
	}
	if cfg.InChanSize == 0 {
		cfg.InChanSize = 1024
	}
	if cfg.NFork == 0 {
		cfg.NFork = 1
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.MaxAttempts > 10 || cfg.BatchSize > 1024 || cfg.NFork > 128 {
		return nil, fmt.Errorf("sender attempts/batch/workers exceed 10/1024/128")
	}
	if cfg.MaxBodySize == 0 {
		cfg.MaxBodySize = 4 << 20
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 64 << 10
	}
	if cfg.MaxWait == 0 {
		cfg.MaxWait = 100 * time.Millisecond
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.RetryBackoff == 0 {
		cfg.RetryBackoff = 200 * time.Millisecond
	}
	if cfg.MaxRetryDelay == 0 {
		cfg.MaxRetryDelay = 30 * time.Second
	}
	s := &HTTPEventsSender{cfg: cfg, httpClient: &http.Client{
		Timeout: cfg.Timeout,
		// Never replay credentials/body to a redirect target or mistake a GET for ACK.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
			DialContext:       (&net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2: true, MaxIdleConns: 128, MaxIdleConnsPerHost: cfg.NFork,
			// Keep common event bodies inside the connection buffer. With a
			// smaller buffer, net/http's nested TCP ReadFrom path can allocate
			// a fresh copy buffer per request despite its outer buffer pool.
			// Request bodies remain privately owned and retry bytes unchanged.
			WriteBufferSize: 32 << 10,
			IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: cfg.Timeout,
			TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
			DisableCompression: true,
		},
	}}
	s.SetSupportedTags(cfg.Tags)
	return s, nil
}

func (s *HTTPEventsSender) GetName() string { return s.cfg.Name }

func (s *HTTPEventsSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	in := make(chan *library.FluentMsg, s.cfg.InChanSize)
	var workers sync.WaitGroup
	workers.Add(s.cfg.NFork)
	for i := 0; i < s.cfg.NFork; i++ {
		go func() {
			defer workers.Done()
			runBatchWorker(ctx, in, s.cfg.BatchSize, s.cfg.MaxWait, func(batch []*library.FluentMsg) bool {
				err := s.Send(ctx, batch)
				return s.reportBatch(ctx, batch, err == nil)
			})
		}()
	}
	go func() { workers.Wait(); s.httpClient.CloseIdleConnections() }()
	return in
}

// Send never mutates messages. Failures leave acknowledgement to the journal's
// retry/recovery path; only a fully received, bounded 2xx response means success.
func (s *HTTPEventsSender) Send(ctx context.Context, batch []*library.FluentMsg) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	records := make([]map[string]interface{}, len(batch))
	for i, m := range batch {
		if m == nil {
			return fmt.Errorf("nil event message")
		}
		records[i] = m.Message
	}
	body, headers, err := streamformat.Encode(s.cfg.Format, s.cfg.Mode, records)
	if err != nil {
		return err
	}
	if int64(len(body)) > s.cfg.MaxBodySize {
		return fmt.Errorf("encoded event batch exceeds max_body_byte")
	}
	if s.cfg.BearerToken != "" {
		headers.Set("Authorization", "Bearer "+s.cfg.BearerToken)
	}
	// Keep the caller's CloudEvents id and data untouched. This optional header is
	// local transport identity only and is omitted for multi-record requests.
	if len(batch) == 1 && batch[0].DeliveryID != "" {
		headers.Set("X-Go-Fluentd-ID", batch[0].DeliveryID)
	}
	for attempt := 0; attempt < s.cfg.MaxAttempts; attempt++ {
		retry, delay, sendErr := s.sendOnce(ctx, body, headers)
		if sendErr == nil {
			return nil
		}
		err = sendErr
		if !retry || attempt+1 == s.cfg.MaxAttempts {
			return err
		}
		backoff := s.cfg.RetryBackoff
		for n := 0; n < attempt && backoff < s.cfg.MaxRetryDelay; n++ {
			if backoff > s.cfg.MaxRetryDelay/2 {
				backoff = s.cfg.MaxRetryDelay
				break
			}
			backoff *= 2
		}
		if delay > backoff {
			backoff = delay
		}
		if backoff > s.cfg.MaxRetryDelay {
			backoff = s.cfg.MaxRetryDelay
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func (s *HTTPEventsSender) sendOnce(ctx context.Context, body []byte, headers http.Header) (bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.Addr, bytes.NewReader(body))
	if err != nil {
		return false, 0, err
	}
	req.Header = headers.Clone()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		// Do not include URL query strings, bearer tokens or payloads in diagnostics.
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		return true, 0, fmt.Errorf("HTTP event request failed (%T)", err)
	}
	defer resp.Body.Close()
	// Read one extra byte without max+1 overflow; do not mistake an oversized or
	// truncated response for a complete acknowledgement.
	n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, s.cfg.MaxResponseBytes))
	if readErr == nil && n == s.cfg.MaxResponseBytes {
		var extra [1]byte
		_, readErr = io.ReadFull(resp.Body, extra[:])
		if readErr == nil {
			return false, 0, fmt.Errorf("HTTP event response exceeds max_response_byte")
		}
		if readErr == io.EOF {
			readErr = nil
		}
	}
	if readErr != nil {
		return true, 0, fmt.Errorf("incomplete HTTP event response")
	}
	status := resp.StatusCode
	if status >= 200 && status < 300 {
		return false, 0, nil
	}
	delay := time.Duration(0)
	if seconds, ok := streamformat.RetrySeconds(resp.Header.Get("Retry-After")); ok {
		delay = seconds
	} else if deadline, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil && deadline.After(time.Now()) {
		delay = time.Until(deadline)
	}
	retry := status == 408 || status == 429 || status >= 500
	return retry, delay, fmt.Errorf("HTTP event destination returned status %d", status)
}
