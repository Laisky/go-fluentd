package recvs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/tinylib/msgp/msgp"
	"gofluentd/internal/recvs/fluentwire"
	"gofluentd/library"
)

// FluentIngressCfg applies to each listener. Zero selects a finite default;
// negative values are configuration errors, never a request to disable a limit.
// MaxValues bounds decoded metadata separately from encoded payload bytes.
type FluentIngressCfg struct {
	MaxConnections       int
	IdleTimeout          time.Duration
	FrameTimeout         time.Duration
	MaxFrameBytes        int
	MaxValueBytes        int
	MaxContainerElements int
	MaxValues            int
	MaxDepth             int
}

func (c *FluentIngressCfg) setDefaultsAndValidate() error {
	if c.MaxConnections == 0 {
		c.MaxConnections = 32
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 30 * time.Second
	}
	if c.FrameTimeout == 0 {
		c.FrameTimeout = 10 * time.Second
	}
	if c.MaxFrameBytes == 0 {
		c.MaxFrameBytes = 8 << 20
	}
	if c.MaxValueBytes == 0 {
		c.MaxValueBytes = c.MaxFrameBytes
	}
	if c.MaxContainerElements == 0 {
		c.MaxContainerElements = 4096
	}
	if c.MaxValues == 0 {
		c.MaxValues = 65536
	}
	if c.MaxDepth == 0 {
		c.MaxDepth = 32
	}
	if c.MaxConnections < 1 || c.IdleTimeout < 0 || c.FrameTimeout < 0 || c.MaxContainerElements < 1 || uint64(c.MaxContainerElements) > uint64(^uint32(0)) || c.MaxValues < 1 {
		return fmt.Errorf("invalid Fluent ingress connection, timeout or element limits")
	}
	return c.wireLimits().Validate()
}

func (c FluentIngressCfg) wireLimits() fluentwire.Limits {
	return fluentwire.Limits{MaxFrameBytes: c.MaxFrameBytes, MaxValueBytes: c.MaxValueBytes, MaxContainerElements: uint32(c.MaxContainerElements), MaxValues: uint64(c.MaxValues), MaxDepth: c.MaxDepth}
}

// acceptFluent uses the Run-owned semaphore across listener rebinds. Rejected
// sockets never get a worker, decoder, or cancellation callback.
func (r *FluentdRecv) acceptFluent(ctx context.Context, ln net.Listener, slots chan struct{}, workers *sync.WaitGroup) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			conn.Close()
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		workers.Add(1)
		go func() { defer workers.Done(); defer func() { <-slots }(); r.decodeMsg(ctx, conn) }()
	}
}

// readFluentFrame allows an idle connection to wait for its first byte, then
// installs a separate absolute deadline for the whole frame. Slow trickles do
// not extend it. Downstream backpressure is intentionally not a read timeout.
func (r *FluentdRecv) readFluentFrame(conn net.Conn, reader *bufio.Reader, v *library.FluentBatchMsg) (*fluentwire.Budget, error) {
	// Release references to the previous frame before waiting on an idle peer.
	clear(*v)
	if err := setFluentReadDeadline(conn, time.Now().Add(r.Ingress.IdleTimeout)); err != nil {
		return nil, err
	}
	if _, err := reader.Peek(1); err != nil {
		return nil, err
	}
	if err := setFluentReadDeadline(conn, time.Now().Add(r.Ingress.FrameTimeout)); err != nil {
		return nil, err
	}
	b, err := fluentwire.NewBudget(r.Ingress.wireLimits())
	if err != nil {
		return nil, err
	}
	err = decodeFluentFrame(reader, b, v, 2, 4)
	return b, err
}

// decodeFluentFrame keeps the generated codec off unchecked network bytes.
// The frame scanner validates every nested length before this codec runs.
// Retaining the streaming codec preserves existing extension/scalar semantics.
func decodeFluentFrame(reader *bufio.Reader, b *fluentwire.Budget, v *library.FluentBatchMsg, minFields, maxFields uint32) error {
	wire, err := b.ReadFrame(reader, minFields, maxFields)
	if err != nil {
		return err
	}
	return msgp.Decode(bytes.NewReader(wire), v)
}

// A peer can close after filling the reader but before the deadline update.
// Closed transports cannot stall another read, so preserve already buffered
// frames. Other deadline failures still fail closed instead of disabling limits.
func setFluentReadDeadline(conn net.Conn, deadline time.Time) error {
	err := conn.SetReadDeadline(deadline)
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func isFluentIngressLimit(err error) bool { return errors.Is(err, fluentwire.ErrLimit) }
