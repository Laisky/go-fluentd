package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
	"gofluentd/library"
)

func TestComponentEventReceiverYAML(t *testing.T) {
	if !componentSubprocess(t) {
		return
	}
	viper.Reset()
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader(`settings:
  acceptor:
    recvs:
      plugins:
        events:
          type: http_events
          active_env: [test]
          format: ndjson
          path: /events
          tag: source.{env}
          bearer_token: test-secret
          max_body_byte: 64
          max_records: 1
          ack_timeout_sec: 1
        disabled:
          type: http_events
          active_env: [prod]
          format: invalid
`)); err != nil {
		t.Fatal(err)
	}
	server = gin.New()
	c := NewControllor()
	rs := c.initRecvs("test")
	if len(rs) != 1 || rs[0].GetName() != "events" {
		t.Fatalf("registered receivers: %v", rs)
	}
	r, ok := rs[0].(*recvs.HTTPEventsRecv)
	if !ok {
		t.Fatal("wrong receiver")
	}
	out := make(chan *library.FluentMsg, 2)
	r.SetSyncOutChan(out)
	r.SetMsgPool(c.msgPool)
	r.SetCounter(utils.NewCounter())
	request := func(body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-ndjson")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}
	if got := request("{}\n", "wrong").Code; got != 401 {
		t.Fatalf("auth=%d", got)
	}
	if got := request("{}\n{}\n", "test-secret").Code; got != 400 {
		t.Fatalf("record limit=%d", got)
	}
	if got := request(strings.Repeat("x", 65), "test-secret").Code; got != 413 {
		t.Fatalf("byte limit=%d", got)
	}
	if len(out) != 0 {
		t.Fatal("rejected requests published")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request("{\"msgid\":\"caller\"}\n", "test-secret") }()
	m := componentRecv(t, out)
	if m.Tag != "source.test" || m.SourceFormat != "ndjson" || m.DurableAck == nil {
		t.Fatalf("configuration lost: %+v", m)
	}
	m.CompleteAcceptance(nil)
	if got := (<-done).Code; got != 204 {
		t.Fatalf("response=%d", got)
	}
	if _, err := c.initHTTPEventsRecv("test", "events"); err == nil {
		t.Fatal("duplicate route accepted")
	}
	viper.Set("dry", true)
	if _, err := c.initHTTPEventsRecv("test", "events"); err == nil {
		t.Fatal("dry reliable endpoint accepted")
	}
}

// Public receiver -> actual controller writer -> reopen -> producer. No receipt
// success is fabricated here; it comes from the real file/directory Sync path.
func TestRegressionEventEnvelopeSurvivesJournalAndProducer(t *testing.T) {
	for _, gz := range []bool{false, true} {
		for _, group := range []int{1, 64} {
			t.Run(fmt.Sprintf("gzip=%v/group=%d", gz, group), func(t *testing.T) {
				dir := t.TempDir()
				backend := upgradeBackend(t, dir, gz)
				jc := upgradeController(backend, group)
				e := gin.New()
				r, err := recvs.NewHTTPEventsRecv(recvs.HTTPEventsRecvCfg{HTTPSrv: e, Name: "events", Path: "/events", Tag: "source", Format: "ndjson", AckTimeout: 3 * time.Second})
				if err != nil {
					t.Fatal(err)
				}
				r.SetMsgPool(jc.MsgPool)
				r.SetCounter(utils.NewCounter())
				in := make(chan *library.FluentMsg, 8)
				r.SetSyncOutChan(in)
				done := make(chan struct{})
				go func() {
					defer close(done)
					jc.runDataWriter(context.Background(), "source", backend, in, utils.NewCounter())
				}()
				req := httptest.NewRequest("POST", "/events", strings.NewReader("{\"msgid\":\"caller\",\"n\":18446744073709551615,\"text\":\"世界\"}\n"))
				req.Header.Set("Content-Type", "application/x-ndjson")
				w := httptest.NewRecorder()
				e.ServeHTTP(w, req)
				if w.Code != 204 {
					t.Fatalf("real durable response: %d", w.Code)
				}
				original := componentRecv(t, jc.outChan)
				// Exercise marker reset on the writer's reused journal wrapper.
				receipt := make(chan error, 1)
				in <- &library.FluentMsg{ID: 100, Tag: "source", Message: map[string]interface{}{"message": "legacy"}, DurableAck: receipt}
				if err := <-receipt; err != nil {
					t.Fatal(err)
				}
				componentRecv(t, jc.outChan)
				close(in)
				<-done
				backend.Close()
				backend = upgradeBackend(t, dir, gz)
				jc = upgradeController(backend, group)
				recovered := make(chan *library.FluentMsg, 8)
				if _, err := jc.ProcessLegacyMsg(recovered); err != nil {
					t.Fatal(err)
				}
				if len(recovered) != 2 {
					t.Fatalf("recovered count=%d", len(recovered))
				}
				for range 2 {
					m := componentRecv(t, recovered)
					if m.ID == 100 {
						if m.SourceFormat != "" {
							t.Fatal("format leaked into legacy log")
						}
						continue
					}
					if m.SourceFormat != "ndjson" || !reflect.DeepEqual(m.Message, original.Message) {
						t.Fatalf("recovered envelope changed: %+v", m)
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					s := newComponentSender("events")
					s.SetSupportedTags([]string{"source"})
					pin, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
					p, err := NewProducer(&ProducerCfg{InChan: pin, CommitChan: commit, MsgPool: &sync.Pool{}, NFork: 1, DistributeKey: "node"}, s)
					if err != nil {
						t.Fatal(err)
					}
					p.Run(ctx)
					pin <- m
					got := componentRecv(t, s.in)
					if got.Message["msgid"] != "caller" || !reflect.DeepEqual(got.Message, original.Message) || got.DeliveryID == "" {
						t.Fatalf("producer changed event: %+v", got)
					}
					s.good <- got
					componentRecv(t, commit)
					close(pin)
					cancel()
				}
			})
		}
	}
}

func TestComponentEventSenderYAML(t *testing.T) {
	if !componentSubprocess(t) {
		return
	}
	viper.Reset()
	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader(`settings:
  producer:
    sender_inchan_size: 4
    plugins:
      events:
        type: http_events
        active_env: [test]
        format: cloudevents
        mode: binary
        addr: https://example.test/events
        tags: ["source.{env}"]
        max_attempts: 1
        max_body_byte: 1
      disabled:
        type: http_events
        active_env: [prod]
        addr: bad
`)); err != nil {
		t.Fatal(err)
	}
	c := NewControllor()
	ss := c.initSenders("test")
	if len(ss) != 1 || ss[0].GetName() != "events" || !ss[0].IsTagSupported("source.test") || ss[0].DiscardWhenBlocked() {
		t.Fatal("sender configuration")
	}
	sender, ok := ss[0].(*senders.HTTPEventsSender)
	if !ok {
		t.Fatal("wrong sender")
	}
	// The configured size limit must fail before any request to the remote URL.
	m := &library.FluentMsg{Message: map[string]interface{}{"specversion": "1.0", "id": "a", "source": "/s", "type": "t", "data": "payload"}}
	if err := sender.Send(context.Background(), []*library.FluentMsg{m}); err == nil {
		t.Fatal("configured output bound ignored")
	}
	viper.Set("settings.producer.plugins.events.is_discard_when_blocked", true)
	if _, err := c.initHTTPEventsSender("test", "events"); err == nil {
		t.Fatal("lossy config accepted")
	}
	viper.Set("settings.producer.plugins.events.is_discard_when_blocked", false)
	viper.Set("dry", true)
	if _, err := c.initHTTPEventsSender("test", "events"); err == nil {
		t.Fatal("dry config accepted")
	}
}
