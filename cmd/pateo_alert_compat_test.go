package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
)

func TestPateoAlertPreservesLegacyWireAndLevelGate(t *testing.T) {
	raw, err := os.ReadFile("testdata/pateo_legacy_wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]interface{}
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	packets := make(chan map[string]interface{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var body interface{}
		if err = json.Unmarshal(raw, &body); err != nil {
			t.Error(err)
			return
		}
		packets <- map[string]interface{}{"method": r.Method, "content_type": r.Header.Get("Content-Type"), "authorization": r.Header.Get("Authorization"), "body": body}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p, err := newPateoAlertPusher(ctx, server.URL, "synthetic-pateo-token")
	if err != nil {
		t.Fatal(err)
	}
	entry := zapcore.Entry{Level: zapcore.ErrorLevel, Time: time.Unix(12345, 987654321).UTC(), LoggerName: "fixture-service", Message: "chosen diagnostic", Caller: zapcore.EntryCaller{Defined: true, File: "fixture.go", Line: 7}, Stack: "fixture stack"}
	lower := entry
	lower.Level = zapcore.InfoLevel
	lower.Message = "ignored info control"
	if err = p.GetZapHook()(lower, nil); err != nil {
		t.Fatal(err)
	}
	if err = p.GetZapHook()(entry, []zapcore.Field{zap.String("context", "preserved"), zap.Int64("large", 9007199254740993)}); err != nil {
		t.Fatal(err)
	}
	// The single sender is FIFO: an incorrectly admitted info control would be
	// observed before the error packet, avoiding a timing-only absence assertion.
	select {
	case got := <-packets:
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("legacy alert contract changed: got %#v want %#v", got, expected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("legacy error alert was not delivered")
	}
}

func TestPateoAlertQueueIsBounded(t *testing.T) {
	p := &pateoAlertPusher{queue: make(chan pateoAlertMessage, 20)}
	for n := 0; n < 20; n++ {
		if err := p.Send("title", "content", time.Unix(1, 2).UTC()); err != nil {
			t.Fatal("funded queue admission", n, err)
		}
	}
	if err := p.Send("refused", "overflow", time.Time{}); err == nil {
		t.Fatal("full legacy alert queue accepted new work")
	}
	if len(p.queue) != 20 {
		t.Fatal("overflow changed accepted queue")
	}
	for n := 0; n < 20; n++ {
		message := <-p.queue
		if message.Title != "title" || message.Content != "content" || message.Time != "1970-01-01T00:00:01.000000002Z" {
			t.Fatal("queue changed accepted payload", message)
		}
	}
}

type pateoRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pateoRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type pateoTrackedBody struct {
	io.Reader
	closed chan<- struct{}
}

func (b *pateoTrackedBody) Close() error { b.closed <- struct{}{}; return nil }

func TestPateoAlertClosesResponsesWithoutRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p, err := newPateoAlertPusher(ctx, "http://synthetic.invalid", "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 8)
	names := make(chan string, 8)
	var requests atomic.Int64
	p.client.Transport = pateoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, err
		}
		var message pateoAlertMessage
		if err = json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		requests.Add(1)
		names <- message.Title
		status := http.StatusOK
		if message.Title == "failure" {
			status = http.StatusInternalServerError
		}
		return &http.Response{StatusCode: status, Body: &pateoTrackedBody{Reader: strings.NewReader("{}"), closed: closed}, Header: make(http.Header)}, nil
	})
	for _, title := range []string{"success", "failure", "barrier"} {
		if err = p.Send(title, "body", time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	// The FIFO barrier proves the failed response was processed without an
	// intervening retry; it is not an early snapshot of a request counter.
	for _, expected := range []string{"success", "failure", "barrier"} {
		select {
		case actual := <-names:
			if actual != expected {
				t.Fatalf("unexpected alert request %q, wanted %q", actual, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("alert barrier was not reached")
		}
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("did not close success and failure responses")
		}
	}
	if requests.Load() != 3 {
		t.Fatal("legacy alert response was retried", requests.Load())
	}
}
