package recvs

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"reflect"
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

func behaviorDirtyPool() *sync.Pool {
	return &sync.Pool{New: func() interface{} {
		return &library.FluentMsg{Tag: "stale", ID: 999, ExtIds: []int64{555}, Message: map[string]interface{}{"stale": true}}
	}}
}
func TestBehaviorKafkaDecodeAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		isJSON     bool
		tagKey     string
		valid      bool
	}{
		{"raw", "hello", false, "", true}, {"object", `{"origin":"app.prod","value":3}`, true, "origin", true},
		{"malformed", `{"value":`, true, "", false}, {"null", `null`, true, "", false}, {"array", `[]`, true, "", false},
		{"missing_tag", `{"value":1}`, true, "origin", false}, {"wrong_tag_type", `{"origin":3}`, true, "origin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("invalid Kafka payload panicked: %v", p)
				}
			}()
			r := NewKafkaRecv(&KafkaCfg{Tag: "source", TagKey: "tag", MsgKey: "log", IsJSONFormat: tc.isJSON, JSONTagKey: tc.tagKey, RewriteTag: "destination"})
			r.SetCounter(utils.NewCounter())
			r.SetMsgPool(behaviorDirtyPool())
			raw := []byte(tc.body)
			m, err := r.parse2Msg(&kafka.KafkaMsg{Message: raw})
			if !tc.valid {
				if err == nil || m != nil {
					t.Fatal("invalid input accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.ID != 1 || m.Tag != "destination" || len(m.ExtIds) != 0 {
				t.Errorf("stale pooled metadata survived: %+v", m)
			}
			origin := "source"
			if tc.tagKey != "" {
				origin = "app.prod"
			}
			if m.Message["tag"] != origin {
				t.Error(m.Message)
			}
			if !tc.isJSON {
				raw[0] = 'X'
				if string(m.Message["log"].([]byte)) != "hello" {
					t.Error("decoded log aliases a recycled Kafka input buffer")
				}
			}
			if _, ok := m.Message["stale"]; ok {
				t.Fatal("stale field")
			}
		})
	}
	if GetKafkaRewriteTag("", "prod") != "" || GetKafkaRewriteTag("app", "prod") != "app.prod" {
		t.Fatal("Kafka tag config")
	}
}
func behaviorHTTPReceiver() (*HTTPRecv, *gin.Engine, chan *library.FluentMsg) {
	srv := gin.New()
	out := make(chan *library.FluentMsg, 8)
	r := NewHTTPRecv(&HTTPRecvCfg{Name: "behavior", HTTPSrv: srv, Path: "/logs/:env", Env: "sit", Tag: "forward", OrigTag: "app", TagKey: "tag", TimeKey: "time", TimeFormat: time.RFC3339Nano, TSRegexp: regexp.MustCompile(`^\d{4}-\d\d-\d\dT`), SigKey: "sig", SigSalt: []byte("salt"), MaxBodySize: 1024, MaxAllowedDelaySec: time.Minute, MaxAllowedAheadSec: time.Minute})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(behaviorDirtyPool())
	r.SetAsyncOutChan(out)
	return r, srv, out
}
func behaviorSignedHTTP(ts time.Time) map[string]interface{} {
	s := ts.UTC().Format(time.RFC3339Nano)
	sum := md5.Sum([]byte(s + "salt"))
	return map[string]interface{}{"time": s, "sig": hex.EncodeToString(sum[:]), "nested": map[string]interface{}{"value": "ok"}}
}
func TestBehaviorHTTPValidationAndPoolReset(t *testing.T) {
	for _, name := range []string{"valid", "env", "json", "null", "missing_time", "wrong_time_type", "time_format", "bad_signature", "missing_signature", "stale", "future"} {
		t.Run(name, func(t *testing.T) {
			_, srv, out := behaviorHTTPReceiver()
			payload := behaviorSignedHTTP(utils.Clock.GetUTCNow())
			path := "/logs/prod"
			switch name {
			case "env":
				path = "/logs/unknown"
			case "missing_time":
				delete(payload, "time")
			case "wrong_time_type":
				payload["time"] = 123
			case "time_format":
				payload["time"] = "nonsense"
			case "bad_signature":
				payload["sig"] = "wrong"
			case "missing_signature":
				delete(payload, "sig")
			case "stale":
				payload = behaviorSignedHTTP(utils.Clock.GetUTCNow().Add(-time.Hour))
			case "future":
				payload = behaviorSignedHTTP(utils.Clock.GetUTCNow().Add(time.Hour))
			}
			b, err := stdjson.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if name == "json" {
				b = []byte("{")
			}
			if name == "null" {
				b = []byte("null")
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(b)))
			if name != "valid" {
				if w.Code != 400 || len(out) != 0 {
					t.Fatalf("rejected input was accepted: status=%d queued=%d", w.Code, len(out))
				}
				return
			}
			if w.Code != 200 || len(out) != 1 {
				t.Fatalf("valid request failed: %d %s", w.Code, w.Body)
			}
			m := <-out
			if m.Tag != "forward.sit" || m.Message["tag"] != "app.prod" || m.Message["nested__value"] != "ok" || m.ID != 1 || len(m.ExtIds) != 0 {
				t.Fatalf("bad accepted record: %+v", m)
			}
		})
	}
}
func TestBehaviorHTTPCancelledRequestCannotAcknowledgeUnqueuedMessage(t *testing.T) {
	r, srv, _ := behaviorHTTPReceiver()
	blocked := make(chan *library.FluentMsg)
	r.SetAsyncOutChan(blocked)
	b, _ := stdjson.Marshal(behaviorSignedHTTP(utils.Clock.GetUTCNow()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/logs/sit", bytes.NewReader(b)).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); srv.ServeHTTP(w, req) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("cancelled HTTP request stuck on downstream channel")
		<-blocked
		<-done
	}
	if w.Code == 200 {
		t.Error("HTTP 200 returned before the message entered the queue")
	}
}
func behaviorFluentReceiver() *FluentdRecv {
	r := NewFluentdRecv(&FluentdRecvCfg{Name: "behavior", NFork: 1})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(behaviorDirtyPool())
	r.SetAsyncOutChan(make(chan *library.FluentMsg, 16))
	return r
}
func behaviorDecodeFrame(t *testing.T, frame library.FluentBatchMsg) ([]*library.FluentMsg, interface{}) {
	t.Helper()
	r := behaviorFluentReceiver()
	out := make(chan *library.FluentMsg, 16)
	r.SetAsyncOutChan(out)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan interface{}, 1)
	go func() { defer func() { done <- recover() }(); r.decodeMsg(ctx, a) }()
	var buf bytes.Buffer
	writer := msgp.NewWriter(&buf)
	if err := frame.EncodeMsg(writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	b.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := b.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	b.Close()
	var p interface{}
	select {
	case p = <-done:
	case <-time.After(time.Second):
		t.Fatal("decoder did not finish")
	}
	got := []*library.FluentMsg{}
	for len(out) > 0 {
		got = append(got, <-out)
	}
	return got, p
}
func TestBehaviorFluentReceiverWireFormatsAndMalformedEntries(t *testing.T) {
	var packed bytes.Buffer
	w := msgp.NewWriter(&packed)
	entry := library.FluentBatchMsg{int64(1), map[string]interface{}{"value": "packed"}}
	if err := entry.EncodeMsg(w); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	cases := []struct {
		name  string
		frame library.FluentBatchMsg
		want  []string
	}{
		{"message", library.FluentBatchMsg{"logs", int64(1), map[string]interface{}{"value": "single"}}, []string{"single"}},
		{"forward", library.FluentBatchMsg{"logs", []interface{}{[]interface{}{int64(1), map[string]interface{}{"value": "first"}}, []interface{}{int64(2), map[string]interface{}{"value": "second"}}}}, []string{"first", "second"}},
		{"packed", library.FluentBatchMsg{"logs", packed.Bytes()}, []string{"packed"}},
		{"bad_tag", library.FluentBatchMsg{42, []interface{}{}}, nil}, {"short", library.FluentBatchMsg{"logs"}, nil},
		{"wrong_entry", library.FluentBatchMsg{"logs", []interface{}{3, []interface{}{int64(1), map[string]interface{}{"value": "valid"}}}}, []string{"valid"}},
		{"short_entry", library.FluentBatchMsg{"logs", []interface{}{[]interface{}{}, []interface{}{int64(1), map[string]interface{}{"value": "valid"}}}}, []string{"valid"}},
		{"non_record", library.FluentBatchMsg{"logs", []interface{}{[]interface{}{int64(1), 3}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, p := behaviorDecodeFrame(t, tc.frame)
			if p != nil {
				t.Fatalf("network input panicked: %v", p)
			}
			values := []string{}
			for _, m := range got {
				values = append(values, m.Message["value"].(string))
				if m.Tag != "logs" || len(m.ExtIds) != 0 || m.Message["stale"] != nil {
					t.Errorf("stale record metadata: %+v", m)
				}
			}
			if strings.Join(values, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("records=%v want %v", values, tc.want)
			}
		})
	}
}

type behaviorObservedConn struct {
	net.Conn
	once    sync.Once
	started chan struct{}
}

func (c *behaviorObservedConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(p)
}
func TestBehaviorFluentIdleConnectionCancellation(t *testing.T) {
	r := behaviorFluentReceiver()
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	observed := &behaviorObservedConn{Conn: a, started: make(chan struct{})}
	go func() { defer close(done); r.decodeMsg(ctx, observed) }()
	<-observed.started
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("idle decoder did not observe cancellation")
		a.Close()
		<-done
	}
}
func TestBehaviorFluentListenerFailureDoesNotPanic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	r := behaviorFluentReceiver()
	r.Addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan interface{}, 1)
	go func() { defer func() { done <- recover() }(); r.Run(ctx) }()
	select {
	case p := <-done:
		if p != nil {
			t.Errorf("bind error panicked instead of returning/retrying: %v", p)
		}
	case <-time.After(50 * time.Millisecond):
		cancel()
		select {
		case p := <-done:
			if p != nil {
				t.Error(p)
			}
		case <-time.After(time.Second):
			t.Error("listener retries ignore cancellation")
		}
	}
}
func TestBehaviorFluentConcatenationIsolatesTags(t *testing.T) {
	r := behaviorFluentReceiver()
	r.SetMsgPool(&sync.Pool{})
	cfg := &concatCfg{msgKey: "log", identifierKey: "container", headRegexp: regexp.MustCompile("^HEAD")}
	r.concatTagCfg = map[string]*concatCfg{"a": cfg, "b": cfg}
	r.ConcatMaxLen = 1000
	out := make(chan *library.FluentMsg, 8)
	r.SetAsyncOutChan(out)
	in := make(chan *library.FluentMsg, 4)
	a := &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"log": "HEAD A", "container": "same"}}
	b := &library.FluentMsg{Tag: "b", Message: map[string]interface{}{"log": "orphan B", "container": "same"}}
	in <- a
	in <- b
	close(in)
	r.runConcator(context.Background(), 0, in)
	if len(out) != 2 {
		t.Fatalf("cross-tag concatenation merged distinct records: outputs=%d", len(out))
	}
	got := map[string]string{}
	for len(out) > 0 {
		m := <-out
		got[m.Tag] = fmt.Sprint(m.Message["log"])
	}
	if !reflect.DeepEqual(got, map[string]string{"a": fmt.Sprint([]byte("HEAD A")), "b": fmt.Sprint([]byte("orphan B"))}) {
		t.Fatal(got)
	}
}

// Seed corpus is intentionally bounded; arbitrary msgpack array lengths are not allocated here.
func FuzzBehaviorKafkaJSONObject(f *testing.F) {
	for _, s := range []string{`{}`, `null`, `{"tag":"a"}`, `{`, `[]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 8192 {
			t.Skip()
		}
		r := NewKafkaRecv(&KafkaCfg{Tag: "logs", TagKey: "origin", IsJSONFormat: true})
		r.SetMsgPool(behaviorDirtyPool())
		r.SetCounter(utils.NewCounter())
		m, err := r.parse2Msg(&kafka.KafkaMsg{Message: []byte(s)})
		if err == nil && (m == nil || m.Message == nil || len(m.ExtIds) != 0) {
			t.Fatal("invalid success or stale IDs")
		}
	})
}
