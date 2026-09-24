// Package otlpwire is the protocol foundation for OTLP/HTTP integration.
// It does not register a receiver, export telemetry or acknowledge a WAL record.
package otlpwire

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
)

// Signal is selected by the endpoint. Protobuf is not self-describing.
type Signal string

const (
	Logs     Signal = "logs"
	Metrics  Signal = "metrics"
	Traces   Signal = "traces"
	JSON            = "application/json"
	Protobuf        = "application/x-protobuf"
)

func (s Signal) Path() (string, error) {
	switch s {
	case Logs, Metrics, Traces:
		return "/v1/" + string(s), nil
	}
	return "", fmt.Errorf("unsupported OTLP signal %q", s)
}

// Limits bounds encoded and decompressed input independently. Items counts
// logs, spans or data points, not requests or metric descriptors. Input bounds
// do not bound the decoder's allocations or aggregate process memory.
type Limits struct {
	WireBytes, DecodedBytes int64
	Items                   int
}

func DefaultLimits() Limits { return Limits{4 << 20, 4 << 20, 10000} }
func (l Limits) valid() error {
	if l.WireBytes <= 0 || l.DecodedBytes <= 0 || l.Items <= 0 || l.WireBytes == math.MaxInt64 || l.DecodedBytes == math.MaxInt64 {
		return errors.New("OTLP limits must be positive and permit a one-byte overflow probe")
	}
	return nil
}

var ErrTooLarge = errors.New("OTLP message exceeds configured limit")
var ErrMediaType = errors.New("unsupported OTLP media type or content encoding")

// Request keeps the complete uncompressed wire payload, including unknown
// fields. Keep one Export request as one WAL record; never flatten metrics into
// log maps. Counting/schema decoding does not validate all semantic conventions.
// A nonempty envelope with zero known items must not be silently discarded.
type Request struct {
	signal      Signal
	contentType string
	payload     []byte
	items       int
}

func (r *Request) Signal() Signal      { return r.signal }
func (r *Request) ContentType() string { return r.contentType }
func (r *Request) Items() int          { return r.items }

// Payload returns a copy so callers cannot mutate retained request bytes.
func (r *Request) Payload() []byte { return bytes.Clone(r.payload) }

func mediaType(value string) (string, error) {
	m, p, err := mime.ParseMediaType(value)
	if err != nil || (m != JSON && m != Protobuf) {
		return "", ErrMediaType
	}
	if c := p["charset"]; m == JSON && c != "" && !strings.EqualFold(c, "utf-8") {
		return "", ErrMediaType
	}
	return m, nil
}
func readLimit(r io.Reader, n int64) ([]byte, error) {
	if r == nil {
		return nil, errors.New("nil body reader")
	}
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if int64(len(b)) > n {
		return nil, ErrTooLarge
	}
	return b, err
}
func readBody(r io.Reader, encoding string, l Limits) ([]byte, error) {
	encoding = strings.ToLower(strings.TrimSpace(encoding))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		return nil, ErrMediaType
	}
	b, err := readLimit(r, l.WireBytes)
	if err != nil {
		return nil, err
	}
	if encoding != "gzip" {
		if int64(len(b)) > l.DecodedBytes {
			return nil, ErrTooLarge
		}
		return b, nil
	}
	z, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer z.Close()
	// Reading to EOF checks the checksum and every member, except when oversized.
	return readLimit(z, l.DecodedBytes)
}

type decoder interface {
	UnmarshalJSON([]byte) error
	UnmarshalProto([]byte) error
}

func decode(d decoder, ct string, b []byte) error {
	if ct == JSON {
		trim := bytes.TrimSpace(b)
		if !utf8.Valid(b) || len(trim) == 0 || trim[0] != '{' || !json.Valid(b) {
			return errors.New("expected one UTF-8 OTLP JSON object")
		}
		return d.UnmarshalJSON(b)
	}
	return d.UnmarshalProto(b)
}

// ReadRequest uses the OpenTelemetry Collector's OTLP decoders, but retains the
// ORIGINAL bytes, not their re-marshaled model. The caller closes the reader.
// This does not provide durable admission: an adapter must wait for journal Sync.
func ReadRequest(signal Signal, contentType, encoding string, body io.Reader, l Limits) (*Request, error) {
	if _, err := signal.Path(); err != nil {
		return nil, err
	}
	if err := l.valid(); err != nil {
		return nil, err
	}
	ct, err := mediaType(contentType)
	if err != nil {
		return nil, err
	}
	b, err := readBody(body, encoding, l)
	if err != nil {
		return nil, err
	}
	var n int
	switch signal {
	case Logs:
		r := plogotlp.NewExportRequest()
		err = decode(r, ct, b)
		if err == nil {
			n = r.Logs().LogRecordCount()
		}
	case Metrics:
		r := pmetricotlp.NewExportRequest()
		err = decode(r, ct, b)
		if err == nil {
			n = r.Metrics().DataPointCount()
		}
	case Traces:
		r := ptraceotlp.NewExportRequest()
		err = decode(r, ct, b)
		if err == nil {
			n = r.Traces().SpanCount()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", signal, err)
	}
	if n > l.Items {
		return nil, ErrTooLarge
	}
	return &Request{signal, ct, b, n}, nil
}

// Disposition prevents confusing partial/permanent rejection with a retryable
// transport error. In particular, blindly retrying partial success violates OTLP.
type Disposition uint8

const (
	InvalidResponse Disposition = iota
	Accepted
	PartiallyRejected
	PermanentlyRejected
	Retryable
)

// Outcome is not a journal commit decision. Partial/permanent/invalid outcomes
// need explicit durable handling, not silent delivery ACK or automatic replay.
// Rejected counts do not identify the individual failed data points.
type Outcome struct {
	Disposition Disposition
	Rejected    int64
	Diagnostic  string
	RetryAfter  time.Duration
}

func (o Outcome) MayRetry() bool      { return o.Disposition == Retryable }
func (o Outcome) FullyAccepted() bool { return o.Disposition == Accepted }

// RetryDelay accepts delta seconds or an HTTP-date. Invalid, past and overflowing
// values are ignored. An exporter must add backoff/jitter; this never sleeps.
func RetryDelay(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	digits := true
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		n, err := strconv.ParseUint(value, 10, 63)
		if err != nil || n > uint64(math.MaxInt64/int64(time.Second)) {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if d, err := http.ParseTime(value); err == nil && d.After(now) {
		return d.Sub(now)
	}
	return 0
}

// SuccessBody returns an empty Export*ServiceResponse. Adapters MUST use HTTP
// 200, not 204, with matching Content-Type, after their acceptance contract holds.
func SuccessBody(s Signal, contentType string) ([]byte, error) {
	if _, err := s.Path(); err != nil {
		return nil, err
	}
	ct, err := mediaType(contentType)
	if err != nil {
		return nil, err
	}
	if ct == JSON {
		return []byte("{}"), nil
	}
	return []byte{}, nil
}

// ReadResponse classifies a bounded response without retrying, acknowledging or
// closing the reader. Only 429/502/503/504 are retryable HTTP statuses. Oversized
// responses are non-retryable even on those statuses. Network failure before a
// response is a separate transport decision. sentItems must be nonnegative.
func ReadResponse(s Signal, requestType string, status int, h http.Header, body io.Reader, l Limits, sentItems int64, now time.Time) (Outcome, error) {
	invalid := Outcome{Disposition: InvalidResponse}
	if _, err := s.Path(); err != nil {
		return invalid, err
	}
	if err := l.valid(); err != nil {
		return invalid, err
	}
	if sentItems < 0 {
		return invalid, errors.New("negative item count")
	}
	requestCT, err := mediaType(requestType)
	if err != nil {
		return invalid, err
	}
	b, err := readBody(body, h.Get("Content-Encoding"), l)
	if err != nil {
		return invalid, err
	}
	if status != 200 {
		switch status {
		case 429, 502, 503, 504:
			return Outcome{Disposition: Retryable, RetryAfter: RetryDelay(h.Get("Retry-After"), now)}, nil
		default:
			return Outcome{Disposition: PermanentlyRejected, Diagnostic: fmt.Sprintf("OTLP HTTP status %d", status)}, nil
		}
	}
	ct, err := mediaType(h.Get("Content-Type"))
	if err != nil || ct != requestCT {
		return invalid, errors.New("OTLP response Content-Type must match request")
	}
	var rejected int64
	var message string
	switch s {
	case Logs:
		r := plogotlp.NewExportResponse()
		err = decode(r, ct, b)
		if err == nil {
			rejected = r.PartialSuccess().RejectedLogRecords()
			message = r.PartialSuccess().ErrorMessage()
		}
	case Metrics:
		r := pmetricotlp.NewExportResponse()
		err = decode(r, ct, b)
		if err == nil {
			rejected = r.PartialSuccess().RejectedDataPoints()
			message = r.PartialSuccess().ErrorMessage()
		}
	case Traces:
		r := ptraceotlp.NewExportResponse()
		err = decode(r, ct, b)
		if err == nil {
			rejected = r.PartialSuccess().RejectedSpans()
			message = r.PartialSuccess().ErrorMessage()
		}
	}
	if err != nil {
		return invalid, fmt.Errorf("decode export response: %w", err)
	}
	if rejected < 0 || rejected > sentItems {
		return invalid, errors.New("invalid rejected item count")
	}
	if rejected > 0 {
		return Outcome{Disposition: PartiallyRejected, Rejected: rejected, Diagnostic: message}, nil
	}
	return Outcome{Disposition: Accepted, Diagnostic: message}, nil // zero-count warning: no retry
}
