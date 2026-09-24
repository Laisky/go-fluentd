package streamformat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	NDJSON      = "ndjson"
	CloudEvents = "cloudevents"
	Structured  = "structured"
	Binary      = "binary"
	Batch       = "batch"
)

func ValidFormat(format string) bool { return format == NDJSON || format == CloudEvents }

// MediaType validates a MIME type and UTF-8 charset before format selection.
func MediaType(s string) (string, error) {
	m, p, e := mime.ParseMediaType(s)
	if e != nil {
		return "", e
	}
	if c := p["charset"]; c != "" && !strings.EqualFold(c, "utf-8") {
		return "", fmt.Errorf("only UTF-8 charset is supported")
	}
	return m, nil
}
func isJSON(m string) bool { return strings.HasSuffix(m, "/json") || strings.HasSuffix(m, "+json") }

// Decode validates an entire bounded request before the caller can publish any
// record. Empty batches are allowed. NDJSON ignores empty lines, requires a final
// newline, accepts CRLF, and restricts each record to a non-null object.
func Decode(format, contentType string, headers http.Header, body []byte, maxRecords int) ([]map[string]interface{}, error) {
	if maxRecords <= 0 {
		return nil, fmt.Errorf("max records must be positive")
	}
	media, err := MediaType(contentType)
	// Binary CloudEvents may omit Content-Type and contain opaque bytes.
	if err != nil && !(format == CloudEvents && contentType == "") {
		return nil, fmt.Errorf("invalid Content-Type: %w", err)
	}
	records := []map[string]interface{}{}
	appendRecord := func(v interface{}) error {
		m, ok := v.(map[string]interface{})
		if !ok || m == nil {
			return fmt.Errorf("record must be a JSON object")
		}
		if len(records) >= maxRecords {
			return fmt.Errorf("record count exceeds configured limit")
		}
		if format == CloudEvents {
			if err := ValidateEvent(m); err != nil {
				return err
			}
		}
		records = append(records, m)
		return nil
	}
	switch format {
	case NDJSON:
		if media != "application/x-ndjson" {
			return nil, fmt.Errorf("expected application/x-ndjson")
		}
		if len(body) > 0 && body[len(body)-1] != '\n' {
			return nil, fmt.Errorf("NDJSON must end in newline")
		}
		for _, line := range bytes.Split(body, []byte{'\n'}) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				continue
			}
			if bytes.ContainsRune(line, '\r') {
				return nil, fmt.Errorf("embedded CR in NDJSON")
			}
			v, e := JSON(line)
			if e != nil {
				return nil, e
			}
			if e = appendRecord(v); e != nil {
				return nil, e
			}
		}
	case CloudEvents:
		switch media {
		case "application/cloudevents+json":
			v, e := JSON(body)
			if e != nil {
				return nil, e
			}
			if e = appendRecord(v); e != nil {
				return nil, e
			}
		case "application/cloudevents-batch+json":
			v, e := JSON(body)
			if e != nil {
				return nil, e
			}
			a, ok := v.([]interface{})
			if !ok {
				return nil, fmt.Errorf("expected CloudEvents batch array")
			}
			for _, v := range a {
				if e = appendRecord(v); e != nil {
					return nil, e
				}
			}
		default:
			if strings.HasPrefix(media, "application/cloudevents") {
				return nil, fmt.Errorf("unsupported CloudEvents encoding")
			}
			m := map[string]interface{}{}
			for name, values := range headers {
				name = strings.ToLower(name)
				if !strings.HasPrefix(name, "ce-") {
					continue
				}
				name = strings.TrimPrefix(name, "ce-")
				if name == "datacontenttype" || name == "data" || name == "data_base64" || len(values) != 1 {
					return nil, fmt.Errorf("invalid binary CloudEvents header")
				}
				if _, ok := m[name]; ok {
					return nil, fmt.Errorf("duplicate CloudEvents header")
				}
				raw := values[0]
				if strings.HasPrefix(raw, "\"") {
					_, params, e := mime.ParseMediaType("x/x;v=" + raw)
					if e != nil || len(params) != 1 {
						return nil, fmt.Errorf("invalid quoted CloudEvents header")
					}
					raw = params["v"]
				}
				decoded, e := url.PathUnescape(raw)
				if e != nil || !utf8.ValidString(decoded) {
					return nil, fmt.Errorf("invalid CloudEvents header encoding")
				}
				m[name] = decoded
			}
			if contentType != "" {
				m["datacontenttype"] = contentType
			}
			if len(body) > 0 {
				if isJSON(media) {
					v, e := JSON(body)
					if e != nil {
						return nil, e
					}
					m["data"] = v
				} else {
					m["data_base64"] = base64.StdEncoding.EncodeToString(body)
				}
			}
			if e := appendRecord(m); e != nil {
				return nil, e
			}
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	return records, nil
}

func attributeString(v interface{}) (string, bool) {
	s, ok := v.(string)
	if !ok || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if r < 32 || (r >= 127 && r <= 159) {
			return "", false
		}
	}
	return s, true
}
func nameOK(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ValidateEvent checks the CloudEvents 1.0 context and JSON envelope. It does not
// synthesize source/id/type or replace a producer's event identity with a WAL ID.
func ValidateEvent(m map[string]interface{}) error {
	for _, key := range []string{"specversion", "id", "source", "type"} {
		s, ok := attributeString(m[key])
		if !ok || s == "" {
			return fmt.Errorf("CloudEvents %s must be a nonempty string", key)
		}
	}
	if m["specversion"] != "1.0" {
		return fmt.Errorf("only CloudEvents specversion 1.0 is supported")
	}
	if _, e := url.Parse(m["source"].(string)); e != nil {
		return fmt.Errorf("invalid source URI reference")
	}
	if data, ok := m["data_base64"]; ok {
		if _, both := m["data"]; both {
			return fmt.Errorf("data and data_base64 are mutually exclusive")
		}
		s, ok := data.(string)
		if !ok {
			return fmt.Errorf("data_base64 must be a string")
		}
		if _, e := base64.StdEncoding.Strict().DecodeString(s); e != nil {
			return fmt.Errorf("invalid data_base64")
		}
	}
	if data, exists := m["data"]; exists {
		if ct, ok := m["datacontenttype"].(string); ok {
			media, _, err := mime.ParseMediaType(ct)
			if err == nil && !isJSON(media) {
				if _, ok := data.(string); !ok {
					return fmt.Errorf("non-JSON data must be a string")
				}
			}
		}
	}
	for key, v := range m {
		if key == "data" || key == "data_base64" {
			continue
		}
		if !nameOK(key) {
			return fmt.Errorf("invalid CloudEvents attribute name %q", key)
		}
		switch key {
		case "specversion", "id", "source", "type":
			continue
		case "datacontenttype", "dataschema", "subject", "time":
			if v == nil {
				continue
			} // JSON-format null context attributes mean absent.
			s, ok := attributeString(v)
			if !ok {
				return fmt.Errorf("invalid %s attribute", key)
			}
			switch key {
			case "datacontenttype":
				if _, _, e := mime.ParseMediaType(s); e != nil {
					return fmt.Errorf("invalid datacontenttype")
				}
			case "dataschema":
				u, e := url.Parse(s)
				if e != nil || !u.IsAbs() {
					return fmt.Errorf("dataschema must be an absolute URI")
				}
			case "time":
				if _, e := time.Parse(time.RFC3339Nano, s); e != nil {
					return fmt.Errorf("invalid event time")
				}
			}
		default:
			switch v := v.(type) {
			case nil, bool:
			case string:
				if _, ok := attributeString(v); !ok {
					return fmt.Errorf("invalid extension string")
				}
			case int:
				if int64(v) < math.MinInt32 || int64(v) > math.MaxInt32 {
					return fmt.Errorf("extension integer exceeds int32")
				}
			case int64:
				if v < math.MinInt32 || v > math.MaxInt32 {
					return fmt.Errorf("extension integer exceeds int32")
				}
			case uint64:
				if v > math.MaxInt32 {
					return fmt.Errorf("extension integer exceeds int32")
				}
			default:
				return fmt.Errorf("unsupported extension attribute type for %s", key)
			}
		}
	}
	return nil
}

// Encode emits a complete request body. CloudEvents structured/binary modes take
// one event; batch mode must be explicitly configured for a willing recipient.
// The input maps are read-only and must remain owned by the caller until return.
func Encode(format, mode string, records []map[string]interface{}) ([]byte, http.Header, error) {
	h := http.Header{}
	var out bytes.Buffer
	for _, m := range records {
		if m == nil {
			return nil, nil, fmt.Errorf("nil record")
		}
	}
	switch format {
	case NDJSON:
		h.Set("Content-Type", "application/x-ndjson")
		e := json.NewEncoder(&out)
		e.SetEscapeHTML(false)
		for _, m := range records {
			if err := e.Encode(m); err != nil {
				return nil, nil, err
			}
		}
	case CloudEvents:
		for _, m := range records {
			if err := ValidateEvent(m); err != nil {
				return nil, nil, err
			}
		}
		if mode == "" {
			mode = Structured
		}
		switch mode {
		case Batch:
			h.Set("Content-Type", "application/cloudevents-batch+json")
			if records == nil {
				records = []map[string]interface{}{}
			}
			b, e := json.Marshal(records)
			return b, h, e
		case Structured, Binary:
			if len(records) != 1 {
				return nil, nil, fmt.Errorf("%s mode requires one event", mode)
			}
			m := records[0]
			if mode == Structured {
				h.Set("Content-Type", "application/cloudevents+json")
				b, e := json.Marshal(m)
				return b, h, e
			}
			for key, v := range m {
				if key == "data" || key == "data_base64" || v == nil {
					continue
				}
				if key == "datacontenttype" {
					h.Set("Content-Type", v.(string))
					continue
				}
				h.Set("Ce-"+key, encodeHeader(fmt.Sprint(v)))
			}
			if v, ok := m["data_base64"]; ok {
				b, e := base64.StdEncoding.Strict().DecodeString(v.(string))
				return b, h, e
			}
			if data, ok := m["data"]; ok {
				ct, exists := m["datacontenttype"]
				if !exists || ct == nil {
					h.Set("Content-Type", "application/json")
				}
				media, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
				if isJSON(media) {
					b, e := json.Marshal(data)
					return b, h, e
				}
				s, ok := data.(string)
				if !ok {
					return nil, nil, fmt.Errorf("non-JSON event data must be a string or data_base64")
				}
				return []byte(s), h, nil
			}
		default:
			return nil, nil, fmt.Errorf("unsupported CloudEvents mode %q", mode)
		}
	default:
		return nil, nil, fmt.Errorf("unsupported format %q", format)
	}
	return out.Bytes(), h, nil
}
func encodeHeader(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 32 || c > 126 || c == '"' || c == '%' {
			b.WriteByte('%')
			b.WriteString(strings.ToUpper(fmt.Sprintf("%02x", c)))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// RetrySeconds parses the integer form of Retry-After without accepting negative
// or overflowing values. HTTP-date values are handled at the transport layer.
func RetrySeconds(s string) (time.Duration, bool) {
	n, e := strconv.ParseInt(s, 10, 32)
	if e != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}
