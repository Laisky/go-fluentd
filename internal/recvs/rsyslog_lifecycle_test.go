package recvs

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	syslog "github.com/Laisky/go-syslog"
	"github.com/Laisky/go-syslog/format"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
)

type behaviorSyslogServer struct {
	bootError error
	booted    chan struct{}
	killed    chan struct{}
	once      sync.Once
}

func (s *behaviorSyslogServer) Boot(*syslog.BLBCfg) error { close(s.booted); return s.bootError }
func (s *behaviorSyslogServer) Wait()                     { <-s.killed }
func (s *behaviorSyslogServer) Kill() error               { s.once.Do(func() { close(s.killed) }); return nil }
func behaviorSyslogRecv() *RsyslogRecv {
	r := NewRsyslogRecv(&RsyslogCfg{Name: "syslog-runtime", Tag: "logs", TagKey: "tag", TimeKey: "timestamp", NewTimeKey: "@timestamp", MsgKey: "content", NewTimeFormat: time.RFC3339})
	r.SetMsgPool(behaviorDirtyPool())
	r.SetCounter(utils.NewCounter())
	return r
}
func TestBehaviorSyslogBootFailureDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &behaviorSyslogServer{bootError: fmt.Errorf("boot refused"), booted: make(chan struct{}), killed: make(chan struct{})}
	r := behaviorSyslogRecv()
	r.newServer = func(string) (syslogServer, syslog.LogPartsChannel, error) { cancel(); return srv, nil, nil }
	done := make(chan interface{}, 1)
	go func() { defer func() { done <- recover() }(); r.run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("boot error caused panic: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed boot did not exit")
	}
	select {
	case <-srv.killed:
	default:
		t.Fatal("failed server was not closed")
	}
}
func TestBehaviorSyslogDialBackoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := behaviorSyslogRecv()
	called := make(chan struct{}, 1)
	r.newServer = func(string) (syslogServer, syslog.LogPartsChannel, error) {
		called <- struct{}{}
		return nil, nil, fmt.Errorf("bind failed")
	}
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()
	<-called
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelled receiver remains asleep in retry backoff")
	}
}
func TestBehaviorSyslogBlockedHandoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &behaviorSyslogServer{booted: make(chan struct{}), killed: make(chan struct{})}
	r := behaviorSyslogRecv()
	input := make(syslog.LogPartsChannel)
	r.SetAsyncOutChan(make(chan *library.FluentMsg))
	r.newServer = func(string) (syslogServer, syslog.LogPartsChannel, error) { return srv, input, nil }
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()
	<-srv.booted
	input <- format.LogParts{"timestamp": time.Now(), "content": "blocked"}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cancelled receiver blocked publishing to downstream")
	}
	select {
	case <-srv.killed:
	default:
		t.Fatal("server not closed")
	}
}
func TestBehaviorSyslogWireRoundTrip(t *testing.T) {
	// A real local datagram socket exercises the dependency's parser without
	// reserving/releasing a guessed TCP/UDP port or requiring a syslog daemon.
	path := filepath.Join(t.TempDir(), "syslog.sock")
	srv := syslog.NewServer()
	srv.SetFormat(syslog.Automatic)
	input := make(syslog.LogPartsChannel, 8)
	srv.SetHandler(syslog.NewChannelHandler(input))
	if err := srv.ListenUnixgram(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := behaviorSyslogRecv()
	out := make(chan *library.FluentMsg, 8)
	r.SetAsyncOutChan(out)
	r.newServer = func(string) (syslogServer, syslog.LogPartsChannel, error) { return srv, input, nil }
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()
	conn, err := net.Dial("unixgram", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, body := range []string{"one", "two"} {
		if _, err = fmt.Fprintf(conn, "<34>Oct 11 22:14:15 node app: %s\n", body); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{"one", "two"} {
		select {
		case msg := <-out:
			if msg.Tag != "logs" || msg.Message["message"] != body || msg.Message["tag"] != "logs" {
				t.Fatalf("syslog wire data: %+v", msg)
			}
		case <-time.After(time.Second):
			t.Fatal("no decoded syslog")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("syslog shutdown did not finish")
	}
}
