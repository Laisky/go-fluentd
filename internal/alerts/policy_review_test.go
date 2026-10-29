package alerts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type reviewNotifier struct{ calls chan string }

func (n reviewNotifier) Send(ctx context.Context, text string) DeliveryResult {
	n.calls <- text
	return DeliveryResult{Delivered: true}
}
func TestReviewNotifierBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", SendInterval: 100 * time.Millisecond}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	calls := make(chan string, 1)
	o.notifier = reviewNotifier{calls: calls}
	o.Observe(Event{Source: "app", Severity: "error"})
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("notifier not called")
	}
	deadline := time.Now().Add(time.Second)
	for o.Stats().Sent == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.Stats().Sent != 1 {
		t.Fatal("notifier outcome not counted")
	}
}

func TestReviewAggregationAndEviction(t *testing.T) {
	o := inert()
	o.cfg.DedupEntries = 2
	o.Observe(Event{Source: "one", Severity: "error", Message: "first"})
	o.Observe(Event{Source: "one", Severity: "error", Message: "second"})
	if o.Stats().Queued != 1 || o.Stats().Suppressed != 1 {
		t.Fatalf("hidden messages should group: %+v", o.Stats())
	}
	<-o.queue
	o.Observe(Event{Source: "two", Severity: "error"})
	<-o.queue
	o.Observe(Event{Source: "three", Severity: "error"})
	if o.Stats().Queued != 3 || o.Stats().Evicted != 1 {
		t.Fatalf("bounded table blocked a new source: %+v", o.Stats())
	}
}
func TestReviewDropUnits(t *testing.T) {
	o := inert()
	for i := 0; i < 65; i++ {
		o.Observe(Event{Source: fmt.Sprint(i), Severity: "error"})
	}
	req := reviewRequest(t, "worker")
	for i := 0; i < 5; i++ {
		o.ObserveOTLP(req)
	}
	o.parseOTLP(otlpPayload{contentType: "application/json", data: []byte("invalid")})
	s := o.Stats()
	if s.DroppedEvents != 1 || s.DroppedRequests != 2 || s.RequestQueueFull != 1 || s.RequestDecodeError != 1 {
		t.Fatal(s)
	}
}
func TestReviewShutdownCountsOutstandingEvents(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", SendInterval: 100 * time.Millisecond}, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	o.Observe(Event{Source: "first", Severity: "error"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancel()
		o.Wait()
		t.Fatal("transport did not start")
	}
	o.Observe(Event{Source: "second", Severity: "error"})
	o.Observe(Event{Source: "third", Severity: "error"})
	cancel()
	o.Wait()
	if o.Stats().ShutdownEvents != 3 {
		t.Fatalf("shutdown losses: %+v", o.Stats())
	}
}
