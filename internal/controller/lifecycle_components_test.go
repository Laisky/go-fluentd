package controller

import (
	"context"
	"github.com/gin-gonic/gin"
	"gofluentd/library"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// A subprocess turns fatal worker panics into an assertion rather than killing
// unrelated component tests. The child exercises the same production entry point.
func componentSubprocess(t *testing.T) bool {
	t.Helper()
	if os.Getenv("GO_FLUENTD_COMPONENT_CHILD") == t.Name() {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=8s")
	cmd.Env = append(os.Environ(), "GO_FLUENTD_COMPONENT_CHILD="+t.Name())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("component subprocess failed: %v\n%s", err, output)
	}
	return false
}
func TestComponentProducerBlockedCacheDoesNotPanic(t *testing.T) {
	if !componentSubprocess(t) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newComponentSender("blocked")
	s.IsDiscardWhenBlocked = true
	in, commit := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	p, err := NewProducer(&ProducerCfg{NFork: 1, InChan: in, CommitChan: commit, MsgPool: &sync.Pool{}}, s)
	if err != nil {
		t.Fatal(err)
	}
	p.tag2SenderCaches.Store("logs", []*senderCache{{sender: s, inchan: make(chan *library.FluentMsg)}})
	p.tag2NSender.Store("logs", 1)
	p.Run(ctx)
	in <- &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{}}
	in <- &library.FluentMsg{Tag: "unsupported", ID: 2, Message: map[string]interface{}{}}
	ids := map[int64]bool{}
	for i := 0; i < 2; i++ {
		ids[componentRecv(t, commit).ID] = true
	}
	if !ids[1] || !ids[2] {
		t.Fatal("terminal discard did not finish both messages")
	}
	close(in)
}
func TestComponentHTTPServerCancellationDrainsRequests(t *testing.T) {
	if !componentSubprocess(t) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	reservation.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	server.GET("/component-slow", func(c *gin.Context) { close(entered); <-release; c.String(200, "done") })
	done := make(chan struct{})
	go func() { defer close(done); RunServer(ctx, addr) }()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/health")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP server did not start")
		}
		time.Sleep(time.Millisecond)
	}
	requestDone := make(chan error, 1)
	go func() {
		resp, err := client.Get("http://" + addr + "/component-slow")
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		requestDone <- err
	}()
	<-entered
	cancel()
	select {
	case <-done:
		t.Error("shutdown returned before active request drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("shutdown hung")
	}
	time.Sleep(10 * time.Millisecond)
}
func TestComponentJournalDefaultChannelCapacities(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := NewJournal(ctx, &JournalCfg{BufDirPath: t.TempDir(), MsgPool: &sync.Pool{}, BufSizeBytes: 8192})
	if cap(j.outChan) != j.JournalOutChanLen || cap(j.commitChan) != j.CommitIDChanLen {
		t.Fatalf("journal channel capacity mismatch: %d/%d", cap(j.outChan), cap(j.commitChan))
	}
	if err := j.CloseTag("absent"); err == nil {
		t.Fatal("unknown close succeeded")
	}
	msg := &library.FluentMsg{ID: 42, Tag: "logs", Message: map[string]interface{}{"x": 1}}
	data := map[string]interface{}{}
	j.ConvertMsg2Buf(msg, &data)
	if data["id"] != int64(42) || data["tag"] != "logs" {
		t.Fatal(data)
	}
}
