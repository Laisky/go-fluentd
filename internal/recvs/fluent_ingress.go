package recvs

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/Laisky/zap"
	"io"
	"net"
	"sync"
	"time"
	"unicode/utf8"

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

// durableFluentEntry retains one validated record and its original decoded timestamp.
type durableFluentEntry struct {
	timestamp interface{}
	message   map[string]interface{}
}

const maximumFluentChunkBytes = 512

// durableFluentFrame validates every entry before any pipeline ownership transfer.
// Options retain their decoded values; gzip is permitted only for bounded packed input.
func (r *FluentdRecv) durableFluentFrame(v library.FluentBatchMsg, budget *fluentwire.Budget) (tag string, entries []durableFluentEntry, options map[string]interface{}, chunk interface{}, err error) {
	if len(v) < 2 {
		err = errors.New("invalid Fluent frame")
		return
	}
	switch value := v[0].(type) {
	case string:
		tag = value
	case []byte:
		tag = string(value)
	default:
		err = errors.New("invalid Fluent tag")
		return
	}
	if tag == "" || !utf8.ValidString(tag) {
		err = errors.New("invalid Fluent tag")
		return
	}
	optionIndex := 3
	switch v[1].(type) {
	case []interface{}, []byte:
		optionIndex = 2
	}
	if len(v) != optionIndex && len(v) != optionIndex+1 {
		err = errors.New("invalid Fluent frame shape")
		return
	}
	if len(v) == optionIndex+1 {
		var ok bool
		options, ok = v[optionIndex].(map[string]interface{})
		if !ok || options == nil {
			err = errors.New("invalid Fluent options")
			return
		}
	}
	if value, ok := options["chunk"]; ok {
		switch value := value.(type) {
		case string:
			if len(value) == 0 || len(value) > maximumFluentChunkBytes || !utf8.ValidString(value) {
				err = errors.New("invalid Fluent chunk")
				return
			}
			chunk = value
		case []byte:
			if len(value) == 0 || len(value) > maximumFluentChunkBytes {
				err = errors.New("invalid Fluent chunk")
				return
			}
			chunk = bytes.Clone(value)
		default:
			err = errors.New("invalid Fluent chunk")
			return
		}
	}
	if chunk == nil && !r.BestEffort {
		err = errors.New("durable Fluent requires a chunk")
		return
	}
	compression := ""
	if value, ok := options["compressed"]; ok {
		compression, ok = value.(string)
		if !ok || compression != "gzip" && compression != "text" {
			err = errors.New("unsupported Fluent compression")
			return
		}
	}
	add := func(timestamp, record interface{}) bool {
		if !validDurableFluentTimestamp(timestamp) {
			err = errors.New("invalid Fluent timestamp")
			return false
		}
		message, ok := record.(map[string]interface{})
		if !ok || message == nil {
			err = errors.New("invalid Fluent record")
			return false
		}
		if len(entries) >= r.Ingress.MaxContainerElements {
			err = errors.New("Fluent record count limit")
			return false
		}
		entries = append(entries, durableFluentEntry{timestamp: timestamp, message: message})
		return true
	}
	switch body := v[1].(type) {
	case []interface{}:
		if compression != "" && compression != "text" {
			err = errors.New("compression requires packed Fluent input")
			return
		}
		for _, item := range body {
			entry, ok := item.([]interface{})
			if !ok || len(entry) != 2 {
				err = errors.New("invalid Fluent forward entry")
				return
			}
			if !add(entry[0], entry[1]) {
				return
			}
		}
	case []byte:
		packed := body
		if compression == "gzip" {
			gz, e := gzip.NewReader(bytes.NewReader(body))
			if e != nil {
				err = errors.New("invalid Fluent gzip")
				return
			}
			expanded, e := io.ReadAll(io.LimitReader(gz, int64(r.Ingress.MaxFrameBytes)+1))
			ce := gz.Close()
			if e != nil || ce != nil || len(expanded) > r.Ingress.MaxFrameBytes {
				err = errors.New("invalid or oversized Fluent gzip")
				return
			}
			packed = expanded
		}
		reader := bufio.NewReader(bytes.NewReader(packed))
		for {
			var entry library.FluentBatchMsg
			e := decodeFluentFrame(reader, budget, &entry, 2, 2)
			if e == io.EOF {
				break
			}
			if e != nil {
				err = errors.New("invalid or oversized Fluent packed entry")
				return
			}
			if !add(entry[0], entry[1]) {
				return
			}
		}
	default:
		if compression != "" && compression != "text" {
			err = errors.New("compression requires packed Fluent input")
			return
		}
		if !add(v[1], v[2]) {
			return
		}
	}
	if len(entries) == 0 {
		err = errors.New("empty Fluent batch")
		return
	}
	return
}

// decodeDurableFluent sends a chunk ACK only after all bounded, validated entries pass Sync.
// Failure may leave a durable prefix; no ACK is sent and sender retry may duplicate it.
func (r *FluentdRecv) decodeDurableFluent(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for ctx.Err() == nil {
		var frame library.FluentBatchMsg
		budget, err := r.readFluentFrame(conn, reader, &frame)
		if err != nil {
			return
		}
		tag, entries, options, chunk, err := r.durableFluentFrame(frame, budget)
		if err != nil {
			r.logger.Warn("durable Fluent frame rejected", zap.Error(err))
			return
		}
		admissionCtx, cancel := context.WithTimeout(ctx, r.Ingress.FrameTimeout)
		for _, entry := range entries {
			msg := r.newMsg()
			msg.ID, msg.Tag, msg.Message = r.counter.Count(), tag, entry.message
			msg.SourceMetadata = map[string]interface{}{"protocol": "forward", "timestamp": entry.timestamp, "options": options}
			if err = r.SubmitDurable(admissionCtx, msg); err != nil {
				break
			}
		}
		cancel()
		if err != nil {
			r.logger.Warn("durable Fluent admission refused", zap.Error(err))
			return
		}
		if chunk == nil {
			continue
		}
		if err = conn.SetWriteDeadline(time.Now().Add(r.Ingress.FrameTimeout)); err != nil {
			return
		}
		writer := msgp.NewWriter(conn)
		if err = writer.WriteMapHeader(1); err != nil {
			return
		}
		if err = writer.WriteString("ack"); err != nil {
			return
		}
		if err = writer.WriteIntf(chunk); err != nil {
			return
		}
		if err = writer.Flush(); err != nil {
			return
		}
	}
}

// validDurableFluentTimestamp accepts integer seconds or the exact Fluent EventTime extension.
func validDurableFluentTimestamp(value interface{}) bool {
	switch v := value.(type) {
	case int:
		return v >= 0
	case int64:
		return v >= 0
	case int32:
		return v >= 0
	case uint, uint64, uint32, uint16, uint8:
		return true
	case *msgp.RawExtension:
		return v != nil && v.Type == 0 && len(v.Data) == 8 && binary.BigEndian.Uint32(v.Data[4:]) < 1000000000
	default:
		return false
	}
}
