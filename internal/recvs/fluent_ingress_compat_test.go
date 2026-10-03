package recvs

import (
	"bufio"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"gofluentd/library"
)

func TestFluentIngressDrainsBufferedFramesAfterPeerClose(t *testing.T) {
	r := ingressReceiver(t, FluentIngressCfg{})
	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })
	frame := []byte{0x93, 0xa1, 't', 0, 0x80}
	wire := append(append([]byte{}, frame...), frame...)
	writeDone := make(chan error, 1)
	go func() { _, err := client.Write(wire); client.Close(); writeDone <- err }()
	reader := bufio.NewReader(server)
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Peek(len(wire)); err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	// net.Pipe reports ErrClosedPipe on deadline updates after its peer closes,
	// although both complete frames remain in reader's buffer.
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("fixture: %v", err)
	}
	var v library.FluentBatchMsg
	for i := 0; i < 2; i++ {
		if _, err := r.readFluentFrame(server, reader, &v); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(v) != 3 || v[0] != "t" {
			t.Fatalf("frame %d: %#v", i, v)
		}
	}
	if _, err := r.readFluentFrame(server, reader, &v); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
}

type failingFluentDeadlineConn struct {
	net.Conn
	err    error
	calls  int
	failAt int
}

func (c *failingFluentDeadlineConn) SetReadDeadline(d time.Time) error {
	c.calls++
	if c.calls == c.failAt {
		return c.err
	}
	return c.Conn.SetReadDeadline(d)
}

func TestFluentIngressDeadlineFailureFailsClosed(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		server, client := net.Pipe()
		t.Cleanup(func() { server.Close(); client.Close() })
		r := ingressReceiver(t, FluentIngressCfg{})
		reader := bufio.NewReader(server)
		// Preload one frame, then keep the transport open. A genuine deadline
		// failure must not be ignored merely because input has been buffered.
		written := make(chan error, 1)
		go func() { _, err := client.Write([]byte{0x93, 0xa1, 't', 0, 0x80}); written <- err }()
		if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.Peek(5); err != nil {
			t.Fatal(err)
		}
		if err := <-written; err != nil {
			t.Fatal(err)
		}
		want := errors.New("deadline unsupported")
		conn := &failingFluentDeadlineConn{Conn: server, err: want, failAt: failAt}
		var v library.FluentBatchMsg
		if _, err := r.readFluentFrame(conn, reader, &v); !errors.Is(err, want) {
			t.Fatalf("deadline %d: %v", failAt, err)
		}
		if len(v) != 0 {
			t.Fatal("record decoded despite deadline failure")
		}
	}
}
