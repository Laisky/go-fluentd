package controller

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The historical implementation panics from a different goroutine on normal
// http.ErrServerClosed. Isolate that process crash rather than recovering it
// in an unrelated goroutine or taking down the remainder of the suite.
func TestBehaviorServerShutdown(t *testing.T) {
	if os.Getenv("GO_FLUENTD_SHUTDOWN_CHILD") == "1" {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(10 * time.Millisecond); cancel() }()
		RunServer(ctx, "127.0.0.1:0")
		time.Sleep(30 * time.Millisecond)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBehaviorServerShutdown$")
	cmd.Env = append(os.Environ(), "GO_FLUENTD_SHUTDOWN_CHILD=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("normal HTTP shutdown crashed the process: %v\n%s", err, b)
	}
}

func TestBehaviorServerShutdownDrainsActiveRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "completed")
	})}
	done := make(chan error, 1)
	go func() { done <- serveHTTP(ctx, srv, ln) }()
	result := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			defer resp.Body.Close()
			var b []byte
			b, err = io.ReadAll(resp.Body)
			if err == nil && string(b) != "completed" {
				err = fmt.Errorf("incomplete response %q", b)
			}
		}
		result <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Errorf("server stopped before active request completed: %v", err)
		close(release)
		return
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish after request drained")
	}
}
