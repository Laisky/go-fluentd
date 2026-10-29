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
)

type Event struct{ Source, Severity, OriginalSeverity, Message string }
type Config struct {
	Token, ChatID  string
	IncludeMessage bool
	Tags, Services []string
}
type Stats struct{ Queued, Dropped, Suppressed, Failed, Sent uint64 }
type Observer struct {
	cfg                                       Config
	endpoint                                  string
	client                                    *http.Client
	queue                                     chan Event
	otlp                                      chan otlpPayload
	otlpSlots                                 chan struct{}
	done                                      chan struct{}
	mu                                        sync.Mutex
	seen                                      map[[32]byte]time.Time
	queued, dropped, suppressed, failed, sent atomic.Uint64
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
	cfg.Tags = append([]string(nil), cfg.Tags...)
	cfg.Services = append([]string(nil), cfg.Services...)
	if endpoint == "" {
		endpoint = "https://api.telegram.org"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	o := &Observer{cfg: cfg, endpoint: strings.TrimRight(endpoint, "/"), client: client, queue: make(chan Event, 64), otlp: make(chan otlpPayload, 4), otlpSlots: make(chan struct{}, 4), done: make(chan struct{}), seen: make(map[[32]byte]time.Time)}
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

// Observe never waits for delivery. Duplicate suppression is bounded and local
// to this process; queue saturation drops the alert, never the source record.
func (o *Observer) Observe(e Event) {
	if o == nil || !IsError(e.Severity) {
		return
	}
	if len(e.Source) > 8192 {
		e.Source = "[oversized source omitted]"
	}
	e.Source = clean(e.Source, 128)
	e.Severity = clean(strings.ToLower(strings.TrimSpace(e.Severity)), 32)
	if len(e.OriginalSeverity) > 8192 {
		e.OriginalSeverity = "[oversized severity omitted]"
	}
	e.OriginalSeverity = clean(e.OriginalSeverity, 64)
	// Bound work before regex processing. Values beyond this limit are omitted.
	if len(e.Message) > 8192 {
		e.Message = "[oversized message omitted]"
	}
	e.Message = clean(e.Message, 1400)
	sum := sha256.Sum256([]byte(e.Source + "\x00" + e.Severity + "\x00" + e.OriginalSeverity + "\x00" + e.Message))
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if until, ok := o.seen[sum]; ok && now.Before(until) {
		o.suppressed.Add(1)
		return
	}
	if len(o.seen) >= 1024 {
		for k, v := range o.seen {
			if !now.Before(v) {
				delete(o.seen, k)
			}
		}
		if len(o.seen) >= 1024 {
			o.dropped.Add(1)
			return
		}
	}
	select {
	case o.queue <- e:
		o.seen[sum] = now.Add(time.Minute)
		o.queued.Add(1)
	default:
		o.dropped.Add(1)
	}
}
func (o *Observer) Stats() Stats {
	return Stats{o.queued.Load(), o.dropped.Load(), o.suppressed.Load(), o.failed.Load(), o.sent.Load()}
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
	defer close(o.done)
	for {
		select {
		case <-ctx.Done():
			return
		case p := <-o.otlp:
			o.parseOTLP(p)
			<-o.otlpSlots
		case e := <-o.queue:
			text := fmt.Sprintf("Error log\nsource: %s\nlevel: %s", e.Source, e.Severity)
			if e.OriginalSeverity != "" {
				text += "\noriginal severity: " + e.OriginalSeverity
			}
			if o.cfg.IncludeMessage {
				text += "\nmessage: " + e.Message
			}
			for attempt := 0; attempt < 3; attempt++ {
				if !wait(ctx, time.Second) {
					return
				}
				retry, delay := o.send(ctx, text)
				if !retry {
					break
				}
				if attempt == 2 {
					o.failed.Add(1)
					break
				}
				backoff := time.Duration(1<<attempt) * time.Second
				if delay < backoff {
					delay = backoff
				}
				if !wait(ctx, delay) {
					return
				}
			}
		}
	}
}
func (o *Observer) send(ctx context.Context, text string) (bool, time.Duration) {
	body, _ := json.Marshal(map[string]string{"chat_id": o.cfg.ChatID, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint+"/bot"+o.cfg.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		o.failed.Add(1)
		return false, 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return true, time.Second
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
		o.sent.Add(1)
		return false, 0
	}
	if resp.StatusCode == 429 {
		seconds := result.Parameters.RetryAfter
		if decodeErr != nil || seconds < 0 || seconds > 3600 {
			o.failed.Add(1)
			return false, 0
		}
		d := time.Duration(seconds) * time.Second
		if d < time.Second {
			d = time.Second
		}
		return true, d
	}
	if resp.StatusCode >= 500 {
		return true, time.Second
	}
	o.failed.Add(1)
	return false, 0
}
