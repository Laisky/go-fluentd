package recvs

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	kafka "github.com/Laisky/go-kafka"
	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
	"github.com/tinylib/msgp/msgp"
	"gofluentd/library"
)

func recvPool() *sync.Pool {
	return &sync.Pool{New: func() interface{} {
		return &library.FluentMsg{ExtIds: []int64{999}, Message: map[string]interface{}{"stale": true}}
	}}
}
func recvTake(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("receiver timed out")
		return nil
	}
}
func componentHTTP(t *testing.T) (*HTTPRecv, *gin.Engine, chan *library.FluentMsg) {
	t.Helper()
	e := gin.New()
	r := NewHTTPRecv(&HTTPRecvCfg{Name: "http", HTTPSrv: e, Path: "/logs/:env", Env: "sit", Tag: "forward", OrigTag: "app", TagKey: "tag", TimeKey: "ts", TimeFormat: time.RFC3339, TSRegexp: regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`), SigKey: "sig", SigSalt: []byte("test-salt"), MaxBodySize: 4096, MaxAllowedDelaySec: time.Minute, MaxAllowedAheadSec: time.Minute})
	out := make(chan *library.FluentMsg, 8)
	r.SetMsgPool(recvPool())
	r.SetCounter(utils.NewCounter())
	r.SetAsyncOutChan(out)
	return r, e, out
}
func signedHTTP(ts string) map[string]interface{} {
	sum := md5.Sum([]byte(ts + "test-salt"))
	return map[string]interface{}{"ts": ts, "sig": hex.EncodeToString(sum[:]), "nested": map[string]interface{}{"value": "ok"}}
}
func TestComponentHTTPValidationAndPublication(t *testing.T) {
	cases := []struct {
		name, env string
		edit      func(map[string]interface{})
	}{{"valid", "prod", nil}, {"bad env", "dev", nil}, {"missing timestamp", "sit", func(m map[string]interface{}) { delete(m, "ts") }}, {"wrong timestamp type", "sit", func(m map[string]interface{}) { m["ts"] = 42 }}, {"bad timestamp shape", "sit", func(m map[string]interface{}) { m["ts"] = "bad" }}, {"missing signature", "sit", func(m map[string]interface{}) { delete(m, "sig") }}, {"wrong signature type", "sit", func(m map[string]interface{}) { m["sig"] = 1 }}, {"invalid signature", "sit", func(m map[string]interface{}) { m["sig"] = "wrong" }}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, e, out := componentHTTP(t)
			m := signedHTTP(utils.Clock.GetUTCNow().Format(time.RFC3339))
			if tc.edit != nil {
				tc.edit(m)
			}
			body, _ := stdjson.Marshal(m)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("POST", "/logs/"+tc.env, bytes.NewReader(body)))
			if tc.name != "valid" {
				if w.Code != 400 || len(out) != 0 {
					t.Fatalf("rejected input status=%d published=%d", w.Code, len(out))
				}
				return
			}
			if w.Code != 200 || r.GetName() != "http" {
				t.Fatal(w.Code)
			}
			got := recvTake(t, out)
			if got.Tag != "forward.sit" || got.Message["tag"] != "app.prod" || got.Message["nested__value"] != "ok" {
				t.Fatal(got)
			}
			if len(got.ExtIds) != 0 || got.Message["stale"] != nil {
				t.Fatal("pooled receiver state contaminated a new record")
			}
			var resp map[string]int64
			if err := stdjson.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["msgid"] != got.ID {
				t.Fatalf("response ID mismatch: %s", w.Body.String())
			}
			r.Run(context.Background())
			probe := httptest.NewRecorder()
			e.ServeHTTP(probe, httptest.NewRequest("GET", "/logs/sit", nil))
			if probe.Code != 200 {
				t.Fatal(probe.Code)
			}
		})
	}
}
func TestComponentHTTPTimeWindowAndMalformedJSON(t *testing.T) {
	for _, delta := range []time.Duration{-2 * time.Minute, 2 * time.Minute} {
		t.Run(delta.String(), func(t *testing.T) {
			_, e, out := componentHTTP(t)
			body, _ := stdjson.Marshal(signedHTTP(utils.Clock.GetUTCNow().Add(delta).Format(time.RFC3339)))
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("POST", "/logs/sit", bytes.NewReader(body)))
			if w.Code != 400 || len(out) != 0 {
				t.Fatal("out-of-window input admitted")
			}
		})
	}
	for _, body := range []string{"null", "[]", "{", "123"} {
		t.Run(body, func(t *testing.T) {
			_, e, out := componentHTTP(t)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("POST", "/logs/sit", strings.NewReader(body)))
			if w.Code != 400 || len(out) != 0 {
				t.Fatal("malformed JSON published")
			}
		})
	}
}
func TestComponentHTTPCanceledBackpressureDoesNotAcknowledge(t *testing.T) {
	r, e, _ := componentHTTP(t)
	blocked := make(chan *library.FluentMsg)
	r.SetAsyncOutChan(blocked)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, _ := stdjson.Marshal(signedHTTP(utils.Clock.GetUTCNow().Format(time.RFC3339)))
	request := httptest.NewRequest("POST", "/logs/sit", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); e.ServeHTTP(w, request) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("canceled request blocked on the downstream queue")
		select {
		case <-blocked:
		case <-time.After(time.Second):
			t.Fatal("could not release old blocked handler")
		}
		<-done
	}
	if w.Code/100 == 2 {
		t.Fatal("canceled unpublished record was acknowledged")
	}
}
func TestComponentHTTPByteValidation(t *testing.T) {
	r, _, _ := componentHTTP(t)
	ts := utils.Clock.GetUTCNow().Format(time.RFC3339)
	m := signedHTTP(ts)
	m["ts"] = []byte(ts)
	m["sig"] = []byte(m["sig"].(string))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if !r.validate(c, &library.FluentMsg{Message: m}) {
		t.Fatal("valid byte-based timestamp/signature rejected")
	}
}
func TestComponentKafkaDecodingRoutingAndPoolReset(t *testing.T) {
	for _, isJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(isJSON), func(t *testing.T) {
			r := NewKafkaRecv(&KafkaCfg{IsJSONFormat: isJSON, Name: "kafka", Tag: "raw", TagKey: "tag", JSONTagKey: map[bool]string{true: "origin"}[isJSON], RewriteTag: "rewritten", KafkaCommitCfg: KafkaCommitCfg{AddCfg: library.AddCfg{"rewritten": {{"added": "%{@tag}"}}}}})
			r.SetCounter(utils.NewCounter())
			r.SetMsgPool(recvPool())
			payload := []byte("hello")
			if isJSON {
				payload = []byte(`{"origin":"app","value":3}`)
			}
			got, err := r.parse2Msg(&kafka.KafkaMsg{Message: payload})
			if err != nil {
				t.Fatal(err)
			}
			expectedTag := "raw"
			if isJSON {
				expectedTag = "app"
			}
			if got.Tag != "rewritten" || got.Message["tag"] != expectedTag || got.Message["added"] != "rewritten" || r.GetName() != "kafka" {
				t.Fatal(got)
			}
			if len(got.ExtIds) != 0 || got.Message["stale"] != nil {
				t.Fatal("pooled IDs/payload leaked into decoded Kafka record")
			}
			if !isJSON && !bytes.Equal(got.Message["log"].([]byte), payload) {
				t.Fatal("plain Kafka body changed")
			}
		})
	}
	if GetKafkaRewriteTag("app", "prod") != "app.prod" || GetKafkaRewriteTag("", "prod") != "" {
		t.Fatal("rewrite configuration")
	}
}
func TestComponentKafkaRejectsNonObjectJSON(t *testing.T) {
	for _, body := range []string{"null", "[]", "{", "1", `{"origin":7}`} {
		t.Run(body, func(t *testing.T) {
			r := NewKafkaRecv(&KafkaCfg{IsJSONFormat: true, Tag: "raw", TagKey: "tag"})
			if strings.Contains(body, "origin") {
				r.JSONTagKey = "origin"
			}
			r.SetCounter(utils.NewCounter())
			r.SetMsgPool(recvPool())
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("malformed JSON panic: %v", v)
				}
			}()
			if got, err := r.parse2Msg(&kafka.KafkaMsg{Message: []byte(body)}); err == nil || got != nil {
				t.Fatalf("bad JSON accepted: %+v %v", got, err)
			}
		})
	}
}
func newComponentFluent(t *testing.T) *FluentdRecv {
	t.Helper()
	r := NewFluentdRecv(&FluentdRecvCfg{Name: "fluent", NFork: 1, TagKey: "tag"})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(recvPool())
	return r
}
func TestComponentFluentWireFormatsAndMalformedEntries(t *testing.T) {
	cases := []struct {
		name  string
		frame library.FluentBatchMsg
		want  int
	}{{"message", library.FluentBatchMsg{"logs", 0, map[string]interface{}{"n": 1}}, 1}, {"forward", library.FluentBatchMsg{"logs", []interface{}{[]interface{}{0, map[string]interface{}{"n": 1}}, []interface{}{0, map[string]interface{}{"n": 2}}}}, 2}, {"malformed entries", library.FluentBatchMsg{"logs", []interface{}{42, []interface{}{0}, []interface{}{0, "bad"}, []interface{}{0, map[string]interface{}{"n": 1}}}}, 1}, {"invalid tag", library.FluentBatchMsg{42, 0, map[string]interface{}{"n": 1}}, 0}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newComponentFluent(t)
			r.concators = []chan *library.FluentMsg{make(chan *library.FluentMsg, 8)}
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(time.Second))
			done := make(chan interface{}, 1)
			go func() { defer func() { done <- recover() }(); r.decodeMsg(context.Background(), server) }()
			writer := msgp.NewWriter(client)
			if err := tc.frame.EncodeMsg(writer); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(); err != nil {
				t.Fatal(err)
			}
			client.Close()
			select {
			case v := <-done:
				if v != nil {
					t.Fatalf("untrusted frame panicked decoder: %v", v)
				}
			case <-time.After(time.Second):
				t.Fatal("decoder did not terminate at EOF")
			}
			if len(r.concators[0]) != tc.want {
				t.Fatalf("delivered=%d want=%d", len(r.concators[0]), tc.want)
			}
			for len(r.concators[0]) > 0 {
				m := <-r.concators[0]
				if m.Tag != "logs" || m.Message["n"] == nil {
					t.Fatal(m)
				}
				if len(m.ExtIds) != 0 {
					t.Fatal("wire decoder reused stale journal acknowledgement IDs")
				}
			}
		})
	}
}
func TestComponentFluentDecoderIdleCancellation(t *testing.T) {
	entered := make(chan struct{})
	r := newComponentFluent(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); r.decodeMsg(ctx, &readNoticeConn{Conn: server, entered: entered}) }()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("idle decoder ignores cancellation")
		client.Close()
		<-done
	}
}
func TestComponentFluentConcatenationKeepsTagsSeparate(t *testing.T) {
	r := newComponentFluent(t)
	r.concatTagCfg = map[string]*concatCfg{}
	for _, tag := range []string{"a", "b"} {
		r.concatTagCfg[tag] = &concatCfg{headRegexp: regexp.MustCompile("^HEAD"), msgKey: "log", identifierKey: "source"}
	}
	out, in := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
	r.SetAsyncOutChan(out)
	a := &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"log": "HEAD A", "source": "same"}}
	b := &library.FluentMsg{Tag: "b", Message: map[string]interface{}{"log": "orphan B", "source": "same"}}
	in <- a
	in <- b
	close(in)
	r.runConcator(context.Background(), 0, in)
	if len(out) != 2 {
		t.Fatalf("cross-tag concatenation merged independent records: got %d", len(out))
	}
	got := map[string]string{}
	for len(out) > 0 {
		m := <-out
		got[m.Tag] = string(m.Message["log"].([]byte))
	}
	if got["a"] != "HEAD A" || got["b"] != "orphan B" {
		t.Fatal(got)
	}
}

// Verify ordinary socket errors can be represented by the standard io contract.
var _ io.ReadCloser = (*regressionCountingBody)(nil)
var _ = http.StatusOK

type readNoticeConn struct {
	net.Conn
	entered chan struct{}
	once    sync.Once
}

func (c *readNoticeConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	return c.Conn.Read(p)
}
func TestComponentSyslogNormalizationDoesNotDeleteSameNameFields(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			cfg := &RsyslogCfg{Name: "syslog", Tag: "syslog.prod", TagKey: "tag", MsgKey: "content", TimeKey: "time", NewTimeKey: "@timestamp", NewTimeFormat: time.RFC3339, TimeShift: time.Hour, RewriteTags: map[string]string{"host": "hostname", "same": "same"}}
			if same {
				cfg.MsgKey = "message"
				cfg.TimeKey = "@timestamp"
			}
			r := NewRsyslogRecv(cfg)
			r.SetMsgPool(recvPool())
			r.SetCounter(utils.NewCounter())
			parts := map[string]interface{}{cfg.MsgKey: "hello", cfg.TimeKey: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC), "host": "node", "same": "keep"}
			msg := r.parseLogPart(parts)
			if msg == nil || msg.Tag != "syslog.prod" || msg.Message["message"] != "hello" || msg.Message["@timestamp"] != "2026-09-22T13:00:00Z" || msg.Message["hostname"] != "node" || msg.Message["same"] != "keep" {
				t.Fatalf("syslog fields lost: %+v", msg)
			}
			if len(msg.ExtIds) != 0 || r.GetName() != "syslog" {
				t.Fatal("pool reset/name incorrect")
			}
		})
	}
}
func TestComponentSyslogInvalidTimestampIsRejected(t *testing.T) {
	r := NewRsyslogRecv(&RsyslogCfg{TimeKey: "time", NewTimeKey: "@timestamp", MsgKey: "content"})
	r.SetMsgPool(recvPool())
	r.SetCounter(utils.NewCounter())
	if r.parseLogPart(map[string]interface{}{"time": "invalid", "content": "hello"}) != nil {
		t.Fatal("invalid syslog timestamp was forwarded instead of rejected")
	}
}
func TestComponentFluentPackedRecordsRecoverAfterBadFrame(t *testing.T) {
	r := newComponentFluent(t)
	r.concators = []chan *library.FluentMsg{make(chan *library.FluentMsg, 8)}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.decodeMsg(ctx, server) }()
	var packed bytes.Buffer
	inner := msgp.NewWriter(&packed)
	frame := library.FluentBatchMsg{0, map[string]interface{}{"value": "packed"}}
	if err := frame.EncodeMsg(inner); err != nil {
		t.Fatal(err)
	}
	if err := inner.Flush(); err != nil {
		t.Fatal(err)
	}
	writer := msgp.NewWriter(client)
	for _, body := range [][]byte{{0xc1}, packed.Bytes()} {
		frame := library.FluentBatchMsg{"logs", body}
		if err := frame.EncodeMsg(writer); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("malformed packed frame trapped the decoder")
	}
	if len(r.concators[0]) != 1 {
		t.Fatalf("packed decoded count=%d", len(r.concators[0]))
	}
}
