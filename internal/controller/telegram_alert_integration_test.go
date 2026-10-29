package controller_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/alerts"
	"gofluentd/library/otlpwire"
)

func TestAlertFailureDoesNotBlockOTLPStorageOrRepeatOnReplay(t *testing.T) {
	var attempts, alertsReceived atomic.Int64
	stored := make(chan []byte, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		select {
		case stored <- b:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer peer.Close()
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { alertsReceived.Add(1); w.WriteHeader(400) }))
	defer telegram.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := alerts.New(ctx, alerts.Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"svc"}}, telegram.URL, telegram.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	cfg := serviceConfig(t, peer.URL)
	cfg.ObserveAccepted = o.ObserveOTLP
	svc := serviceStart(t, cfg)
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeLogs":[{"logRecords":[{"severityNumber":17,"body":{"stringValue":"synthetic"}}]}]}]}`)
	status, _ := servicePost(t, &http.Client{Timeout: 3 * time.Second}, "http://"+svc.Addr().String()+"/v1/logs", otlpwire.JSON, "", "private-test-ingress", body)
	if status != 200 {
		t.Fatalf("admission status %d", status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (attempts.Load() < 2 || o.Stats().Failed < 1) {
		time.Sleep(20 * time.Millisecond)
	}
	if attempts.Load() < 2 || o.Stats().Failed != 1 || alertsReceived.Load() != 1 || o.Stats().Suppressed != 0 {
		t.Fatalf("storage attempts=%d alert calls=%d stats=%+v", attempts.Load(), alertsReceived.Load(), o.Stats())
	}
	select {
	case b := <-stored:
		if !bytes.Equal(b, body) {
			t.Fatal("stored payload changed")
		}
	case <-time.After(time.Second):
		t.Fatal("storage did not receive original payload")
	}
}
