package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"gofluentd/library/otlpwire"
)

type capturedMessage struct {
	Text        string `json:"text"`
	LinkPreview struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

// captureTelegram records every outbound sendMessage body so that tests assert
// what actually leaves the collector rather than internal state.
func captureTelegram(t *testing.T) (*httptest.Server, <-chan capturedMessage) {
	t.Helper()
	captured := make(chan capturedMessage, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message capturedMessage
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Errorf("decode outbound body: %v", err)
		}
		captured <- message
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func startObserver(t *testing.T, cfg Config, server *httptest.Server) *Observer {
	t.Helper()
	cfg.Token, cfg.ChatID = "123:synthetic", "synthetic"
	if cfg.SendInterval == 0 {
		cfg.SendInterval = 100 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, cfg, server.URL, server.Client())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); o.Wait() })
	return o
}

func nextMessage(t *testing.T, captured <-chan capturedMessage, timeout time.Duration) capturedMessage {
	t.Helper()
	select {
	case message := <-captured:
		return message
	case <-time.After(timeout):
		t.Fatal("no notification delivered")
		return capturedMessage{}
	}
}

func logsRequest(tb testing.TB, raw string) *otlpwire.Request {
	tb.Helper()
	limits := otlpwire.Limits{WireBytes: 2 << 20, DecodedBytes: 2 << 20, Items: 2048}
	r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(raw), limits)
	if err != nil {
		tb.Fatal(err)
	}
	return r
}

// parseRequest runs one request through the queue and parser synchronously.
func parseRequest(o *Observer, r *otlpwire.Request) {
	o.ObserveOTLP(r)
	o.parseOTLP(<-o.otlp)
	<-o.otlpSlots
}

// wordyFailure returns messages that differ in words, not in digits, so they
// remain distinct error kinds after variable tokens are ignored.
func wordyFailure(i int) string {
	word := []byte{}
	for {
		word = append(word, byte('a'+i%26))
		i /= 26
		if i == 0 {
			return "failure " + string(word)
		}
	}
}

func TestTagOnlyConfigurationSkipsOTLPWork(t *testing.T) {
	o := inert()
	o.ObserveOTLP(logsRequest(t, `{"resourceLogs":[]}`))
	if s := o.Stats(); len(o.otlp) != 0 || s.Dropped != 0 || s.DroppedRequests != 0 {
		t.Fatalf("unselected OTLP was queued or counted as loss: depth=%d %+v", len(o.otlp), s)
	}
}

func TestDistinctHiddenErrorsFromOneSourceAreNotCollapsed(t *testing.T) {
	o := inert()
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "database connection refused"})
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "disk quota exceeded"})
	if s := o.Stats(); s.Queued != 2 || s.Suppressed != 0 {
		t.Fatalf("independent errors collapsed: %+v", s)
	}
}

func TestHiddenMessagesRemainPrivateButDistinguishable(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}}, server)
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "database connection refused"})
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "disk quota exceeded"})
	first := nextMessage(t, captured, 3*time.Second).Text
	second := nextMessage(t, captured, 3*time.Second).Text
	if first == second {
		t.Fatalf("distinct errors are visually identical: %q", first)
	}
	for _, text := range []string{first, second} {
		if strings.Contains(text, "database") || strings.Contains(text, "quota") {
			t.Fatalf("hidden message leaked: %q", text)
		}
	}
}

func TestVariableTokensShareOneGroup(t *testing.T) {
	for _, include := range []bool{false, true} {
		o := inert()
		o.cfg.IncludeMessage = include
		for _, message := range []string{
			"timeout after 31ms req=abc1 from 10.0.0.1:443",
			"Timeout after 950ms req=zzz2 from 10.0.0.2:8443",
			"timeout after 7ms req=550e8400-e29b-41d4-a716-446655440000 from 192.168.1.9:80",
		} {
			o.Observe(Event{Source: "app", Severity: "error", Message: message})
		}
		if s := o.Stats(); s.Queued != 1 || s.Suppressed != 2 {
			t.Fatalf("include=%v: variable values split one error kind: %+v", include, s)
		}
	}
}

func TestNonASCIIErrorTextStaysDistinct(t *testing.T) {
	o := inert()
	o.Observe(Event{Source: "app", Severity: "error", Message: "连接失败 42"})
	o.Observe(Event{Source: "app", Severity: "error", Message: "写入失败 42"})
	if s := o.Stats(); s.Queued != 2 {
		t.Fatalf("non-ASCII text ignored by grouping: %+v", s)
	}
}

func TestMessageLinesCannotForgeNotificationFields(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, IncludeMessage: true}, server)
	message := "boom"
	for _, separator := range []string{"\n", "\r", "\r\n", "\v", "\f", "\u0085", "\u2028", "\u2029"} {
		message += separator + "source: fluent/forged" + separator + "level: fatal"
	}
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": message + "\u202e"})
	text := nextMessage(t, captured, 3*time.Second).Text
	lines := regexp.MustCompile(`\r\n|[\n\r\v\f\x{85}\x{2028}\x{2029}]`).Split(text, -1)
	var sources, levels, block int
	for i, line := range lines {
		if strings.HasPrefix(line, "source:") {
			sources++
		}
		if strings.HasPrefix(line, "level:") {
			levels++
		}
		if block != 0 && !strings.HasPrefix(line, "| ") {
			t.Fatalf("message line %d is not delimited: %q", i, text)
		}
		if line == "message:" {
			block = i
		}
	}
	if sources != 1 || levels != 1 || block == 0 || strings.ContainsRune(text, '\u202e') {
		t.Fatalf("message forged notification fields: %q", text)
	}
}

func TestMultilineStackTraceRemainsReadable(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, IncludeMessage: true}, server)
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "panic boom\n\tmain.run\n\nsecond line"})
	text := nextMessage(t, captured, 3*time.Second).Text
	if want := "\nmessage:\n| panic boom\n| \tmain.run\n| \n| second line"; !strings.HasSuffix(text, want) {
		t.Fatalf("stack trace layout changed: %q", text)
	}
}

func TestRenderedNotificationStaysWithinTelegramLimit(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, IncludeMessage: true}, server)
	for i, message := range []string{strings.Repeat("\n", 8000), strings.Repeat("😀\n", 2000), strings.Repeat("😀", 2000)} {
		o.Observe(Event{Source: strings.Repeat("😀", 100) + wordyFailure(i), Severity: "error", OriginalSeverity: strings.Repeat("😀", 100), Message: message})
		text := nextMessage(t, captured, 3*time.Second).Text
		if units := len(utf16.Encode([]rune(text))); units > 4096 {
			t.Fatalf("message %d rendered to %d UTF-16 units", i, units)
		}
	}
}

func TestLinkPreviewsAreDisabled(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, IncludeMessage: true}, server)
	o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "fetch failed for attacker.example/leak"})
	if message := nextMessage(t, captured, 3*time.Second); !message.LinkPreview.IsDisabled {
		t.Fatalf("Telegram may fetch a producer-controlled link: %+v", message)
	}
}

func TestNoisySourceCannotStarveQuietSource(t *testing.T) {
	o := inert()
	o.cfg.IncludeMessage = true
	for i := 0; i < 1000; i++ {
		o.Observe(Event{Source: "fluent/noisy", Severity: "error", Message: wordyFailure(i)})
	}
	before := o.Stats()
	o.Observe(Event{Source: "fluent/quiet", Severity: "error", Message: "first failure"})
	if after := o.Stats(); after.DroppedEvents != before.DroppedEvents || after.Queued != before.Queued+1 {
		t.Fatalf("quiet source starved: before=%+v after=%+v", before, after)
	}
}

func TestRotatedOTLPIdentityCannotStarveOtherService(t *testing.T) {
	o := inert()
	o.cfg.Services = []string{"noisy", "quiet"}
	scopes := make([]string, 1000)
	for i := range scopes {
		scopes[i] = fmt.Sprintf(`{"scope":{"name":%q},"logRecords":[{"severityNumber":17,"body":{"stringValue":%q}}]}`, wordyFailure(i), wordyFailure(i))
	}
	parseRequest(o, logsRequest(t, `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"noisy"}}]},"scopeLogs":[`+strings.Join(scopes, ",")+`]}]}`))
	before := o.Stats()
	parseRequest(o, logsRequest(t, `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"quiet"}}]},"scopeLogs":[{"logRecords":[{"severityNumber":17,"body":{"stringValue":"first failure"}}]}]}]}`))
	if after := o.Stats(); after.DroppedEvents != before.DroppedEvents || after.Queued != before.Queued+1 {
		t.Fatalf("rotated scopes starved another service: before=%+v after=%+v", before, after)
	}
}

// The allowlist selects notifications; it does not authenticate producers. Any
// client holding the shared ingress credential can claim a selected name, so
// this pins the documented trust assumption.
func TestServiceNameSelectorIsNotAnAuthenticationBoundary(t *testing.T) {
	o := inert()
	o.cfg.Services = []string{"payments"}
	parseRequest(o, logsRequest(t, `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"payments"}},{"key":"host.name","value":{"stringValue":"untrusted-client"}}]},"scopeLogs":[{"logRecords":[{"severityNumber":17}]}]}]}`))
	if e := <-o.queue; !strings.HasPrefix(e.Source, "otlp/payments/untrusted-client") {
		t.Fatalf("unexpected source %q", e.Source)
	}
}

func TestInfoRecordsSkipMessageConversion(t *testing.T) {
	o := inert()
	body := make([]byte, 8<<10)
	for _, record := range []map[string]interface{}{{"level": "info", "msg": body}, {"level": []byte("info"), "msg": body}} {
		if allocs := testing.AllocsPerRun(100, func() { o.ObserveFluent("app", record) }); allocs != 0 {
			t.Errorf("INFO record allocated %v times", allocs)
		}
	}
}

func TestOTLPObservationDoesNotCopyPayload(t *testing.T) {
	o := inert()
	o.cfg.Services = []string{"svc"}
	r := logsRequest(t, `{"resourceLogs":[],"padding":"`+strings.Repeat("x", 1<<20-128)+`"}`)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 16; i++ {
		o.ObserveOTLP(r)
		<-o.otlp
		<-o.otlpSlots
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= 1<<20 {
		t.Fatalf("synchronous observation allocated %d bytes", allocated)
	}
}

func TestRepeatedErrorsReportSuppressedCount(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, DedupWindow: time.Second}, server)
	record := map[string]interface{}{"level": "error", "msg": "disk quota exceeded"}
	for i := 0; i < 4; i++ {
		o.ObserveFluent("app", record)
	}
	if first := nextMessage(t, captured, 3*time.Second).Text; strings.Contains(first, "suppressed_since_last") {
		t.Fatalf("first notification reported history: %q", first)
	}
	time.Sleep(1100 * time.Millisecond)
	o.ObserveFluent("app", record)
	if second := nextMessage(t, captured, 3*time.Second).Text; !strings.Contains(second, "suppressed_since_last: 3") {
		t.Fatalf("suppressed count missing: %q", second)
	}
}

func TestBurstFollowedBySilenceStillReportsSuppressedCount(t *testing.T) {
	server, captured := captureTelegram(t)
	o := startObserver(t, Config{Tags: []string{"app"}, DedupWindow: time.Second}, server)
	for i := 0; i < 4; i++ {
		o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "disk quota exceeded"})
	}
	nextMessage(t, captured, 3*time.Second)
	summary := nextMessage(t, captured, 3*time.Second).Text
	if !strings.Contains(summary, "source: fluent/app") || !strings.Contains(summary, "suppressed_since_last: 3") {
		t.Fatalf("no summary after silence: %q", summary)
	}
}

func TestSourceBudgetBoundsNotificationsAndReportsOverflow(t *testing.T) {
	o := inert()
	o.cfg.MaxGroupsPerSource = 2
	for i := 0; i < 5; i++ {
		o.Observe(Event{Source: "fluent/app", Severity: "error", Message: wordyFailure(i)})
	}
	if s := o.Stats(); s.Queued != 2 || s.SourceLimited != 3 || s.DroppedEvents != 0 {
		t.Fatalf("budget not enforced: %+v", s)
	}
	o.mu.Lock()
	o.sources["fluent/app"].until = time.Now()
	o.mu.Unlock()
	<-o.queue
	<-o.queue
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "failure after the window"})
	if e := <-o.queue; e.Suppressed != 3 {
		t.Fatalf("overflow count not carried: %+v", e)
	}
}

func TestSingleGroupBudgetRestoresOneNotificationPerSource(t *testing.T) {
	o := inert()
	o.cfg.MaxGroupsPerSource = 1
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "database connection refused"})
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "disk quota exceeded"})
	if s := o.Stats(); s.Queued != 1 || s.SourceLimited != 1 {
		t.Fatalf("coarse grouping unavailable: %+v", s)
	}
}

func TestSummaryWaitsForQueueSpaceWithoutLosingCount(t *testing.T) {
	o := inert()
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "disk quota exceeded"})
	o.Observe(Event{Source: "fluent/app", Severity: "error", Message: "disk quota exceeded"})
	later := time.Now().Add(2 * o.cfg.DedupWindow)
	<-o.queue
	for i := 0; i < cap(o.queue); i++ {
		o.queue <- Event{}
	}
	o.flushSummaries(later)
	for i := 0; i < cap(o.queue); i++ {
		<-o.queue
	}
	o.flushSummaries(later)
	if e := <-o.queue; !e.summary || e.Suppressed != 1 || e.Source != "fluent/app" {
		t.Fatalf("summary lost: %+v", e)
	}
	o.flushSummaries(later)
	if len(o.queue) != 0 {
		t.Fatal("summary repeated")
	}
}

func TestSummaryNamesOnlyTheAllowlistedSource(t *testing.T) {
	o := inert()
	o.cfg.Services = []string{"svc"}
	raw := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}},{"key":"host.name","value":{"stringValue":"untrusted-host"}}]},"scopeLogs":[{"logRecords":[{"severityNumber":17,"body":{"stringValue":"disk quota exceeded"}}]}]}]}`
	parseRequest(o, logsRequest(t, raw))
	parseRequest(o, logsRequest(t, raw))
	<-o.queue
	o.flushSummaries(time.Now().Add(2 * o.cfg.DedupWindow))
	if e := <-o.queue; e.Source != "otlp/svc" {
		t.Fatalf("summary exposed producer-chosen identity: %+v", e)
	}
}

func TestFingerprintIsKeyedPerObserver(t *testing.T) {
	first, second := inert(), inert()
	e := Event{Source: "app", Severity: "error", Message: "disk quota exceeded"}
	_, a := first.identify(e)
	_, b := first.identify(e)
	_, c := second.identify(e)
	if a != b || a == c {
		t.Fatalf("fingerprints: stable=%v keyed=%v", a == b, a != c)
	}
}

func TestConfigDefaultsAndBounds(t *testing.T) {
	cfg, err := Config{Token: "123:synthetic", ChatID: "synthetic"}.normalized()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EventQueueSize != 64 || cfg.OTLPQueueSize != 4 || cfg.DedupEntries != 1024 || cfg.MaxAttempts != 3 || cfg.MaxGroupsPerSource != 8 || cfg.DedupWindow != time.Minute || cfg.SendInterval != time.Second {
		t.Fatalf("default policy changed: %+v", cfg)
	}
	for name, mutate := range map[string]func(*Config){
		"negative groups": func(c *Config) { c.MaxGroupsPerSource = -1 },
		"too many groups": func(c *Config) { c.MaxGroupsPerSource = 1025 },
		"short window":    func(c *Config) { c.DedupWindow = time.Millisecond },
		"bad token":       func(c *Config) { c.Token = "synthetic" },
		"too many tags":   func(c *Config) { c.Tags = make([]string, maxSources+1) },
	} {
		candidate := Config{Token: "123:synthetic", ChatID: "synthetic"}
		mutate(&candidate)
		if _, err := candidate.normalized(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
