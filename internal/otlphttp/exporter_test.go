package otlphttp_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

type codec interface {
	UnmarshalJSON([]byte) error
	MarshalProto() ([]byte, error)
}

var signals = []otlpwire.Signal{otlpwire.Logs, otlpwire.Metrics, otlpwire.Traces}
var encodings = []string{otlpwire.JSON, otlpwire.Protobuf}

func fixture(t testing.TB, s otlpwire.Signal, ct string) otlpstate.Envelope {
	t.Helper()
	raw, err := os.ReadFile("../../library/otlpwire/testdata/" + string(s) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if ct == otlpwire.Protobuf {
		var c codec
		switch s {
		case otlpwire.Logs:
			c = plogotlp.NewExportRequest()
		case otlpwire.Metrics:
			c = pmetricotlp.NewExportRequest()
		case otlpwire.Traces:
			c = ptraceotlp.NewExportRequest()
		}
		if err = c.UnmarshalJSON(raw); err != nil {
			t.Fatal(err)
		}
		raw, err = c.MarshalProto()
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, 0xa0, 0x06, 0x07)
	} else {
		raw = append([]byte(`{"futureEnvelope":{"unknown":"keep"},`), bytes.TrimSpace(raw)[1:]...)
	}
	n := int64(1)
	if s == otlpwire.Metrics {
		n = 6
	}
	return otlpstate.Envelope{Signal: string(s), ContentType: ct, Payload: raw, Items: n}
}
func compressed(t testing.TB, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func config(endpoint string) otlphttp.ExporterConfig {
	return otlphttp.ExporterConfig{Endpoints: map[otlpwire.Signal]string{otlpwire.Logs: endpoint, otlpwire.Metrics: endpoint, otlpwire.Traces: endpoint}, Timeout: 3 * time.Second, InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
}
func exporter(t testing.TB, c otlphttp.ExporterConfig) *otlphttp.Exporter {
	t.Helper()
	e, err := otlphttp.NewExporter(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func TestOTLPHTTPExporterPreservesWire(t *testing.T) {
	for _, s := range signals {
		for _, ct := range encodings {
			for _, gz := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/gzip=%v", s, ct, gz), func(t *testing.T) {
					env := fixture(t, s, ct)
					var calls atomic.Int32
					bad := make(chan string, 10)
					peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						b, err := io.ReadAll(r.Body)
						if err != nil {
							bad <- err.Error()
						}
						if r.Header.Get("Content-Encoding") == "gzip" {
							z, err := gzip.NewReader(bytes.NewReader(b))
							if err != nil {
								bad <- err.Error()
								return
							}
							b, err = io.ReadAll(z)
							z.Close()
							if err != nil {
								bad <- err.Error()
							}
						}
						if r.Method != "POST" || r.URL.Path != "/custom/"+string(s) || r.Header.Get("Content-Type") != ct || r.Header.Get("Authorization") != "Bearer secret-test" || !bytes.Equal(b, env.Payload) {
							bad <- "wire request changed"
						}
						w.Header().Set("Content-Type", ct)
						w.Header().Set("Content-Encoding", "gzip")
						response := []byte{}
						if ct == otlpwire.JSON {
							response = []byte(`{}`)
						}
						w.Write(compressed(t, response))
					}))
					defer peer.Close()
					c := config(peer.URL + "/custom/" + string(s))
					c.Gzip = gz
					c.BearerToken = "secret-test"
					e := exporter(t, c)
					out, err := e.Send(context.Background(), env)
					if err != nil || out.Kind != otlpstate.Accepted || calls.Load() != 1 {
						t.Fatalf("acceptance: %+v %v calls=%d", out, err, calls.Load())
					}
					select {
					case why := <-bad:
						t.Fatal(why)
					default:
					}
					if strings.Contains(fmt.Sprint(out), c.BearerToken) {
						t.Fatal("credential leaked in evidence")
					}
					if !bytes.Equal(env.Payload, fixture(t, s, ct).Payload) {
						t.Fatal("caller payload mutated")
					}
				})
			}
		}
	}
}
func TestOTLPHTTPExporterResponsePolicy(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body, ct string
		kind     otlpstate.Kind
		calls    int
		wantErr  bool
	}{
		{"accepted", 200, `{}`, otlpwire.JSON, otlpstate.Accepted, 1, false},
		{"warning", 200, `{"partialSuccess":{"rejectedLogRecords":"0","errorMessage":"warning"}}`, otlpwire.JSON, otlpstate.Accepted, 1, false},
		{"partial", 200, `{"partialSuccess":{"rejectedLogRecords":"1","errorMessage":"rejected"}}`, otlpwire.JSON, otlpstate.Partial, 1, false},
		{"400", 400, `{}`, otlpwire.JSON, otlpstate.Permanent, 1, false},
		{"408", 408, `{}`, otlpwire.JSON, otlpstate.Permanent, 1, false},
		{"500", 500, `{}`, otlpwire.JSON, otlpstate.Permanent, 1, false},
		{"204-not-success", 204, "", otlpwire.JSON, otlpstate.Permanent, 1, false},
		{"429", 429, `{}`, otlpwire.JSON, otlpstate.Retryable, 3, false},
		{"502", 502, `{}`, otlpwire.JSON, otlpstate.Retryable, 3, false},
		{"503", 503, `{}`, otlpwire.JSON, otlpstate.Retryable, 3, false},
		{"504", 504, `{}`, otlpwire.JSON, otlpstate.Retryable, 3, false},
		{"malformed", 200, `{`, otlpwire.JSON, otlpstate.Invalid, 1, true},
		{"mismatched-type", 200, `{}`, otlpwire.Protobuf, otlpstate.Invalid, 1, true},
		{"too-many-rejected", 200, `{"partialSuccess":{"rejectedLogRecords":"2"}}`, otlpwire.JSON, otlpstate.Invalid, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", tc.ct)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer peer.Close()
			out, err := exporter(t, config(peer.URL)).Send(context.Background(), fixture(t, otlpwire.Logs, otlpwire.JSON))
			if out.Kind != tc.kind || (err != nil) != tc.wantErr || int(calls.Load()) != tc.calls {
				t.Fatalf("response policy: %+v %v calls=%d", out, err, calls.Load())
			}
			if string(out.Response) != tc.body {
				t.Fatal("raw response evidence changed")
			}
		})
	}
}
func TestOTLPHTTPExporterFailureBounds(t *testing.T) {
	for _, kind := range []string{"wire-limit", "gzip-limit", "bad-gzip", "truncated", "duplicate-type"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", otlpwire.JSON)
				switch kind {
				case "wire-limit":
					w.WriteHeader(503)
					io.WriteString(w, strings.Repeat("x", 257))
				case "gzip-limit":
					w.Header().Set("Content-Encoding", "gzip")
					w.WriteHeader(503)
					w.Write(compressed(t, bytes.Repeat([]byte("x"), 2048)))
				case "bad-gzip":
					w.Header().Set("Content-Encoding", "gzip")
					w.Write([]byte("broken"))
				case "duplicate-type":
					w.Header().Add("Content-Type", otlpwire.Protobuf)
					io.WriteString(w, `{}`)
				case "truncated":
					c, b, err := w.(http.Hijacker).Hijack()
					if err != nil {
						return
					}
					fmt.Fprint(b, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{}")
					b.Flush()
					c.Close()
				}
			}))
			defer peer.Close()
			c := config(peer.URL)
			c.ResponseBytes = 256
			out, err := exporter(t, c).Send(context.Background(), fixture(t, otlpwire.Logs, otlpwire.JSON))
			if err == nil || out.Kind != otlpstate.Invalid || calls.Load() != 1 || len(out.Response) > 256 {
				t.Fatalf("bounded invalid response: %+v %v calls=%d", out, err, calls.Load())
			}
			if (kind == "wire-limit" || kind == "truncated") && !out.Truncated {
				t.Fatal("unmarked incomplete evidence")
			}
		})
	}
}
func TestOTLPHTTPExporterRetryAndCancel(t *testing.T) {
	t.Run("same-bytes-after-disconnect", func(t *testing.T) {
		var calls atomic.Int32
		first := make(chan []byte, 1)
		bad := make(chan bool, 1)
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			if calls.Add(1) == 1 {
				first <- b
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
				return
			}
			if !bytes.Equal(b, <-first) {
				bad <- true
			}
			w.Header().Set("Content-Type", otlpwire.JSON)
			io.WriteString(w, `{}`)
		}))
		defer peer.Close()
		c := config(peer.URL)
		c.Gzip = true
		o, err := exporter(t, c).Send(context.Background(), fixture(t, otlpwire.Logs, otlpwire.JSON))
		if err != nil || o.Kind != otlpstate.Accepted || calls.Load() != 2 {
			t.Fatalf("retry: %+v %v %d", o, err, calls.Load())
		}
		select {
		case <-bad:
			t.Fatal("retry bytes changed")
		default:
		}
	})
	t.Run("retry-after-across-calls", func(t *testing.T) {
		var calls atomic.Int32
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(503)
		}))
		defer peer.Close()
		c := config(peer.URL)
		c.MaxAttempts = 1
		e := exporter(t, c)
		env := fixture(t, otlpwire.Logs, otlpwire.JSON)
		o, err := e.Send(context.Background(), env)
		if err != nil || o.Kind != otlpstate.Retryable {
			t.Fatal(o, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err = e.Send(ctx, env)
		if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
			t.Fatalf("server delay bypassed: %v %d", err, calls.Load())
		}
	})
	t.Run("bounded-call", func(t *testing.T) {
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
		defer peer.Close()
		c := config(peer.URL)
		c.Timeout = 30 * time.Millisecond
		_, err := exporter(t, c).Send(context.Background(), fixture(t, otlpwire.Logs, otlpwire.JSON))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call not canceled: %v", err)
		}
	})
}
func TestOTLPHTTPExporterTLSAndRedirect(t *testing.T) {
	env := fixture(t, otlpwire.Logs, otlpwire.JSON)
	var calls atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", otlpwire.JSON)
		io.WriteString(w, `{}`)
	}))
	defer peer.Close()
	c := config(peer.URL)
	c.MaxAttempts = 1
	if o, err := exporter(t, c).Send(context.Background(), env); err == nil || o.Kind == otlpstate.Accepted || calls.Load() != 0 {
		t.Fatal("untrusted certificate accepted")
	}
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	c.RootCAs = roots
	if o, err := exporter(t, c).Send(context.Background(), env); err != nil || o.Kind != otlpstate.Accepted {
		t.Fatal(o, err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, peer.URL, 307) }))
	defer redirect.Close()
	c.Endpoints = map[otlpwire.Signal]string{otlpwire.Logs: redirect.URL}
	if o, err := exporter(t, c).Send(context.Background(), env); err != nil || o.Kind != otlpstate.Permanent || calls.Load() != 1 {
		t.Fatal("redirect followed", o, err, calls.Load())
	}
}
func TestOTLPHTTPExporterRejectsInvalidBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer peer.Close()
	e := exporter(t, config(peer.URL))
	for _, mutate := range []func(*otlpstate.Envelope){func(e *otlpstate.Envelope) { e.Signal = "profiles" }, func(e *otlpstate.Envelope) { e.ContentType = "text/plain" }, func(e *otlpstate.Envelope) { e.Items++ }, func(e *otlpstate.Envelope) { e.Payload = []byte("bad") }} {
		env := fixture(t, otlpwire.Logs, otlpwire.JSON)
		mutate(&env)
		if _, err := e.Send(context.Background(), env); err == nil {
			t.Fatal("invalid input allowed")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input contacted peer")
	}
	for _, url := range []string{"ftp://example.org", "http://user:password@example.org", "http://example.org?token=x", "http://example.org#fragment", "://"} {
		if _, err := otlphttp.NewExporter(config(url)); err == nil {
			t.Fatal("invalid URL accepted", url)
		}
	}
}
