package senders

import (
	"bytes"
	"context"
	"fmt"
	"github.com/tinylib/msgp/msgp"
	"gofluentd/library"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type regressionConn struct {
	write  func([]byte) (int, error)
	closed atomic.Bool
}

func (c *regressionConn) Read([]byte) (int, error) { return 0, io.EOF }
func (c *regressionConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if c.write != nil {
		return c.write(p)
	}
	return len(p), nil
}
func (c *regressionConn) Close() error                   { c.closed.Store(true); return nil }
func (*regressionConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*regressionConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*regressionConn) SetDeadline(time.Time) error      { return nil }
func (*regressionConn) SetReadDeadline(time.Time) error  { return nil }
func (*regressionConn) SetWriteDeadline(time.Time) error { return nil }

func regressionFluent() (*FluentSender, chan *library.FluentMsg, chan *library.FluentMsg) {
	s := NewFluentSender(&FluentSenderCfg{Name: "test", Addr: "unused:1", BatchSize: 1, InChanSize: 1024, NFork: 8, MaxWait: time.Hour})
	ok, fail := make(chan *library.FluentMsg, 1024), make(chan *library.FluentMsg, 1024)
	s.SetSuccessedChan(ok)
	s.SetFailedChan(fail)
	return s, ok, fail
}
func regressionChild(s *FluentSender, ctx context.Context) (chan *library.FluentMsg, <-chan struct{}) {
	in, done := make(chan *library.FluentMsg, 16), make(chan struct{})
	go func() { defer close(done); s.spawnChildSenderForTag(ctx, "logs", in) }()
	return in, done
}
func regressionMessage() *library.FluentMsg {
	return &library.FluentMsg{ID: 42, Tag: "logs", Message: map[string]interface{}{"message": "hello"}}
}

func TestRegressionFluentFailedFlushNotAcknowledged(t *testing.T) {
	s, ok, fail := regressionFluent()
	var writes atomic.Int64
	s.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return &regressionConn{write: func([]byte) (int, error) { writes.Add(1); return 0, io.ErrClosedPipe }}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, _ := regressionChild(s, ctx)
	in <- regressionMessage()
	select {
	case <-ok:
		t.Error("failed TCP Flush was acknowledged as successful")
	case <-fail:
	case <-time.After(2 * time.Second):
		t.Error("missing delivery outcome")
	}
	if writes.Load() == 0 {
		t.Error("test did not exercise an actual write")
	}
}
func TestRegressionFluentSuccess(t *testing.T) {
	s, ok, fail := regressionFluent()
	s.dialContext = func(context.Context, string, string) (net.Conn, error) { return &regressionConn{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, _ := regressionChild(s, ctx)
	want := regressionMessage()
	in <- want
	select {
	case got := <-ok:
		if got != want {
			t.Error("wrong message acknowledged")
		}
	case <-fail:
		t.Error("successful write failed")
	case <-time.After(time.Second):
		t.Error("missing success")
	}
}
func TestRegressionFluentConnectionClosedOnCancel(t *testing.T) {
	s, ok, _ := regressionFluent()
	conn := &regressionConn{}
	s.dialContext = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, done := regressionChild(s, ctx)
	in <- regressionMessage()
	select {
	case <-ok:
	case <-time.After(time.Second):
		t.Fatal("missing success")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sender did not stop")
	}
	if !conn.closed.Load() {
		t.Error("TCP connection leaked after cancellation")
	}
}
func TestRegressionFluentReconnectCancellation(t *testing.T) {
	s, _, _ := regressionFluent()
	started := make(chan struct{})
	var once sync.Once
	s.dialContext = func(context.Context, string, string) (net.Conn, error) {
		once.Do(func() { close(started) })
		return nil, io.ErrClosedPipe
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, done := regressionChild(s, ctx)
	in <- regressionMessage()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Error("reconnect loop ignored cancellation")
	}
}
func TestRegressionFluentInputCloseFlushesPending(t *testing.T) {
	s, ok, fail := regressionFluent()
	s.BatchSize = 10
	s.dialContext = func(context.Context, string, string) (net.Conn, error) { return &regressionConn{}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, done := regressionChild(s, ctx)
	for i := 0; i < 3; i++ {
		in <- regressionMessage()
	}
	close(in)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sender did not drain closed input")
	}
	if len(ok) != 3 || len(fail) != 0 {
		t.Errorf("closed input: successes=%d failures=%d, want 3 successes", len(ok), len(fail))
	}
}
func TestRegressionFluentPartialWriteReconnects(t *testing.T) {
	s, ok, fail := regressionFluent()
	var dials atomic.Int64
	s.dialContext = func(context.Context, string, string) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return &regressionConn{write: func(p []byte) (int, error) { return len(p) / 2, io.ErrUnexpectedEOF }}, nil
		}
		return &regressionConn{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, _ := regressionChild(s, ctx)
	in <- regressionMessage()
	select {
	case <-ok:
	case <-fail:
		t.Error("fresh connection did not recover")
	case <-time.After(time.Second):
		t.Fatal("no outcome")
	}
	if dials.Load() < 2 {
		t.Error("partial write was acknowledged without reconnecting and retrying")
	}
}
func TestRegressionFluentConcurrentTagRouting(t *testing.T) {
	s, ok, fail := regressionFluent()
	errs := make(chan error, 1024)
	s.dialContext = func(context.Context, string, string) (net.Conn, error) {
		return &regressionConn{write: func(p []byte) (int, error) {
			var packet library.FluentBatchMsg
			if err := packet.DecodeMsg(msgp.NewReader(bytes.NewReader(p))); err != nil {
				errs <- err
				return len(p), nil
			}
			records, valid := packet[1].([]interface{})
			if !valid {
				errs <- fmt.Errorf("bad records type %T", packet[1])
				return len(p), nil
			}
			for _, r := range records {
				entry := r.([]interface{})
				msg := entry[1].(map[string]interface{})
				if packet[0] != msg["sourceTag"] {
					errs <- fmt.Errorf("message routed to tag %v, want %v", packet[0], msg["sourceTag"])
				}
			}
			return len(p), nil
		}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := s.Spawn(ctx)
	const n = 256
	for i := 0; i < n; i++ {
		tag := fmt.Sprintf("tag-%d", i%8)
		in <- &library.FluentMsg{ID: int64(i + 1), Tag: tag, Message: map[string]interface{}{"sourceTag": tag}}
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for i := 0; i < n; i++ {
		select {
		case <-ok:
		case <-fail:
			t.Error("unexpected failed delivery")
		case <-deadline.C:
			t.Fatal("missing delivery outcomes")
		}
	}
	close(in)
	cancel()
	for len(errs) > 0 {
		t.Error(<-errs)
	}
}
