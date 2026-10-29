// Package alerts provides bounded, best-effort notification of accepted logs.
package alerts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Event contains bounded notification context. Timestamp is an optional source
// timestamp; ObservedAt is collector processing time and is not part of grouping.
type Event struct {
	Source, Severity, OriginalSeverity, Message string
	Timestamp, ObservedAt                       time.Time
}
type Config struct {
	Token, ChatID                                            string
	IncludeMessage                                           bool
	Tags, Services                                           []string
	EventQueueSize, OTLPQueueSize, DedupEntries, MaxAttempts int
	DedupWindow, SendInterval                                time.Duration
}
type Stats struct {
	Queued, Dropped, Suppressed, Failed, Sent                                          uint64 // legacy totals
	DroppedRequests, DroppedEvents, ShutdownRequests, ShutdownEvents, Retries, Evicted uint64
	RequestTooLarge, RequestQueueFull, RequestDecodeError                              uint64
	EventQueueDepth, OTLPQueueDepth                                                    int
	ParserAlive, DeliveryAlive                                                         bool
}
type Observer struct {
	cfg                                                                                Config
	ctx                                                                                context.Context
	notifier                                                                           Notifier
	lifecycle                                                                          sync.RWMutex
	stopped                                                                            bool
	droppedRequests, droppedEvents, shutdownRequests, shutdownEvents, retries, evicted atomic.Uint64
	requestTooLarge, requestQueueFull, requestDecodeError                              atomic.Uint64
	parserAlive, deliveryAlive                                                         atomic.Bool
	queue                                                                              chan Event
	otlp                                                                               chan otlpPayload
	otlpSlots                                                                          chan struct{}
	done                                                                               chan struct{}
	mu                                                                                 sync.Mutex
	seen                                                                               map[[32]byte]time.Time
	queued, dropped, suppressed, failed, sent                                          atomic.Uint64
}

// DeliveryResult describes a transport outcome without exposing response bodies.
type DeliveryResult struct {
	Delivered, Retry bool
	Delay            time.Duration
}

// Notifier is the narrow transport boundary; it must honor cancellation.
type Notifier interface {
	Send(context.Context, string) DeliveryResult
}
type telegramNotifier struct {
	token, chatID, endpoint string
	client                  *http.Client
}

var tokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
var secretPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)["']?\s*[:=]\s*["']?)(?:bearer\s+)?[^\s,;"'}]+`)
var quotedSecretPattern = regexp.MustCompile(`(?i)((?:password|passwd|token|secret|api[_-]?key|authorization)["']?\s*[:=]\s*")[^"\r\n]*"`)
var bearerPattern = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;"'}]+`)
var botTokenPattern = regexp.MustCompile(`\b[0-9]{4,}:[A-Za-z0-9_-]{15,}\b`)
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s]+`)

// New starts one worker. endpoint is injected only by tests; production uses
// Telegram's fixed HTTPS origin. Errors never include credential values.
func New(ctx context.Context, cfg Config, endpoint string, client *http.Client) (*Observer, error) {
	if ctx == nil || ctx.Err() != nil || len(cfg.Token) > 256 || !tokenPattern.MatchString(cfg.Token) || strings.TrimSpace(cfg.ChatID) == "" || len(cfg.ChatID) > 256 || strings.ContainsAny(cfg.ChatID, "\r\n") {
		return nil, errors.New("invalid Telegram alert configuration")
	}
	if len(cfg.Tags)+len(cfg.Services) > 128 {
		return nil, errors.New("invalid Telegram alert source allowlist")
	}
	for _, values := range [][]string{cfg.Tags, cfg.Services} {
		for _, v := range values {
			if strings.TrimSpace(v) == "" || len(v) > 256 {
				return nil, errors.New("invalid Telegram alert source allowlist")
			}
		}
	}
	if cfg.EventQueueSize == 0 {
		cfg.EventQueueSize = 64
	}
	if cfg.OTLPQueueSize == 0 {
		cfg.OTLPQueueSize = 4
	}
	if cfg.DedupEntries == 0 {
		cfg.DedupEntries = 1024
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = time.Minute
	}
	if cfg.SendInterval == 0 {
		cfg.SendInterval = time.Second
	}
	if cfg.EventQueueSize < 1 || cfg.EventQueueSize > 4096 || cfg.OTLPQueueSize < 1 || cfg.OTLPQueueSize > 32 || cfg.DedupEntries < 1 || cfg.DedupEntries > 16384 || cfg.MaxAttempts < 1 || cfg.MaxAttempts > 5 || cfg.DedupWindow < time.Second || cfg.DedupWindow > time.Hour || cfg.SendInterval < 100*time.Millisecond || cfg.SendInterval > time.Minute {
		return nil, errors.New("invalid Telegram alert policy limits")
	}
	cfg.Tags = append([]string(nil), cfg.Tags...)
	cfg.Services = append([]string(nil), cfg.Services...)
	if endpoint == "" {
		endpoint = "https://api.telegram.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	o := &Observer{cfg: cfg, ctx: ctx, queue: make(chan Event, cfg.EventQueueSize), otlp: make(chan otlpPayload, cfg.OTLPQueueSize), otlpSlots: make(chan struct{}, cfg.OTLPQueueSize), done: make(chan struct{}), seen: make(map[[32]byte]time.Time)}
	o.notifier = telegramNotifier{token: cfg.Token, chatID: cfg.ChatID, endpoint: strings.TrimRight(endpoint, "/"), client: client}
	go o.run(ctx)
	return o, nil
}

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
// the pooled source record can be reused.
func (o *Observer) ObserveFluent(tag string, record map[string]interface{}) {
	if o == nil || !contains(o.cfg.Tags, tag) {
		return
	}
	levelValue, hasLevel := record["level"]
	level := stringValue(levelValue)
	msg := stringValue(record["msg"])
	if !hasLevel {
		// Some Fluent receivers retain the application JSON as a `log` string.
		// Decode a bounded envelope only when no structured level is present.
		raw := stringValue(record["log"])
		var embedded struct {
			Level   string `json:"level"`
			Msg     string `json:"msg"`
			Message string `json:"message"`
		}
		if raw != "" && json.Unmarshal([]byte(raw), &embedded) == nil {
			level = embedded.Level
			msg = embedded.Msg
			if msg == "" {
				msg = embedded.Message
			}
		}
	} else if msg == "" {
		msg = stringValue(record["log"])
	}
	if !IsError(level) {
		return
	}
	o.Observe(Event{Source: "fluent/" + tag, Severity: level, Message: msg})
}
func stringValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		if len(x) > 8192 {
			return "[oversized value omitted]"
		}
		return x
	case []byte:
		if len(x) > 8192 {
			return "[oversized value omitted]"
		}
		return string(x)
	default:
		return ""
	}
}
func clean(s string, limit int) string {
	if s == "" {
		return ""
	}
	// All supported key/value, URL and bot-token patterns contain ':' or '=';
	// bearer tokens are checked separately. Avoid regex replacement and allocation
	// for the common bounded metadata and message with no such marker.
	if len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, ":=") && !bearerPattern.MatchString(s) {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	s = urlPattern.ReplaceAllString(s, "[url redacted]")
	s = bearerPattern.ReplaceAllString(s, "Bearer [redacted]")
	s = quotedSecretPattern.ReplaceAllString(s, `${1}[redacted]"`)
	s = secretPattern.ReplaceAllString(s, "$1[redacted]")
	s = botTokenPattern.ReplaceAllString(s, "[redacted]")
	r := []rune(s)
	if len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

// cleanMetadata prevents producer-controlled metadata from creating lines or
// terminal formatting controls in the notification. Bound before processing.
func cleanMetadata(s string, limit int) string {
	if len(s) > 512 {
		s = "[oversized metadata omitted]"
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, s)
	return clean(s, limit)
}

// Observe never waits for delivery. Duplicate suppression is bounded and local
// to this process; queue saturation drops the alert, never the source record.
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
	if len(e.Source) > 8192 {
		e.Source = "[oversized source omitted]"
	}
	e.Source = cleanMetadata(e.Source, 128)
	e.Severity = clean(strings.ToLower(strings.TrimSpace(e.Severity)), 32)
	if len(e.OriginalSeverity) > 8192 {
		e.OriginalSeverity = "[oversized severity omitted]"
	}
	e.OriginalSeverity = cleanMetadata(e.OriginalSeverity, 64)
	// Message content is not needed for grouping when it will not be rendered.
	if !o.cfg.IncludeMessage {
		e.Message = ""
	}
	// Bound work before regex processing. Values beyond this limit are omitted.
	if len(e.Message) > 8192 {
		e.Message = "[oversized message omitted]"
	}
	e.Message = clean(e.Message, 1400)
	// Group events by rendered identity; hidden messages do not create visually identical alerts.
	messageKey := ""
	if o.cfg.IncludeMessage {
		messageKey = e.Message
	}
	sum := sha256.Sum256([]byte(e.Source + "\x00" + e.Severity + "\x00" + e.OriginalSeverity + "\x00" + messageKey))
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if e.ObservedAt.IsZero() {
		e.ObservedAt = now.UTC()
	}
	if until, ok := o.seen[sum]; ok {
		if now.Before(until) {
			o.suppressed.Add(1)
			return
		}
		delete(o.seen, sum)
	}
	if len(o.queue) == cap(o.queue) {
		o.dropped.Add(1)
		o.droppedEvents.Add(1)
		return
	}
	limit := o.cfg.DedupEntries
	if limit == 0 {
		limit = 1024
	}
	if len(o.seen) >= limit {
		// Evict one arbitrary entry in bounded space without scanning the table.
		for key := range o.seen {
			delete(o.seen, key)
			o.evicted.Add(1)
			break
		}
	}
	select {
	case o.queue <- e:
		window := o.cfg.DedupWindow
		if window == 0 {
			window = time.Minute
		}
		o.seen[sum] = now.Add(window)
		o.queued.Add(1)
	default:
		o.dropped.Add(1)
		o.droppedEvents.Add(1)
	}
}
func (o *Observer) Stats() Stats {
	return Stats{Queued: o.queued.Load(), Dropped: o.dropped.Load(), Suppressed: o.suppressed.Load(), Failed: o.failed.Load(), Sent: o.sent.Load(), DroppedRequests: o.droppedRequests.Load(), RequestTooLarge: o.requestTooLarge.Load(), RequestQueueFull: o.requestQueueFull.Load(), RequestDecodeError: o.requestDecodeError.Load(), DroppedEvents: o.droppedEvents.Load(), ShutdownRequests: o.shutdownRequests.Load(), ShutdownEvents: o.shutdownEvents.Load(), Retries: o.retries.Load(), Evicted: o.evicted.Load(), EventQueueDepth: len(o.queue), OTLPQueueDepth: len(o.otlp), ParserAlive: o.parserAlive.Load(), DeliveryAlive: o.deliveryAlive.Load()}
}
func (o *Observer) Wait() { <-o.done }
func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
func (o *Observer) run(ctx context.Context) {
	parserDone := make(chan struct{})
	go func() {
		o.parserAlive.Store(true)
		defer func() { o.parserAlive.Store(false); close(parserDone) }()
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-o.otlp:
				o.parseOTLP(p)
				<-o.otlpSlots
			}
		}
	}()
	o.deliveryAlive.Store(true)
	defer func() {
		o.deliveryAlive.Store(false)
		o.lifecycle.Lock()
		o.stopped = true
		o.lifecycle.Unlock()
		<-parserDone
		for {
			select {
			case <-o.queue:
				o.shutdownEvents.Add(1)
			default:
				goto requests
			}
		}
	requests:
		for {
			select {
			case <-o.otlp:
				o.shutdownRequests.Add(1)
			default:
				close(o.done)
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-o.queue:
			text := fmt.Sprintf("Error log\nsource: %s\nlevel: %s", e.Source, e.Severity)
			if !e.Timestamp.IsZero() {
				text += "\nevent_time: " + e.Timestamp.UTC().Format(time.RFC3339Nano)
			} else if !e.ObservedAt.IsZero() {
				text += "\nobserved_at: " + e.ObservedAt.UTC().Format(time.RFC3339Nano)
			}
			if e.OriginalSeverity != "" {
				text += "\noriginal severity: " + e.OriginalSeverity
			}
			if o.cfg.IncludeMessage {
				text += "\nmessage: " + e.Message
			}
			for attempt := 0; attempt < o.cfg.MaxAttempts; attempt++ {
				if !wait(ctx, o.cfg.SendInterval) {
					o.shutdownEvents.Add(1)
					return
				}
				retry, delay := o.send(ctx, text)
				if ctx.Err() != nil {
					if retry {
						o.shutdownEvents.Add(1)
					}
					return
				}
				if !retry {
					break
				}
				if attempt == o.cfg.MaxAttempts-1 {
					o.failed.Add(1)
					break
				}
				o.retries.Add(1)
				backoff := time.Duration(1<<attempt) * time.Second
				if delay < backoff {
					delay = backoff
				}
				if !wait(ctx, delay) {
					o.shutdownEvents.Add(1)
					return
				}
			}
		}
	}
}
func (o *Observer) send(ctx context.Context, text string) (bool, time.Duration) {
	result := o.notifier.Send(ctx, text)
	if result.Delivered {
		o.sent.Add(1)
	} else if !result.Retry {
		o.failed.Add(1)
	}
	return result.Retry, result.Delay
}
func (n telegramNotifier) Send(ctx context.Context, text string) DeliveryResult {
	body, _ := json.Marshal(map[string]string{"chat_id": n.chatID, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint+"/bot"+n.token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return DeliveryResult{}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return DeliveryResult{Retry: true, Delay: time.Second}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	decodeErr := json.Unmarshal(b, &result)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && result.OK {
		return DeliveryResult{Delivered: true}
	}
	if resp.StatusCode == 429 {
		seconds := result.Parameters.RetryAfter
		if decodeErr != nil || seconds < 0 || seconds > 3600 {
			return DeliveryResult{}
		}
		delay := time.Duration(seconds) * time.Second
		if delay < time.Second {
			delay = time.Second
		}
		return DeliveryResult{Retry: true, Delay: delay}
	}
	if resp.StatusCode >= 500 {
		return DeliveryResult{Retry: true, Delay: time.Second}
	}
	return DeliveryResult{}
}
