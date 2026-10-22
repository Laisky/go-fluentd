//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// nativeStorageTestConfig provisions only disposable loopback and journal resources.
func nativeStorageTestConfig(t *testing.T, endpoint string, limit int64) OTLPServiceConfig {
	t.Helper()
	t.Setenv("TEST_OTLP_NATIVE_STORAGE_TOKEN", "synthetic-native-ingress")
	return OTLPServiceConfig{Enabled: true, ListenAddress: "127.0.0.1:0", StorageDirectory: t.TempDir(),
		BearerTokenEnv: "TEST_OTLP_NATIVE_STORAGE_TOKEN", StorageScanTimeout: time.Second,
		ReplayInterval: 10 * time.Millisecond, MaxWireBytes: limit, MaxDecodedBytes: limit,
		Destinations: []OTLPServiceDestination{{ID: "archive", LogsEndpoint: endpoint + "/v1/logs",
			MetricsEndpoint: endpoint + "/v1/metrics", TracesEndpoint: endpoint + "/v1/traces",
			LogStorageCodec: "otlp-record-base64-v1", MaxAttempts: 1}}}
}

// nativeStorageStart registers cleanup without bypassing the actual service listener or WAL.
func nativeStorageStart(t *testing.T, config OTLPServiceConfig) *OTLPService {
	t.Helper()
	service, err := StartOTLPService(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})
	return service
}

// nativeStoragePost submits a real authenticated ingress request and consumes its response.
func nativeStoragePost(t *testing.T, service *OTLPService, payload []byte) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+service.Addr().String()+"/v1/logs", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", otlpwire.Protobuf)
	request.Header.Set("Authorization", "Bearer synthetic-native-ingress")
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, response.Body)
	if err := errors.Join(readErr, response.Body.Close()); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode
}

// nativeStoragePosition reads admission identity and reservation under the WAL ownership gate.
func nativeStoragePosition(t *testing.T, owner *OTLPJournal) [3]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := owner.acquire(ctx, owner.walGate); err != nil {
		t.Fatal(err)
	}
	defer func() { <-owner.walGate }()
	return [3]int64{owner.frontier, owner.reservedRecords.Load(), owner.generation.ReleasedThrough}
}

// nativeStorageAwait waits for a bounded asynchronous durability observation.
func nativeStorageAwait(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("native storage durability observation timed out")
}

// TestOTLPServiceNativeStorageRejectsExpansionBeforeWAL proves no success or durable obligation on codec overflow.
func TestOTLPServiceNativeStorageRejectsExpansionBeforeWAL(t *testing.T) {
	for _, test := range []struct {
		name                    string
		wireBytes, decodedBytes int64
		bodyBytes               int
	}{
		{name: "decoded-expansion", wireBytes: 2048, decodedBytes: 2048, bodyBytes: 1500},
		{name: "wire-expansion", wireBytes: 512, decodedBytes: 4096, bodyBytes: 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", otlpwire.Protobuf)
			}))
			defer peer.Close()
			source := plogotlp.NewExportRequest()
			source.Logs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr(strings.Repeat("x", test.bodyBytes))
			payload, err := source.MarshalProto()
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(payload)) >= test.wireBytes {
				t.Fatal("fixture does not fit the ingress budget")
			}
			parsed, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.Protobuf, "", bytes.NewReader(payload), otlpwire.Limits{WireBytes: test.wireBytes, DecodedBytes: test.decodedBytes, Items: 10})
			if err != nil {
				t.Fatal(err)
			}
			projected, codecErr := parsed.NativeLogStorage(test.decodedBytes)
			if test.wireBytes == test.decodedBytes {
				if !errors.Is(codecErr, otlpwire.ErrTooLarge) {
					t.Fatalf("fixture must exceed decoded codec budget: %v", codecErr)
				}
			} else if codecErr != nil || int64(len(projected)) <= test.wireBytes {
				t.Fatalf("fixture must fit decoded budget but exceed only destination wire budget: length=%d err=%v", len(projected), codecErr)
			}
			config := nativeStorageTestConfig(t, peer.URL, test.decodedBytes)
			config.MaxWireBytes = test.wireBytes
			service := nativeStorageStart(t, config)
			before := nativeStoragePosition(t, service.owner)
			if status := nativeStoragePost(t, service, payload); status != http.StatusRequestEntityTooLarge {
				t.Fatalf("expanded request returned %d, want 413 before admission", status)
			}
			if got := nativeStoragePosition(t, service.owner); got != before {
				t.Fatalf("codec rejection created a WAL identity/reservation: before=%v after=%v", before, got)
			}
			if service.codecAdmissionRejected.Load() != 1 || service.Counters() != (OTLPCounters{}) || calls.Load() != 0 {
				t.Fatalf("codec refusal acknowledged, delivered or miscounted: codec=%d counters=%+v peer=%d", service.codecAdmissionRejected.Load(), service.Counters(), calls.Load())
			}
			ownerConfig := service.owner.cfg
			if err := service.Stop(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenOTLPJournal(context.Background(), ownerConfig, []OTLPDestination{{ID: "archive", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
				calls.Add(1)
				return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			}()
			report, err := reopened.ReplayBatch(context.Background())
			if err != nil || report.Seen != 0 || report.Pending != 0 || nativeStoragePosition(t, reopened) != before || calls.Load() != 0 {
				t.Fatalf("rejected request survived restart as durable pending work: report=%+v err=%v peer=%d", report, err, calls.Load())
			}

		})
	}
}

// nativeStorageTypedPayload contains type distinctions and metadata that flat stores previously lost.
func nativeStorageTypedPayload(t *testing.T) []byte {
	t.Helper()
	source := plogotlp.NewExportRequest()
	resource := source.Logs().ResourceLogs().AppendEmpty()
	resource.SetSchemaUrl("https://example.invalid/resource-schema")
	resource.Resource().Attributes().PutStr("service.name", "synthetic-service")
	scope := resource.ScopeLogs().AppendEmpty()
	scope.SetSchemaUrl("https://example.invalid/scope-schema")
	scope.Scope().SetName("synthetic-scope")
	scope.Scope().SetVersion("v1")
	record := scope.LogRecords().AppendEmpty()
	record.SetTimestamp(pcommon.Timestamp(123456789))
	record.SetEventName("synthetic-native-event")
	record.Body().SetEmptyBytes().FromRaw([]byte{255, 0, 128})
	attributes := record.Attributes()
	attributes.PutDouble("nonfinite", math.Inf(1))
	attributes.PutDouble("negative_zero", math.Copysign(0, -1))
	attributes.PutEmpty("null")
	attributes.PutEmptyMap("empty_object")
	attributes.PutEmptySlice("empty_array")
	attributes.PutStr("service.name", "application-service")
	attributes.PutStr("_msg", "application-message-field")
	payload, err := source.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	return append(payload, 0xf8, 0x7f, 1) // An unknown root field must remain in the opaque archive.
}

// TestOTLPServiceNativeStorageArchiveSurvivesRetryAndRestart checks real HTTP/WAL replay and typed query view.
func TestOTLPServiceNativeStorageArchiveSurvivesRetryAndRestart(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var mu sync.Mutex
	var received []byte
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, err := io.ReadAll(r.Body)
		if int64(len(wire)) > 1024 {
			t.Error("compressed destination wire budget exceeded")
		}
		var body []byte
		if err == nil {
			if r.Header.Get("Content-Encoding") != "gzip" {
				t.Error("configured destination compression missing")
			}
			reader, gzipErr := gzip.NewReader(bytes.NewReader(wire))
			if gzipErr != nil {
				err = gzipErr
			} else {
				body, err = io.ReadAll(reader)
				err = errors.Join(err, reader.Close())
			}
		}
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Content-Type") != otlpwire.Protobuf {
			t.Error("codec output is not protobuf")
		}
		mu.Lock()
		received = bytes.Clone(body)
		mu.Unlock()
		w.Header().Set("Content-Type", otlpwire.Protobuf)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer peer.Close()
	config := nativeStorageTestConfig(t, peer.URL, 16<<10)
	config.MaxWireBytes = 1024
	config.Destinations[0].Gzip = true
	payload := nativeStorageTypedPayload(t)
	parsed, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.Protobuf, "", bytes.NewReader(payload), otlpwire.Limits{WireBytes: config.MaxWireBytes, DecodedBytes: config.MaxDecodedBytes, Items: 10})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := parsed.NativeLogStorage(config.MaxDecodedBytes)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	compressor := gzip.NewWriter(&compressed)
	if _, err := compressor.Write(projected); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	if int64(len(projected)) <= config.MaxWireBytes || int64(compressed.Len()) > config.MaxWireBytes {
		t.Fatalf("fixture must need compression and fit its actual wire budget: raw=%d gzip=%d limit=%d", len(projected), compressed.Len(), config.MaxWireBytes)
	}
	first := nativeStorageStart(t, config)
	if status := nativeStoragePost(t, first, payload); status != http.StatusOK {
		t.Fatalf("valid typed request was not durably admitted: %d", status)
	}
	nativeStorageAwait(t, func() bool { return first.Counters().Retryable > 0 })
	if first.Counters().Accepted != 0 || first.Counters().Quarantined != 0 {
		t.Fatalf("503 was mistaken for acceptance: %+v", first.Counters())
	}
	if err := first.Stop(); err != nil {
		t.Fatal(err)
	}
	fail.Store(false)
	second := nativeStorageStart(t, config)
	nativeStorageAwait(t, func() bool { return second.Counters().Accepted == 1 })
	if second.Counters().Quarantined != 0 || second.codecAdmissionRejected.Load() != 0 {
		t.Fatalf("recovery lost valid typed request: %+v", second.Counters())
	}
	mu.Lock()
	exported := bytes.Clone(received)
	mu.Unlock()
	output := plogotlp.NewExportRequest()
	if err := output.UnmarshalProto(exported); err != nil {
		t.Fatal(err)
	}
	if output.Logs().LogRecordCount() != 1 {
		t.Fatal("recovery changed record count")
	}
	attributes := output.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0).Attributes()
	archive, ok := attributes.Get("otel.record.otlp")
	if !ok {
		t.Fatal("missing authoritative record archive")
	}
	raw, err := base64.StdEncoding.DecodeString(archive.Str())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte{0xf8, 0x7f, 1}) {
		t.Fatal("unknown root wire field was lost")
	}
	original, restored := plogotlp.NewExportRequest(), plogotlp.NewExportRequest()
	if err := original.UnmarshalProto(payload); err != nil {
		t.Fatal(err)
	}
	if err := restored.UnmarshalProto(raw); err != nil {
		t.Fatal(err)
	}
	expectedJSON, err := original.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	restoredJSON, err := restored.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedJSON, restoredJSON) {
		t.Fatal("recovered archive changed known values or metadata")
	}
	queryView, ok := attributes.Get("otel.record.json")
	if !ok {
		t.Fatal("missing queryable typed JSON view")
	}
	var expected, query any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(queryView.Str()), &query); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, query) || !strings.Contains(queryView.Str(), `"bytesValue"`) || !strings.Contains(queryView.Str(), `"doubleValue"`) {
		t.Fatal("query view lost canonical bytes/nonfinite type tags or original metadata")
	}
}
