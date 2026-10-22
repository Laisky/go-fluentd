package otlpwire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go.opentelemetry.io/collector/pdata/plog"
	"strconv"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"google.golang.org/protobuf/encoding/protowire"
)

// NativeLogStorage projects records for a flat native OTLP log store. Every
// record includes an opaque, typed OTLP request containing its original resource,
// scope and record. Protobuf fields are copied without decoding/re-marshaling;
// unknown fields and original value types survive. JSON values retain their raw
// representation. This is an explicit destination codec, never a content policy.
// Empty resource/scope containers do not denote log records and have no row.
// The original Request is immutable. Expansion fails as a whole before transport.
func (r *Request) NativeLogStorage(maxBytes int64) ([]byte, error) {
	if r.signal != Logs || r.items == 0 || maxBytes <= 0 {
		return nil, errors.New("invalid native log storage request")
	}
	pieces, err := nativeRecordPieces(r.payload, r.contentType, 0, maxBytes)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return nil, err
		}
		return nil, errors.New("cannot encode native log storage records")
	}
	if len(pieces) != r.items {
		return nil, errors.New("cannot encode native log storage records")
	}
	// Bound expansion before allocating per-record archives or decoded output.
	var archiveBytes int64
	for _, piece := range pieces {
		cost := int64(base64.StdEncoding.EncodedLen(len(piece)))
		if cost > maxBytes-archiveBytes {
			return nil, ErrTooLarge
		}
		archiveBytes += cost
	}
	source := plogotlp.NewExportRequest()
	if err := decode(source, r.contentType, r.payload); err != nil {
		return nil, errors.New("cannot decode native log storage records")
	}
	output := plogotlp.NewExportRequest()
	position := 0
	projectionBytes := archiveBytes
	resources := source.Logs().ResourceLogs()
	for i := 0; i < resources.Len(); i++ {
		rl := resources.At(i)
		scopes := rl.ScopeLogs()
		for j := 0; j < scopes.Len(); j++ {
			records := scopes.At(j).LogRecords()
			for k := 0; k < records.Len(); k++ {
				original := records.At(k)
				// Keep a queryable standard OTLP JSON view as well as the authoritative raw archive.
				view := plogotlp.NewExportRequest()
				viewResource := view.Logs().ResourceLogs().AppendEmpty()
				rl.Resource().CopyTo(viewResource.Resource())
				viewResource.SetSchemaUrl(rl.SchemaUrl())
				viewScope := viewResource.ScopeLogs().AppendEmpty()
				scopes.At(j).Scope().CopyTo(viewScope.Scope())
				viewScope.SetSchemaUrl(scopes.At(j).SchemaUrl())
				original.CopyTo(viewScope.LogRecords().AppendEmpty())
				viewJSON, jsonError := view.MarshalJSON()
				if jsonError != nil {
					return nil, errors.New("cannot encode native log query view")
				}
				if int64(len(viewJSON)) > maxBytes-projectionBytes {
					return nil, ErrTooLarge
				}
				projectionBytes += int64(len(viewJSON))
				projectedResource := output.Logs().ResourceLogs().AppendEmpty()
				for _, key := range []string{"service.name", "host.name", "deployment.environment.name"} {
					if value, ok := rl.Resource().Attributes().Get(key); ok {
						projectedResource.Resource().Attributes().PutStr(key, value.AsString())
					}
				}
				projectedScope := projectedResource.ScopeLogs().AppendEmpty()
				projectedScope.Scope().SetName("gofluentd/native-log-storage")
				projected := projectedScope.LogRecords().AppendEmpty()
				projected.SetTimestamp(original.Timestamp())
				projected.SetObservedTimestamp(original.ObservedTimestamp())
				projected.SetSeverityNumber(original.SeverityNumber())
				projected.SetSeverityText(original.SeverityText())
				projected.SetTraceID(original.TraceID())
				projected.SetSpanID(original.SpanID())
				projected.SetFlags(original.Flags())
				projected.SetEventName(original.EventName())
				projected.SetDroppedAttributesCount(original.DroppedAttributesCount())
				if original.Body().Type() == pcommon.ValueTypeStr {
					projected.Body().SetStr(original.Body().Str())
				}
				projected.Attributes().PutStr("otel.storage.format", "otlp-record-base64-v1")
				projected.Attributes().PutStr("otel.record.content_type", r.contentType)
				projected.Attributes().PutStr("otel.record.json", string(viewJSON))
				projected.Attributes().PutStr("otel.record.otlp", base64.StdEncoding.EncodeToString(pieces[position]))
				position++
				if original.Body().Type() != pcommon.ValueTypeStr {
					// A string display projection cannot collide with native stream fields.
					// Canonical OTLP JSON keeps type tags and nonfinite-number spelling.
					var document struct {
						Resources []struct {
							Scopes []struct {
								Records []struct {
									Body json.RawMessage `json:"body"`
								} `json:"logRecords"`
							} `json:"scopeLogs"`
						} `json:"resourceLogs"`
					}
					if err := json.Unmarshal(viewJSON, &document); err != nil {
						return nil, errors.New("cannot encode native log body display")
					}
					body := document.Resources[0].Scopes[0].Records[0].Body
					if len(body) == 0 {
						body = json.RawMessage(`{}`)
					}
					projected.Body().SetStr(string(body))
				}
				if nativeStorageRowSize(projectedResource.Resource(), projected) > 2<<20 {
					return nil, ErrTooLarge
				}
			}
		}
	}
	encoded, err := output.MarshalProto()
	if err != nil {
		return nil, errors.New("cannot encode native log storage output")
	}
	if int64(len(encoded)) > maxBytes {
		return nil, ErrTooLarge
	}
	return encoded, nil
}

// nativeRecordPieces retains each log record's enclosing protocol context.
func nativeRecordPieces(data []byte, contentType string, depth int, maxBytes int64) ([][]byte, error) {
	var prefix []byte
	var children [][]byte
	var err error
	if contentType == Protobuf {
		number := protowire.Number(2)
		if depth == 0 {
			number = 1
		}
		prefix, children, err = nativeProtoChildren(data, number)
	} else {
		key := []string{"resourceLogs", "scopeLogs", "logRecords"}[depth]
		prefix, children, err = nativeJSONChildren(data, key)
	}
	if err != nil {
		return nil, err
	}
	var result [][]byte
	var total int64
	for _, child := range children {
		pieces := [][]byte{child}
		if depth < 2 {
			pieces, err = nativeRecordPieces(child, contentType, depth+1, maxBytes)
			if err != nil {
				return nil, err
			}
		}
		for _, piece := range pieces {
			var wrapped []byte
			// Tags/length framing and JSON keys need at most 32 extra bytes.
			cost := int64(len(prefix)) + int64(len(piece)) + 32
			if cost > maxBytes-total {
				return nil, ErrTooLarge
			}
			total += cost
			if contentType == Protobuf {
				number := protowire.Number(2)
				if depth == 0 {
					number = 1
				}
				wrapped = protowire.AppendTag(bytes.Clone(prefix), number, protowire.BytesType)
				wrapped = protowire.AppendBytes(wrapped, piece)
			} else {
				key, _ := json.Marshal([]string{"resourceLogs", "scopeLogs", "logRecords"}[depth])
				wrapped = append(bytes.Clone(prefix), key...)
				wrapped = append(wrapped, ':', '[')
				wrapped = append(wrapped, piece...)
				wrapped = append(wrapped, ']', '}')
			}
			result = append(result, wrapped)
		}
	}
	return result, nil
}

// nativeProtoChildren copies non-child fields verbatim, including unknown fields.
func nativeProtoChildren(data []byte, childNumber protowire.Number) ([]byte, [][]byte, error) {
	var prefix []byte
	var children [][]byte
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return nil, nil, errors.New("malformed native storage wire")
		}
		size := protowire.ConsumeFieldValue(number, typ, data[tagSize:])
		if size < 0 {
			return nil, nil, errors.New("malformed native storage wire")
		}
		field := data[:tagSize+size]
		if number == childNumber {
			if typ != protowire.BytesType {
				return nil, nil, errors.New("malformed native storage child")
			}
			child, n := protowire.ConsumeBytes(data[tagSize:])
			if n < 0 {
				return nil, nil, errors.New("malformed native storage child")
			}
			children = append(children, child)
		} else {
			prefix = append(prefix, field...)
		}
		data = data[len(field):]
	}
	return prefix, children, nil
}

// nativeJSONChildren preserves raw field values and rejects ambiguous envelope keys.
func nativeJSONChildren(data []byte, childKey string) ([]byte, [][]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, nil, errors.New("malformed native storage JSON")
	}
	prefix := []byte{'{'}
	seen := make(map[string]bool)
	var children [][]byte
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, nil, errors.New("ambiguous native storage JSON key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		alias := map[string]string{"resourceLogs": "resource_logs", "scopeLogs": "scope_logs", "logRecords": "log_records"}[childKey]
		if key == childKey || key == alias {
			if (key == childKey && seen[alias]) || (key == alias && seen[childKey]) {
				return nil, nil, errors.New("ambiguous native storage JSON child")
			}
			var entries []json.RawMessage
			if err := json.Unmarshal(value, &entries); err != nil {
				return nil, nil, err
			}
			for _, entry := range entries {
				children = append(children, entry)
			}
		} else {
			encodedKey, _ := json.Marshal(key)
			prefix = append(prefix, encodedKey...)
			prefix = append(prefix, ':')
			prefix = append(prefix, value...)
			prefix = append(prefix, ',')
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, nil, errors.New("malformed native storage JSON")
	}
	return prefix, children, nil
}

// nativeStorageRowSize bounds the pinned VictoriaLogs EstimatedJSONRowLen contract.
// VictoriaLogs ignores rows above its hard 2 MiB block limit even with HTTP 200.
// A 1 KiB reserve covers native generated/default fields under pinned backend defaults.
// A bounded severity fallback upper bound avoids depending on enum display spelling.
func nativeStorageRowSize(resource pcommon.Resource, record plog.LogRecord) int {
	size := 1024 + len("{}\n") + len(`"_time":""`) + len(time.RFC3339Nano)
	add := func(name, value string) {
		if value != "" {
			size += len(`,"":""`) + len(name) + len(value)
		}
	}
	resource.Attributes().Range(func(key string, value pcommon.Value) bool { add(key, value.Str()); return true })
	add("scope.name", "gofluentd/native-log-storage")
	add("scope.version", "unknown")
	add("_msg", record.Body().Str())
	record.Attributes().Range(func(key string, value pcommon.Value) bool { add(key, value.Str()); return true })
	add("severity_number", strconv.FormatInt(int64(record.SeverityNumber()), 10))
	if record.SeverityText() != "" {
		add("severity_text", record.SeverityText())
	} else {
		add("severity_text", "                                ")
	}
	add("event_name", record.EventName())
	if !record.TraceID().IsEmpty() {
		add("trace_id", "00000000000000000000000000000000")
	}
	if !record.SpanID().IsEmpty() {
		add("span_id", "0000000000000000")
	}
	return size
}
