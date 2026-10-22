package otlphttp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"gofluentd/library/otlpwire"
)

func TestOTLPDurableRefusalIsRetryableWholeRequest(t *testing.T) {
	h, err := NewReceiver(ReceiverConfig{}, func(context.Context, *otlpwire.Request) error {
		return errors.New("synthetic capacity or sync failure")
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/logs", strings.NewReader(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"synthetic"}}]}]}]}`))
	r.Header.Set("Content-Type", otlpwire.JSON)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || strings.Contains(w.Body.String(), "partial") {
		t.Fatalf("unsafe full-request retry contract: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
