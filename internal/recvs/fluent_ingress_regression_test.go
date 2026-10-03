package recvs

import (
	"context"
	"net"
	"testing"
	"time"
)

// This test also compiles against the vulnerable revision. The moderate count
// deliberately avoids an OOM while proving rejection must precede a body read.
func TestFluentIngressRejectsOuterHeaderBeforeBody(t *testing.T) {
	r := newComponentFluent(t)
	server, client := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.decodeMsg(ctx, server) }()
	t.Cleanup(func() { cancel(); client.Close(); server.Close(); <-done })
	if err := client.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// array32, 65,536 members: only ~1 MiB on the old decoder, not 64 GiB.
	if _, err := client.Write([]byte{0xdd, 0, 1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("FLUENT_PREVALIDATION_REGRESSION: waited for body after invalid outer header")
	}
}
