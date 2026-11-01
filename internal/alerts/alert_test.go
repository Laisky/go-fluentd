package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"gofluentd/library/otlpwire"
)

// inert builds an observer from validated defaults without starting its workers.
func inert() *Observer {
	o, err := newObserver(context.Background(), Config{Token: "123:synthetic", ChatID: "synthetic", Tags: []string{"app"}})
	if err != nil {
		panic(err)
	}
	return o
}

func TestFluentSeveritySourceAndDedup(t *testing.T) {
	o := inert()
	for _, x := range []struct {
		tag   string
		level interface{}
		want  bool
	}{{"app", "error", true}, {"app", "ERROR", false}, {"app", "fatal", true}, {"app", "panic", true}, {"app", "warn", false}, {"app", 12, false}, {"app", "errorish", false}, {"other", "error", false}} {
		before := o.Stats().Queued
		o.ObserveFluent(x.tag, map[string]interface{}{"level": x.level, "msg": fmt.Sprint(x.level)})
		if got := o.Stats().Queued > before; got != x.want {
			t.Errorf("%v: got %v", x, got)
		}
	}
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "error"})
	if o.Stats().Suppressed == 0 {
		t.Fatal("duplicate not suppressed")
	}
}
func TestFluentEmbeddedJSON(t *testing.T) {
	o := inert()
	o.cfg.IncludeMessage = true
	o.ObserveFluent("app", map[string]interface{}{"log": []byte(`{"level":"ERROR","msg":"synthetic"}`)})
	if o.Stats().Queued != 1 || (<-o.queue).Message != "synthetic" {
		t.Fatal("embedded JSON not observed")
	}
	o.ObserveFluent("app", map[string]interface{}{"level": 17, "log": `{"level":"error"}`})
	o.ObserveFluent("app", map[string]interface{}{"log": `{"level":17}`})
	if o.Stats().Queued != 1 {
		t.Fatal("malformed severity accepted")
	}
}
func TestConcurrentAndBounded(t *testing.T) {
	o := inert()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o.Observe(Event{Source: fmt.Sprint(i), Severity: "error", Message: "synthetic"})
		}(i)
	}
	wg.Wait()
	s := o.Stats()
	if s.Queued != 64 || s.Dropped != 136 {
		t.Fatalf("queue counters: %+v", s)
	}
}
func TestRedactionAndUnicode(t *testing.T) {
	s := clean(`密码 🙂 token=synthetic-secret {"password":"synthetic password words"} Authorization: Bearer synthetic-auth https://example.test/private `+strings.Repeat("😀", 2000), 1400)
	if strings.Contains(s, "synthetic-secret") || strings.Contains(s, "synthetic password words") || strings.Contains(s, "synthetic-auth") || strings.Contains(s, "example.test") || len([]rune(s)) > 1401 {
		t.Fatal("unsafe or unbounded output")
	}
	if len(utf16.Encode([]rune(s))) > 2802 {
		t.Fatal("UTF-16 bound exceeded")
	}
	o := inert()
	o.cfg.IncludeMessage = true
	o.Observe(Event{Source: "app", Severity: "error", Message: strings.Repeat("x", 8193)})
	if (<-o.queue).Message != "[oversized message omitted]" {
		t.Fatal("oversized message retained")
	}
}
func TestOTLPQueueSaturation(t *testing.T) {
	o := inert()
	o.cfg.Services = []string{"svc"}
	raw := `{"resourceLogs":[]}`
	r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(raw), otlpwire.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		o.ObserveOTLP(r)
	}
	if len(o.otlp) != 4 || o.Stats().Dropped != 1 {
		t.Fatalf("OTLP queue: %+v", o.Stats())
	}
}
func TestHTTPOutcomes(t *testing.T) {
	var status atomic.Int64
	status.Store(200)
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		switch status.Load() {
		case 200:
			io.WriteString(w, `{"ok":true}`)
		case 429:
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"parameters":{"retry_after":3}}`)
		case 500:
			w.WriteHeader(500)
		default:
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic"}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	retry, _ := o.send(ctx, "hello\n世界")
	if retry || o.Stats().Sent != 1 || !json.Valid([]byte(captured)) {
		t.Fatal("success")
	}
	status.Store(429)
	retry, d := o.send(ctx, "test")
	if !retry || d != 3*time.Second {
		t.Fatalf("429 %v %v", retry, d)
	}
	status.Store(500)
	retry, _ = o.send(ctx, "test")
	if !retry {
		t.Fatal("5xx not retried")
	}
	status.Store(400)
	retry, _ = o.send(ctx, "test")
	if retry || o.Stats().Failed != 1 {
		t.Fatal("permanent failure")
	}
}
func TestCancellationAndTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	client := server.Client()
	client.Timeout = 20 * time.Millisecond
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic"}, server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	retry, _ := o.send(ctx, "test")
	if !retry {
		t.Fatal("timeout should be retryable")
	}
	cancel()
	done := make(chan struct{})
	go func() { o.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
}
func TestOTLPOriginalSeverityAndSources(t *testing.T) {
	o := inert()
	o.cfg.IncludeMessage = true
	o.cfg.Services = []string{"svc"}
	raw := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeLogs":[{"scope":{"name":"scope"},"logRecords":[{"severityNumber":17,"body":{"stringValue":"boom"}},{"severityNumber":9,"severityText":"ERROR"},{"severityText":"ERROR","body":{"stringValue":"second"}},{"severityText":"garbage"}]}]}]}`
	r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(raw), otlpwire.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	o.ObserveOTLP(r)
	o.parseOTLP(<-o.otlp)
	<-o.otlpSlots
	if o.Stats().Queued != 2 {
		t.Fatalf("stats %+v", o.Stats())
	}
	first := <-o.queue
	if first.Source != "otlp/svc/scope" || first.Message != "boom" {
		t.Fatalf("event %+v", first)
	}
	o.ObserveOTLP(r)
	o.parseOTLP(<-o.otlp)
	<-o.otlpSlots
	if o.Stats().Suppressed != 2 {
		t.Fatal("retry duplicates not suppressed")
	}
}
func TestWorkerRetries429AndRateLimits(t *testing.T) {
	calls := make(chan time.Time, 4)
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- time.Now()
		switch n.Add(1) {
		case 1:
			w.WriteHeader(429)
			io.WriteString(w, `{"ok":false,"parameters":{"retry_after":1}}`)
			return
		case 2:
			w.WriteHeader(500)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic"}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	o.Observe(Event{Source: "one", Severity: "error", Message: "a"})
	o.Observe(Event{Source: "two", Severity: "error", Message: "b"})
	var times []time.Time
	for len(times) < 4 {
		select {
		case tm := <-calls:
			times = append(times, tm)
		case <-time.After(10 * time.Second):
			t.Fatalf("calls=%d stats=%+v", len(times), o.Stats())
		}
	}
	if times[1].Sub(times[0]) < 1900*time.Millisecond || times[2].Sub(times[1]) < 2900*time.Millisecond || times[3].Sub(times[2]) < 900*time.Millisecond {
		t.Fatal("retry-after, backoff or rate interval not honored")
	}
}
func TestDisabledObserverIsNoop(t *testing.T) {
	var o *Observer
	o.ObserveFluent("app", map[string]interface{}{"level": "error"})
	o.ObserveOTLP(nil)
	o.Observe(Event{Severity: "error"})
}
