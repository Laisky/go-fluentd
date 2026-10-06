package senders

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gofluentd/library"
)

type countingESBody struct {
	reader io.Reader
	closer io.Closer
	read   int
	closed bool
}

func (b *countingESBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *countingESBody) Close() error {
	b.closed = true
	if b.closer != nil {
		return b.closer.Close()
	}
	return nil
}

func TestRegressionESUntrustedResponseBound(t *testing.T) {
	const limit = 4 << 20
	for _, status := range []int{200, 503} {
		s := regressionES()
		payload := `{"errors":false}` + strings.Repeat(" ", limit+1024)
		body := &countingESBody{reader: strings.NewReader(payload)}
		s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, ContentLength: -1}, nil
		})}
		err := s.SendBulkMsgs(&bulkOpCtx{}, []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"message": "hello"}}})
		if err == nil {
			t.Errorf("status %d: oversized response accepted", status)
		}
		if body.read > limit+1 {
			t.Errorf("status %d: read %d response bytes; limit+1 is %d", status, body.read, limit+1)
		}
		if err != nil && len(err.Error()) > 512 {
			t.Errorf("status %d: diagnostic length %d exceeds bound", status, len(err.Error()))
		}
		if !body.closed {
			t.Error("response body not closed")
		}
	}
}

func TestRegressionESDiagnosticsDoNotEchoResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{403, "sentinel-response-secret"},
		{200, `{"errors":true,"items":[{"index":{"error":{"reason":"sentinel-response-secret"}}}]}`},
		{200, `{"errors":"sentinel-response-secret"}`},
	} {
		body := &countingESBody{reader: strings.NewReader(tc.body)}
		s := regressionES()
		s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Body: body}, nil
		})}
		err := s.SendBulkMsgs(&bulkOpCtx{}, []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"message": "hello"}}})
		if err == nil || strings.Contains(err.Error(), "sentinel-response-secret") {
			t.Errorf("status %d: absent or unsafe response diagnostic", tc.status)
		}
		if !body.closed {
			t.Error("response body not closed")
		}
	}
}

func TestRegressionESResponseBoundaries(t *testing.T) {
	for _, size := range []int{127, 128, 129, 4096} {
		for _, length := range []int64{-1, 0, 1, int64(size)} {
			s := regressionES()
			s.MaxResponseBytes = 128
			payload := `{"errors":false}` + strings.Repeat(" ", size-len(`{"errors":false}`))
			body := &countingESBody{reader: strings.NewReader(payload)}
			err := s.checkResp(&http.Response{StatusCode: 200, Body: body, ContentLength: length})
			if (err != nil) != (size > 128) || body.read > 129 {
				t.Errorf("size=%d hint=%d read=%d err=%v", size, length, body.read, err)
			}
		}
	}
	for _, payload := range []string{"", "null", `{}`, `{"errors":null}`, `{"errors":0}`, `{"errors":false}garbage`} {
		s := regressionES()
		err := s.checkResp(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))})
		if err == nil || retryESResponse(err) {
			t.Errorf("invalid result %q was accepted or retried", payload)
		}
	}
}

func TestRegressionESGzipAndChunkedDecodedLimit(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, size := range []int{128, 4096} {
			payload := `{"errors":false}` + strings.Repeat(" ", size-len(`{"errors":false}`))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if compressed {
					if r.Header.Get("Accept-Encoding") != "gzip" {
						t.Error("automatic gzip negotiation absent")
					}
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush() // Force chunked transfer, no Content-Length hint.
				if compressed {
					z := gzip.NewWriter(w)
					_, _ = z.Write([]byte(payload))
					_ = z.Close()
				} else {
					_, _ = io.WriteString(w, payload)
				}
			}))
			s := regressionES()
			s.Addr, s.MaxResponseBytes = server.URL, 128
			transport := &http.Transport{}
			var body *countingESBody
			s.httpClient = &http.Client{Transport: regressionRoundTripper(func(r *http.Request) (*http.Response, error) {
				resp, err := transport.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				if resp.ContentLength != -1 || resp.Uncompressed != compressed {
					t.Error("fixture did not exercise expected decoded/chunked response")
				}
				body = &countingESBody{reader: resp.Body, closer: resp.Body}
				resp.Body = body
				return resp, nil
			})}
			err := s.SendBulkMsgs(&bulkOpCtx{}, []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"value": 1}}})
			if (err != nil) != (size > 128) || body == nil || body.read > 129 || !body.closed {
				t.Errorf("gzip=%v size=%d err=%v body=%+v", compressed, size, err, body)
			}
			transport.CloseIdleConnections()
			server.Close()
		}
	}
}

type failingESReader struct{ err error }

func (r failingESReader) Read([]byte) (int, error) { return 0, r.err }
func TestRegressionESReadFailureClosesAndPreservesCause(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		s := regressionES()
		body := &countingESBody{reader: io.MultiReader(strings.NewReader(`{"errors":false}`), failingESReader{cause})}
		s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: body, ContentLength: -1}, nil
		})}
		err := s.SendBulkMsgs(&bulkOpCtx{}, []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{}}})
		if !errors.Is(err, cause) || !body.closed {
			t.Errorf("cause=%v err=%v closed=%v", cause, err, body.closed)
		}
	}
}

func TestRegressionESProtocolFailureDoesNotAcknowledgeOrAmplifyRetries(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status, attempts int
		payload          string
	}{
		{"oversize", 200, 1, `{"errors":false}` + strings.Repeat(" ", 129)},
		{"invalid", 200, 1, `{`},
		{"missing", 200, 1, `{}`},
		{"forbidden", 403, 1, "untrusted"},
		{"unavailable", 503, 4, "untrusted"},
		{"busy", 429, 4, "untrusted"},
		{"item_failure", 200, 4, `{"errors":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := regressionES()
			s.NFork, s.BatchSize, s.MaxResponseBytes = 1, 1, 128
			var calls atomic.Int64
			var bodies []*countingESBody
			s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				body := &countingESBody{reader: strings.NewReader(tc.payload)}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: tc.status, Body: body, ContentLength: -1}, nil
			})}
			good, bad := senderChannels(s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.Spawn(ctx)
			msg := &library.FluentMsg{Tag: "logs", JournalTag: "original", ID: 42, ExtIds: []int64{43, 44}, Message: map[string]interface{}{}}
			in <- msg
			if senderTake(t, bad) != msg || len(good) != 0 || calls.Load() != int64(tc.attempts) {
				t.Fatal("failure was lost, acknowledged, or retried incorrectly")
			}
			close(in)
			if msg.ID != 42 || msg.JournalTag != "original" || len(msg.ExtIds) != 2 {
				t.Fatal("retry identity changed")
			}
			for _, b := range bodies {
				if !b.closed || b.read > 129 {
					t.Fatal("response resource bound violated")
				}
			}
		})
	}
}

func TestRegressionESResponseLimitConfiguration(t *testing.T) {
	for _, limit := range []int64{-1, 0, 1, maximumESResponseBytes, maximumESResponseBytes + 1} {
		s := regressionES()
		s.MaxResponseBytes = limit
		err := s.valid()
		if (err != nil) != (limit < 0 || limit > maximumESResponseBytes) {
			t.Errorf("limit=%d err=%v", limit, err)
		}
		if limit == 0 && s.MaxResponseBytes != defaultESResponseBytes {
			t.Fatal("default missing")
		}
	}
}
