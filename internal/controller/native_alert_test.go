//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gofluentd/internal/alerts"
	"gofluentd/library/otlpwire"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
)

func TestNativeStorageFidelityWithFailedAlert(t *testing.T) {
	stored := make(chan []byte, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		stored <- b
		w.Header().Set("Content-Type", otlpwire.Protobuf)
	}))
	defer peer.Close()
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
	defer telegram.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := alerts.New(ctx, alerts.Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"synthetic-service"}}, telegram.URL, telegram.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	source := plogotlp.NewExportRequest()
	resource := source.Logs().ResourceLogs().AppendEmpty()
	resource.Resource().Attributes().PutStr("service.name", "synthetic-service")
	record := resource.ScopeLogs().AppendEmpty().LogRecords().AppendEmpty()
	record.SetSeverityNumber(17)
	record.Body().SetEmptyBytes().FromRaw([]byte{255, 0, 128})
	record.Attributes().PutEmpty("null")
	record.Attributes().PutEmptyMap("empty")
	payload, err := source.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	config := nativeStorageTestConfig(t, peer.URL, 16<<10)
	config.ObserveAccepted = o.ObserveOTLP
	svc := nativeStorageStart(t, config)
	if status := nativeStoragePost(t, svc, payload); status != http.StatusOK {
		t.Fatalf("admission %d", status)
	}
	nativeStorageAwait(t, func() bool { return o.Stats().Failed == 1 })
	output := plogotlp.NewExportRequest()
	if err := output.UnmarshalProto(<-stored); err != nil {
		t.Fatal(err)
	}
	archive, ok := output.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes().Get("otel.record.otlp")
	if !ok {
		t.Fatal("missing archive")
	}
	raw, err := base64.StdEncoding.DecodeString(archive.Str())
	if err != nil {
		t.Fatal(err)
	}
	restored := plogotlp.NewExportRequest()
	if err = restored.UnmarshalProto(raw); err != nil {
		t.Fatal(err)
	}
	want, _ := source.MarshalJSON()
	got, _ := restored.MarshalJSON()
	if !bytes.Equal(want, got) {
		t.Fatal("native record fidelity changed")
	}
}
