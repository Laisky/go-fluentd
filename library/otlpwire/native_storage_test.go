package otlpwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/protobuf/encoding/protowire"
)

func nativeFixture(t *testing.T, contentType string) []byte {
	t.Helper()
	request := plogotlp.NewExportRequest()
	rl := request.Logs().ResourceLogs().AppendEmpty()
	rl.SetSchemaUrl("https://example.invalid/resource-schema")
	rl.Resource().SetDroppedAttributesCount(7)
	rl.Resource().Attributes().PutStr("service.name", "resource-service")
	rl.Resource().Attributes().PutEmptyMap("custom-resource").PutEmpty("null")
	scope := rl.ScopeLogs().AppendEmpty()
	scope.SetSchemaUrl("https://example.invalid/scope-schema")
	scope.Scope().SetName("original-scope")
	scope.Scope().SetVersion("chosen-version")
	scope.Scope().Attributes().PutEmptyMap("empty")
	for i := 0; i < 2; i++ {
		record := scope.LogRecords().AppendEmpty()
		record.SetTimestamp(pcommon.Timestamp(123456789 + i))
		record.SetEventName("chosen-event")
		record.SetDroppedAttributesCount(3)
		record.SetFlags(1)
		record.SetTraceID(pcommon.TraceID{1, 2, 3})
		if i == 0 {
			record.Body().SetStr("chosen api_key=synthetic-canary")
		} else {
			record.Body().SetEmptyMap().PutStr("service.name", "body-service")
		}
		attrs := record.Attributes()
		attrs.PutInt("sequence", int64(i))
		attrs.PutStr("service.name", "log-service")
		attrs.PutStr("_msg", "chosen-field")
		attrs.PutEmpty("null")
		attrs.PutEmptyMap("empty-map")
		attrs.PutEmptySlice("empty-slice")
		attrs.PutEmptyBytes("bytes").FromRaw([]byte{0, 255})
		attrs.PutDouble("nan", math.NaN())
		attrs.PutDouble("infinity", math.Inf(1))
		attrs.PutDouble("one-double", 1.0)
		attrs.PutInt("one-int", 1)
		attrs.PutDouble("negative-zero", math.Copysign(0, -1))
		attrs.PutStr("a.b", "literal")
		attrs.PutEmptyMap("a").PutStr("b", "nested")
	}
	var data []byte
	var err error
	if contentType == Protobuf {
		data, err = request.MarshalProto()
	} else {
		data, err = request.MarshalJSON()
	}
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestNativeLogStorageTypedRoundTrip(t *testing.T) {
	for _, contentType := range []string{Protobuf, JSON} {
		t.Run(contentType, func(t *testing.T) {
			data := nativeFixture(t, contentType)
			if contentType == Protobuf {
				data = protowire.AppendTag(data, 99, protowire.BytesType)
				data = protowire.AppendBytes(data, []byte("synthetic-unknown-root"))
			} else {
				data = append([]byte(`{"unknown_root":{"keep":null},`), bytes.TrimPrefix(data, []byte("{"))...)
			}
			request, err := ReadRequest(Logs, contentType, "", bytes.NewReader(data), DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			before := bytes.Clone(request.Payload())
			output, err := request.NativeLogStorage(4 << 20)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, request.Payload()) {
				t.Fatal("mutated immutable input")
			}
			projected := plogotlp.NewExportRequest()
			if err := projected.UnmarshalProto(output); err != nil {
				t.Fatal(err)
			}
			if projected.Logs().LogRecordCount() != 2 {
				t.Fatal("changed record count")
			}
			for i := 0; i < 2; i++ {
				resource := projected.Logs().ResourceLogs().At(i)
				record := resource.ScopeLogs().At(0).LogRecords().At(0)
				archive, ok := record.Attributes().Get("otel.record.otlp")
				if !ok {
					t.Fatal("missing typed archive")
				}
				originalBytes, err := base64.StdEncoding.DecodeString(archive.Str())
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(originalBytes, []byte("synthetic-unknown-root")) && contentType == Protobuf {
					t.Fatal("unknown protocol field omitted")
				}
				if !bytes.Contains(originalBytes, []byte(`"unknown_root":{"keep":null}`)) && contentType == JSON {
					t.Fatal("unknown JSON field omitted")
				}
				original := plogotlp.NewExportRequest()
				if err := decode(original, contentType, originalBytes); err != nil {
					t.Fatal(err)
				}
				if original.Logs().LogRecordCount() != 1 {
					t.Fatal("archive does not contain exactly one original record")
				}
				rl := original.Logs().ResourceLogs().At(0)
				if rl.SchemaUrl() != "https://example.invalid/resource-schema" || rl.ScopeLogs().At(0).SchemaUrl() != "https://example.invalid/scope-schema" {
					t.Fatal("lost schema URLs")
				}
				if rl.Resource().DroppedAttributesCount() != 7 || rl.ScopeLogs().At(0).Scope().Name() != "original-scope" {
					t.Fatal("lost context")
				}
				attrs := rl.ScopeLogs().At(0).LogRecords().At(0).Attributes()
				get := func(key string) pcommon.Value {
					value, ok := attrs.Get(key)
					if !ok {
						t.Fatalf("lost field %q", key)
					}
					return value
				}
				if get("sequence").Int() != int64(i) || get("bytes").Type() != pcommon.ValueTypeBytes || !bytes.Equal(get("bytes").Bytes().AsRaw(), []byte{0, 255}) {
					t.Fatal("lost native values")
				}
				if !math.IsNaN(get("nan").Double()) || !math.IsInf(get("infinity").Double(), 1) || !math.Signbit(get("negative-zero").Double()) {
					t.Fatal("lost IEEE values")
				}
				if get("one-double").Type() != pcommon.ValueTypeDouble || get("one-int").Type() != pcommon.ValueTypeInt {
					t.Fatal("lost numeric type identity")
				}
				if get("null").Type() != pcommon.ValueTypeEmpty || get("empty-map").Type() != pcommon.ValueTypeMap || get("empty-slice").Type() != pcommon.ValueTypeSlice {
					t.Fatal("lost empty types")
				}
				if get("a.b").Str() != "literal" {
					t.Fatal("dotted key changed")
				}
				if record.Body().Type() != pcommon.ValueTypeStr || (i == 0 && record.Body().Str() != "chosen api_key=synthetic-canary") {
					t.Fatal("unsafe display or changed string body")
				}
				if i == 1 && !json.Valid([]byte(record.Body().Str())) {
					t.Fatal("typed nonstring display is not valid JSON")
				}
			}
		})
	}
}

func TestNativeLogStorageCapacityAndUnsupported(t *testing.T) {
	for _, contentType := range []string{Protobuf, JSON} {
		request, err := ReadRequest(Logs, contentType, "", bytes.NewReader(nativeFixture(t, contentType)), DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := request.NativeLogStorage(64); err == nil {
			t.Fatal("expansion silently exceeded output budget")
		}
		if _, err := request.NativeLogStorage(0); err == nil {
			t.Fatal("accepted invalid budget")
		}
	}
}

// TestNativeLogStorageBackendRowLimit prevents a sink HTTP200 from hiding oversized row loss.
func TestNativeLogStorageBackendRowLimit(t *testing.T) {
	request := plogotlp.NewExportRequest()
	request.Logs().ResourceLogs().AppendEmpty().ScopeLogs().AppendEmpty().LogRecords().AppendEmpty().Body().SetStr(strings.Repeat("x", 700000))
	payload, err := request.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadRequest(Logs, Protobuf, "", bytes.NewReader(payload), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.NativeLogStorage(4 << 20); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("backend's hard row limit not enforced before delivery: %v", err)
	}
}
