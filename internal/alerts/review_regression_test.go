package alerts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gofluentd/library/otlpwire"
)

func reviewRequest(t *testing.T, scope string) *otlpwire.Request {
	return reviewRequestParts(t, "host", scope, "ERROR")
}
func reviewRequestParts(t *testing.T, host, scope, severity string) *otlpwire.Request {
	t.Helper()
	doc := map[string]interface{}{"resourceLogs": []interface{}{map[string]interface{}{"resource": map[string]interface{}{"attributes": []interface{}{map[string]interface{}{"key": "service.name", "value": map[string]string{"stringValue": "svc"}}, map[string]interface{}{"key": "host.name", "value": map[string]string{"stringValue": host}}}}, "scopeLogs": []interface{}{map[string]interface{}{"scope": map[string]string{"name": scope}, "logRecords": []interface{}{map[string]interface{}{"severityNumber": 17, "severityText": severity, "body": map[string]string{"stringValue": "synthetic"}}}}}}}}
	b, _ := json.Marshal(doc)
	r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(string(b)), otlpwire.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestReviewDecodeContinuesDuringTelegramBackoff(t *testing.T) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"parameters":{"retry_after":3600}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"svc"}, EventQueueSize: 2, OTLPQueueSize: 2, SendInterval: 100 * time.Millisecond}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "first"})
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("no first request")
	}
	await := func(check func(Stats) bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if check(o.Stats()) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("OTLP processing stalled in backoff: %+v", o.Stats())
	}
	o.ObserveOTLP(reviewRequest(t, "worker"))
	await(func(s Stats) bool { return s.Queued == 2 })
	o.ObserveOTLP(reviewRequest(t, "worker-2"))
	await(func(s Stats) bool { return s.Queued == 3 })
	o.ObserveOTLP(reviewRequest(t, "worker-3"))
	await(func(s Stats) bool { return s.DroppedEvents == 1 })
	if s := o.Stats(); s.EventQueueDepth != 2 || s.DroppedRequests != 0 {
		t.Fatalf("unexpected bounded-loss accounting: %+v", s)
	}
}
func TestReviewMetadataCannotInjectMessageField(t *testing.T) {
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		bodies <- p.Text
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"svc"}}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	o.ObserveOTLP(reviewRequest(t, "worker\nmessage: synthetic-injected"))
	select {
	case text := <-bodies:
		if strings.Contains(text, "\nmessage:") || !strings.Contains(text, "worker") {
			t.Fatalf("metadata injected message line: %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no notification")
	}
}

func TestReviewHostScopeAndSeverityControls(t *testing.T) {
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		bodies <- body.Text
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"svc"}, SendInterval: 100 * time.Millisecond}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	o.ObserveOTLP(reviewRequestParts(t, "host\r\nmessage: forged-host", "worker\u2028message: forged-scope", "ERROR\nmessage: forged-severity"))
	select {
	case text := <-bodies:
		if strings.Contains(text, "\nmessage:") || strings.ContainsRune(text, '\u2028') || strings.Contains(text, "forged-severity") || !strings.Contains(text, "original severity: 17") || !strings.Contains(text, "host") {
			t.Fatalf("unsafe metadata: %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("notification missing")
	}
}
