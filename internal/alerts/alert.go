// Package alerts provides bounded, best-effort notification of accepted logs.
//
// The observer is a side path, not an alert delivery system: nothing here takes
// part in journal acknowledgement, commit or downstream delivery, and in-memory
// queues can lose notifications without losing stored logs. Selection is an
// operator policy over producer-supplied names, not an authentication boundary.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Event contains bounded notification context. Timestamp is an optional source
// timestamp; ObservedAt is collector processing time and is not part of grouping.
type Event struct {
	Source           string
	Severity         string
	OriginalSeverity string
	Message          string
	// Fingerprint names the error kind for this process without revealing it.
	Fingerprint string
	// Suppressed counts events of this source withheld since it last notified.
	Suppressed uint64
	Timestamp  time.Time
	ObservedAt time.Time

	// origin is the allowlisted identity charged for this event. OTLP sources also
	// contain producer-chosen host and scope names, which must not select a budget.
	origin string
	// summary marks a count-only report for a source that fell silent.
	summary bool
}

// Stats is a point-in-time copy of the observer counters.
type Stats struct {
	// Queued, Dropped, Suppressed, Failed and Sent are legacy totals; Dropped mixes
	// event and request units.
	Queued     uint64
	Dropped    uint64
	Suppressed uint64
	Failed     uint64
	Sent       uint64

	DroppedRequests  uint64
	DroppedEvents    uint64
	ShutdownRequests uint64
	ShutdownEvents   uint64
	Retries          uint64
	Evicted          uint64
	// SourceLimited counts distinct error kinds refused by a source's group budget.
	SourceLimited uint64

	RequestTooLarge    uint64
	RequestQueueFull   uint64
	RequestDecodeError uint64

	EventQueueDepth int
	OTLPQueueDepth  int
	ParserAlive     bool
	DeliveryAlive   bool
}

// Observer applies alert policy to selected records and owns the parser and
// delivery workers.
type Observer struct {
	cfg      Config
	ctx      context.Context
	notifier Notifier

	lifecycle sync.RWMutex
	stopped   bool

	queue     chan Event
	otlp      chan logsDecoder
	otlpSlots chan struct{}
	done      chan struct{}

	fingerprints sync.Pool

	mu      sync.Mutex
	seen    map[groupKey]time.Time
	sources map[string]*sourceBudget

	queued, dropped, suppressed, failed, sent             atomic.Uint64
	droppedRequests, droppedEvents, shutdownRequests      atomic.Uint64
	shutdownEvents, retries, evicted, sourceLimited       atomic.Uint64
	requestTooLarge, requestQueueFull, requestDecodeError atomic.Uint64
	parserAlive, deliveryAlive                            atomic.Bool
}

// New starts the parser and delivery workers. endpoint is injected only by
// tests; production uses Telegram's fixed HTTPS origin. Errors never include
// credential values.
func New(ctx context.Context, cfg Config, endpoint string, client *http.Client) (*Observer, error) {
	o, err := newObserver(ctx, cfg)
	if err != nil {
		return nil, err
	}
	o.notifier = newTelegramNotifier(o.cfg, endpoint, client)
	go o.run(ctx)
	return o, nil
}

// newObserver builds an observer from validated settings without starting work.
func newObserver(ctx context.Context, cfg Config) (*Observer, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, errInvalidConfiguration
	}
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	o := &Observer{
		cfg:       cfg,
		ctx:       ctx,
		queue:     make(chan Event, cfg.EventQueueSize),
		otlp:      make(chan logsDecoder, cfg.OTLPQueueSize),
		otlpSlots: make(chan struct{}, cfg.OTLPQueueSize),
		done:      make(chan struct{}),
		seen:      make(map[groupKey]time.Time),
		sources:   make(map[string]*sourceBudget),
	}
	o.fingerprints.New = newFingerprinter()
	return o, nil
}

// IsError reports whether a textual severity qualifies for notification.
func IsError(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error", "fatal", "panic", "dpanic", "critical", "crit", "emergency", "alert":
		return true
	}
	return false
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// ObserveFluent accepts only configured tags. Message content is copied before
// the pooled source record can be reused, and only once severity qualifies.
func (o *Observer) ObserveFluent(tag string, record map[string]interface{}) {
	if o == nil || !contains(o.cfg.Tags, tag) {
		return
	}
	var level, message string
	if value, ok := record["level"]; ok {
		if !isErrorValue(value) {
			return
		}
		level = stringValue(value)
		if message = stringValue(record["msg"]); message == "" {
			message = stringValue(record["log"])
		}
	} else if level, message = embeddedFields(record["log"]); !IsError(level) {
		return
	}
	o.Observe(Event{Source: "fluent/" + tag, Severity: level, Message: message})
}

// isErrorValue classifies a record level without retaining byte-backed values.
func isErrorValue(v interface{}) bool {
	switch x := v.(type) {
	case string:
		return len(x) <= maxValueBytes && IsError(x)
	case []byte:
		return len(x) <= maxValueBytes && IsError(string(x))
	}
	return false
}

// embeddedFields decodes the bounded application JSON that some Fluent receivers
// retain as a `log` value. It is only needed when no structured level is present.
func embeddedFields(v interface{}) (level, message string) {
	var raw []byte
	switch x := v.(type) {
	case string:
		if s := strings.TrimSpace(x); s != "" && s[0] == '{' {
			raw = []byte(x)
		}
	case []byte:
		if s := bytes.TrimSpace(x); len(s) != 0 && s[0] == '{' {
			raw = x
		}
	}
	if raw == nil || len(raw) > maxValueBytes {
		return "", ""
	}
	var embedded struct {
		Level   string `json:"level"`
		Msg     string `json:"msg"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &embedded) != nil {
		return "", ""
	}
	if embedded.Msg != "" {
		return embedded.Level, embedded.Msg
	}
	return embedded.Level, embedded.Message
}

func stringValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		if len(x) > maxValueBytes {
			return "[oversized value omitted]"
		}
		return x
	case []byte:
		if len(x) > maxValueBytes {
			return "[oversized value omitted]"
		}
		return string(x)
	default:
		return ""
	}
}

// Observe never waits for delivery. Grouping and source budgets are bounded and
// local to this process; queue saturation drops the alert, never the source record.
func (o *Observer) Observe(e Event) {
	if o == nil || !IsError(e.Severity) {
		return
	}
	o.lifecycle.RLock()
	defer o.lifecycle.RUnlock()
	if o.stopped {
		o.shutdownEvents.Add(1)
		return
	}
	if len(e.Source) > maxValueBytes {
		e.Source = "[oversized source omitted]"
	}
	e.Source = cleanMetadata(e.Source, 128)
	if e.origin == "" {
		e.origin = e.Source
	}
	e.Severity = clean(strings.ToLower(strings.TrimSpace(e.Severity)), 32)
	if len(e.OriginalSeverity) > maxValueBytes {
		e.OriginalSeverity = "[oversized severity omitted]"
	}
	e.OriginalSeverity = cleanMetadata(e.OriginalSeverity, 64)
	// Bound work before hashing or regex processing. Values beyond this limit are omitted.
	if len(e.Message) > maxValueBytes {
		e.Message = "[oversized message omitted]"
	}
	// Identity comes from the raw text: it must not depend on whether the message
	// is displayed, and only a keyed digest of it can leave the process.
	key, fingerprint := o.identify(e)
	if o.cfg.IncludeMessage {
		e.Message = clean(e.Message, maxMessageRunes)
	} else {
		e.Message = ""
	}
	o.admit(e, key, fingerprint)
}

// Stats returns the current counters without blocking the workers.
func (o *Observer) Stats() Stats {
	return Stats{
		Queued:             o.queued.Load(),
		Dropped:            o.dropped.Load(),
		Suppressed:         o.suppressed.Load(),
		Failed:             o.failed.Load(),
		Sent:               o.sent.Load(),
		DroppedRequests:    o.droppedRequests.Load(),
		DroppedEvents:      o.droppedEvents.Load(),
		ShutdownRequests:   o.shutdownRequests.Load(),
		ShutdownEvents:     o.shutdownEvents.Load(),
		Retries:            o.retries.Load(),
		Evicted:            o.evicted.Load(),
		SourceLimited:      o.sourceLimited.Load(),
		RequestTooLarge:    o.requestTooLarge.Load(),
		RequestQueueFull:   o.requestQueueFull.Load(),
		RequestDecodeError: o.requestDecodeError.Load(),
		EventQueueDepth:    len(o.queue),
		OTLPQueueDepth:     len(o.otlp),
		ParserAlive:        o.parserAlive.Load(),
		DeliveryAlive:      o.deliveryAlive.Load(),
	}
}

// Wait blocks until cancellation has stopped both workers.
func (o *Observer) Wait() { <-o.done }
