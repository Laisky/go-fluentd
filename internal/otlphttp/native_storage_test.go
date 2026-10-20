package otlphttp

import (
	"bytes"
	"gofluentd/library/otlpwire"
	"testing"
)

// TestNativeStorageEmptyRequestsRemainTransparent preserves valid empty-batch compatibility.
func TestNativeStorageEmptyRequestsRemainTransparent(t *testing.T) {
	for _, contentType := range []string{otlpwire.JSON, otlpwire.Protobuf} {
		body := []byte("{}")
		if contentType == otlpwire.Protobuf {
			body = nil
		}
		parsed, err := otlpwire.ReadRequest(otlpwire.Logs, contentType, "", bytes.NewReader(body), otlpwire.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		exporter, err := NewExporter(ExporterConfig{Endpoints: map[otlpwire.Signal]string{otlpwire.Logs: "http://example.invalid/v1/logs"}, LogStorageCodec: "otlp-record-base64-v1"})
		if err != nil {
			t.Fatal(err)
		}
		defer exporter.Close()
		if err := exporter.ValidateStorageRequest(parsed); err != nil {
			t.Fatal(err)
		}
		output, actualType, err := exporter.encodeRequest(parsed)
		if err != nil || actualType != contentType || !bytes.Equal(output, body) {
			t.Fatal("codec changed empty OTLP request", err)
		}
	}
}
