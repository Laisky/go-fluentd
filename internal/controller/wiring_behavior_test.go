package controller

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
	"gofluentd/library"
)

func behaviorSettings(t *testing.T, key string, value interface{}) {
	t.Helper()
	old := utils.Settings.Get(key)
	utils.Settings.Set(key, value)
	t.Cleanup(func() { utils.Settings.Set(key, old) })
}
func TestBehaviorConfigurationSelectsReceiversAndSenders(t *testing.T) {
	oldServer := server
	server = gin.New()
	defer func() { server = oldServer }()
	active := []string{"test"}
	behaviorSettings(t, "settings.acceptor.recvs.plugins", map[string]interface{}{
		"fluent":   map[string]interface{}{"type": "fluentd", "active_env": active, "addr": "127.0.0.1:0"},
		"syslog":   map[string]interface{}{"type": "rsyslog", "active_env": active, "tag": "syslog.{env}"},
		"http":     map[string]interface{}{"type": "http", "active_env": active, "path": "/input/:env", "ts_regexp": "^x", "max_body_byte": 100},
		"kafka":    map[string]interface{}{"type": "kafka", "active_env": active, "is_json_format": true, "brokers": map[string]interface{}{"test": []string{"127.0.0.1:1"}}, "topics": map[string]interface{}{"test": "logs"}, "rewrite_tag": "rewritten"},
		"inactive": map[string]interface{}{"type": "not-supported", "active_env": []string{"other"}},
	})
	c := NewControllor()
	receivers := c.initRecvs("test")
	if len(receivers) != 4 {
		t.Fatal("receiver activation")
	}
	seen := map[string]bool{}
	for _, r := range receivers {
		seen[r.GetName()] = true
		switch v := r.(type) {
		case *recvs.KafkaRecv:
			if v.RewriteTag != "rewritten.test" || v.Topics[0] != "logs" {
				t.Fatal("Kafka wiring")
			}
		case *recvs.RsyslogRecv:
			if v.Tag != "syslog.test" {
				t.Fatal("syslog environment")
			}
		}
	}
	if seen["inactive"] {
		t.Fatal("inactive receiver started")
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/input/sit", nil))
	if w.Code != 200 || w.Body.String() != "HTTPrecv" {
		t.Fatal("HTTP route not registered")
	}
	behaviorSettings(t, "settings.producer.plugins", map[string]interface{}{
		"fluent":   map[string]interface{}{"type": "fluentd", "active_env": active, "addr": "127.0.0.1:1", "tags": []string{"logs.test"}},
		"kafka":    map[string]interface{}{"type": "kafka", "active_env": active, "brokers": map[string]interface{}{"test": []string{"127.0.0.1:1"}}, "topic": map[string]interface{}{"test": "logs"}, "tags": []string{"logs"}},
		"es":       map[string]interface{}{"type": "es", "active_env": active, "addr": "http://example.invalid", "indices": map[string]interface{}{"logs.{env}": "index-{env}"}, "tags": []string{"logs.{env}"}},
		"stdout":   map[string]interface{}{"type": "stdout", "active_env": active, "tags": []string{"logs.{env}"}, "is_commit": true},
		"inactive": map[string]interface{}{"type": "not-supported", "active_env": []string{"other"}},
	})
	ss := c.initSenders("test")
	if len(ss) != 4 {
		t.Fatal("sender activation")
	}
	for _, s := range ss {
		if !s.IsTagSupported("logs.test") || s.IsTagSupported("other") {
			t.Fatalf("sender tags: %s", s.GetName())
		}
	}
	in, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
	p := c.initProducer("test", in, commit, ss)
	if p.InChan != in || p.CommitChan != commit {
		t.Fatal("producer channels")
	}
	if !StringListContains(active, "test") || StringListContains(active, "other") {
		t.Fatal("activation lookup")
	}
}
func TestBehaviorConfiguredFilterAndRoutingChain(t *testing.T) {
	behaviorSettings(t, "settings.acceptor_filters.plugins", map[string]interface{}{"default": map[string]interface{}{"remove_empty_tag": true, "accept_tags": []string{"logs.{env}"}}, "spark": map[string]interface{}{"type": "spark", "ignore_regex": "^ignore", "identifier": "container", "msg_key": "log"}, "spring": map[string]interface{}{"type": "spring", "rules": []interface{}{map[interface{}]interface{}{"regexp": "^INFO", "new_tag": "logs.{env}"}}}})
	behaviorSettings(t, "settings.tag_filters.plugins", map[string]interface{}{"parse": map[string]interface{}{"type": "parser", "tags": []string{"logs.{env}"}, "nfork": 1, "parse_json_key": "args", "must_include": "payload"}})
	behaviorSettings(t, "settings.post_filters.plugins", map[string]interface{}{"default": map[string]interface{}{"max_len": 100}, "fields": map[string]interface{}{"type": "fields", "tags": []string{"logs"}, "exclude_fields": []string{"secret"}, "new_fields": map[string]string{"summary": "${payload}"}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewControllor()
	acks := make(chan *library.FluentMsg, 8)
	ap, err := c.initAcceptorPipeline(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw := make(chan *library.FluentMsg, 8)
	accepted, _ := ap.Wrap(ctx, raw, make(chan *library.FluentMsg))
	tp := c.initTagPipeline(ctx, "test", acks)
	d := c.initDispatcher(ctx, accepted, tp)
	post := c.initPostPipeline("test", acks)
	out := post.Wrap(ctx, d.GetOutChan())
	m := &library.FluentMsg{Tag: "logs.test", ID: 1, Message: map[string]interface{}{"args": `{"payload":"ok","secret":"drop"}`}}
	raw <- m
	got := behaviorReceive(t, out)
	if got != m || got.Message["summary"] != "ok" || got.Message["secret"] != nil || len(acks) != 0 {
		t.Fatalf("configured chain: %+v", got)
	}
}
func TestBehaviorConfigurationCreatesJournalAndAcceptor(t *testing.T) {
	behaviorSettings(t, "settings.journal.buf_dir_path", t.TempDir())
	behaviorSettings(t, "settings.journal.buf_file_bytes", 8192)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewControllor()
	j := c.initJournal(ctx)
	a := c.initAcceptor(ctx, j, nil)
	if a.Journal != j || a.MsgPool != c.msgPool {
		t.Fatal("acceptor dependency wiring")
	}
	data := map[string]interface{}{}
	j.ConvertMsg2Buf(&library.FluentMsg{Tag: "logs", ID: 9, Message: map[string]interface{}{"x": "y"}}, &data)
	if data["tag"] != "logs" || data["id"] != int64(9) {
		t.Fatal("journal conversion")
	}
	// Starting an already-cancelled heartbeat must not leave a sleeping worker.
	cancel()
	done := make(chan struct{})
	go func() { c.runHeartBeat(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat cancellation")
	}
}

var _ senders.SenderItf = (*senders.StdoutSender)(nil)
