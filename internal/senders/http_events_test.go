package senders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/library"
)

func eventSender(t *testing.T, addr, format, mode string, edit func(*HTTPEventsSenderCfg)) *HTTPEventsSender {
	t.Helper()
	cfg := HTTPEventsSenderCfg{Name: "events", Addr: addr, Format: format, Mode: mode, Tags: []string{"events.test"}, MaxWait: 10 * time.Millisecond, Timeout: time.Second, RetryBackoff: time.Millisecond, MaxRetryDelay: 10 * time.Millisecond}
	if edit != nil {
		edit(&cfg)
	}
	s, err := NewHTTPEventsSender(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.httpClient.CloseIdleConnections)
	return s
}
func eventMsg() *library.FluentMsg {
	return &library.FluentMsg{ID: 7, Tag: "events.test", SourceFormat: "cloudevents", DeliveryID: "node-7", Message: map[string]interface{}{
		"specversion": "1.0", "id": "caller-7", "source": "/orders", "type": "order.created", "subject": "café % quote\"",
		"data": map[string]interface{}{"n": uint64(18446744073709551615), "text": "世界", "bool": true}}}
}
func eventResponse(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("missing sender result")
		return nil
	}
}
func TestComponentHTTPEventSenderWireAndRetry(t *testing.T) {
	for _, mode := range []string{"ndjson", "structured", "binary", "batch"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var bodies [][]byte
			var headers []http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != "/events" {
					t.Error("wrong method/path")
				}
				mu.Lock()
				bodies = append(bodies, b)
				headers = append(headers, r.Header.Clone())
				n := len(bodies)
				mu.Unlock()
				if n == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(204)
			}))
			defer server.Close()
			format := "cloudevents"
			wireMode := mode
			if mode == "ndjson" {
				format = "ndjson"
				wireMode = ""
			}
			s := eventSender(t, server.URL+"/events", format, wireMode, func(c *HTTPEventsSenderCfg) { c.BearerToken = "secret" })
			m := eventMsg()
			before, _ := json.Marshal(m.Message)
			if err := s.Send(context.Background(), []*library.FluentMsg{m}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
				t.Fatalf("retry changed body: %q", bodies)
			}
			after, _ := json.Marshal(m.Message)
			if string(before) != string(after) || m.Tag != "events.test" || m.ID != 7 {
				t.Fatal("mutated message")
			}
			h, b := headers[1], bodies[1]
			if h.Get("Authorization") != "Bearer secret" || h.Get("X-Go-Fluentd-ID") != "node-7" {
				t.Fatal("transport metadata changed")
			}
			if !strings.Contains(string(b), "18446744073709551615") {
				t.Fatal("integer changed")
			}
			var got interface{}
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.UseNumber()
			if err := dec.Decode(&got); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "ndjson":
				if h.Get("Content-Type") != "application/x-ndjson" || b[len(b)-1] != '\n' {
					t.Fatal("bad NDJSON framing")
				}
			case "structured":
				if h.Get("Content-Type") != "application/cloudevents+json" {
					t.Fatal(h)
				}
			case "batch":
				if h.Get("Content-Type") != "application/cloudevents-batch+json" {
					t.Fatal(h)
				}
				a, ok := got.([]interface{})
				if !ok || len(a) != 1 {
					t.Fatal("batch array")
				}
				got = a[0]
			case "binary":
				if h.Get("Ce-Id") != "caller-7" || h.Get("Ce-Source") != "/orders" || h.Get("Content-Type") != "application/json" || !strings.Contains(h.Get("Ce-Subject"), "%C3%A9") {
					t.Fatalf("bad binary metadata: %v", h)
				}
			}
			if mode != "binary" {
				if got.(map[string]interface{})["id"] != "caller-7" {
					t.Fatal("producer identity replaced")
				}
			} else if got.(map[string]interface{})["text"] != "世界" {
				t.Fatal("binary data changed")
			}
		})
	}
}

func TestComponentHTTPEventSenderFailureStatusAndBody(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 301, 302, 303, 307, 308, 408, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var attempts atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed redirect"); w.WriteHeader(204) }))
			defer target.Close()
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer peer.Close()
			s := eventSender(t, peer.URL, "cloudevents", "structured", nil)
			if err := s.Send(context.Background(), []*library.FluentMsg{eventMsg()}); err == nil {
				t.Fatal("failure acknowledged")
			}
			want := int32(1)
			if status == 408 || status == 429 || status >= 500 {
				want = 3
			}
			if attempts.Load() != want {
				t.Fatalf("attempts=%d want=%d", attempts.Load(), want)
			}
		})
	}
	for _, fault := range []string{"oversized", "truncated"} {
		t.Run(fault, func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if fault == "truncated" {
					w.Header().Set("Content-Length", "20")
					w.WriteHeader(200)
					_, _ = w.Write([]byte("x"))
					return
				}
				_, _ = w.Write([]byte("123456789"))
			}))
			defer peer.Close()
			s := eventSender(t, peer.URL, "ndjson", "", func(c *HTTPEventsSenderCfg) { c.MaxResponseBytes = 8 })
			if err := s.Send(context.Background(), []*library.FluentMsg{eventMsg()}); err == nil {
				t.Fatal("bad success body accepted")
			}
		})
	}
	for _, n := range []int{0, 7, 8} {
		t.Run(fmt.Sprintf("exact-boundary-%d", n), func(t *testing.T) {
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", n))) }))
			defer peer.Close()
			s := eventSender(t, peer.URL, "ndjson", "", func(c *HTTPEventsSenderCfg) { c.MaxResponseBytes = 8 })
			if err := s.Send(context.Background(), []*library.FluentMsg{eventMsg()}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestComponentHTTPEventSenderQueueCloseAndFailureReceipt(t *testing.T) {
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			var requests atomic.Int32
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if bad {
					w.WriteHeader(400)
				} else {
					w.WriteHeader(204)
				}
			}))
			defer peer.Close()
			s := eventSender(t, peer.URL, "ndjson", "", func(c *HTTPEventsSenderCfg) { c.BatchSize = 8; c.MaxWait = time.Hour })
			good, failed := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
			s.SetSuccessedChan(good)
			s.SetFailedChan(failed)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.Spawn(ctx)
			a, b := eventMsg(), eventMsg()
			b.ID = 8
			in <- a
			out := good
			if bad {
				out = failed
			}
			if eventResponse(t, out) != a {
				t.Fatal("wrong first result")
			}
			in <- b
			close(in)
			if eventResponse(t, out) != b {
				t.Fatal("normal input close dropped partial batch")
			}
			if requests.Load() != 2 {
				t.Fatalf("unexpected requests %d", requests.Load())
			}
			other := failed
			if bad {
				other = good
			}
			if len(other) != 0 {
				t.Fatal("both success and failure reported")
			}
		})
	}
}

func TestComponentHTTPEventSenderCancelAndRetryAfter(t *testing.T) {
	entered := make(chan struct{}, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		w.Header().Set("Retry-After", "99999")
		w.WriteHeader(429)
	}))
	defer peer.Close()
	s := eventSender(t, peer.URL, "ndjson", "", func(c *HTTPEventsSenderCfg) { c.MaxRetryDelay = time.Hour })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Send(ctx, []*library.FluentMsg{eventMsg()}) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled retry succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
	if err := s.Send(ctx, []*library.FluentMsg{eventMsg()}); err != context.Canceled {
		t.Fatalf("canceled before send: %v", err)
	}
	for _, value := range []string{"1", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		t.Run(value, func(t *testing.T) {
			var times []time.Time
			var mu sync.Mutex
			p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				times = append(times, time.Now())
				n := len(times)
				mu.Unlock()
				if n == 1 {
					w.Header().Set("Retry-After", value)
					w.WriteHeader(503)
				} else {
					w.WriteHeader(204)
				}
			}))
			defer p.Close()
			sender := eventSender(t, p.URL, "ndjson", "", nil)
			if err := sender.Send(context.Background(), []*library.FluentMsg{eventMsg()}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(times) != 2 || times[1].Sub(times[0]) < 8*time.Millisecond {
				t.Fatal("ignored Retry-After/clamped delay")
			}
		})
	}
}

func TestComponentHTTPEventSenderValidation(t *testing.T) {
	valid := HTTPEventsSenderCfg{Name: "events", Addr: "https://example.test/events", Tags: []string{"events.test"}, Format: "ndjson"}
	edits := []func(*HTTPEventsSenderCfg){
		func(c *HTTPEventsSenderCfg) { c.Name = "" }, func(c *HTTPEventsSenderCfg) { c.Addr = "ftp://example.test" }, func(c *HTTPEventsSenderCfg) { c.Addr = "http://user:pass@example.test" }, func(c *HTTPEventsSenderCfg) { c.Addr = "http://example.test/#secret" }, func(c *HTTPEventsSenderCfg) { c.Tags = nil }, func(c *HTTPEventsSenderCfg) { c.Tags = []string{""} }, func(c *HTTPEventsSenderCfg) { c.Format = "otlp" }, func(c *HTTPEventsSenderCfg) { c.Mode = "binary" }, func(c *HTTPEventsSenderCfg) { c.Format = "cloudevents"; c.Mode = "unknown" }, func(c *HTTPEventsSenderCfg) { c.Format = "cloudevents"; c.Mode = "structured"; c.BatchSize = 2 }, func(c *HTTPEventsSenderCfg) { c.BearerToken = "bad\nsecret" }, func(c *HTTPEventsSenderCfg) { c.MaxAttempts = 11 }, func(c *HTTPEventsSenderCfg) { c.NFork = 129 }, func(c *HTTPEventsSenderCfg) { c.BatchSize = 1025 }, func(c *HTTPEventsSenderCfg) { c.Timeout = -1 }, func(c *HTTPEventsSenderCfg) { c.InChanSize = -1 }, func(c *HTTPEventsSenderCfg) { c.MaxBodySize = -1 },
	}
	for n, edit := range edits {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			c := valid
			edit(&c)
			if _, err := NewHTTPEventsSender(c); err == nil {
				t.Fatal("bad config accepted")
			}
		})
	}
	s, err := NewHTTPEventsSender(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer s.httpClient.CloseIdleConnections()
	if !s.IsTagSupported("events.test") || s.DiscardWhenBlocked() || s.GetName() != "events" {
		t.Fatal("routing/defaults")
	}
	if err := s.Send(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), []*library.FluentMsg{nil}); err == nil {
		t.Fatal("nil accepted")
	}
	bad := eventMsg()
	bad.Message = map[string]interface{}{"bad": make(chan int)}
	if err := s.Send(context.Background(), []*library.FluentMsg{bad}); err == nil {
		t.Fatal("unencodable accepted")
	}
	s.cfg.MaxBodySize = 1
	before := eventMsg()
	copy := eventMsg()
	if err := s.Send(context.Background(), []*library.FluentMsg{before}); err == nil || !reflect.DeepEqual(before, copy) {
		t.Fatal("size rejection or immutability failed")
	}
}
