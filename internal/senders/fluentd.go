package senders

import (
	"context"
	"fmt"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"gofluentd/library"
	"gofluentd/library/log"
	"net"
	"sync"
	"time"
)

type FluentSenderCfg struct {
	Name, Addr                   string
	Tags                         []string
	BatchSize, InChanSize, NFork int
	MaxWait                      time.Duration
	IsDiscardWhenBlocked         bool
	ConcatCfg                    map[string]interface{}
}
type FluentSender struct {
	*BaseSender
	*FluentSenderCfg
	// Test seam; configure before starting workers.
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func NewFluentSender(cfg *FluentSenderCfg) *FluentSender {
	if cfg.Addr == "" {
		log.Logger.Panic("addr should not be empty")
	}
	if cfg.NFork <= 0 {
		cfg.NFork = 1
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.InChanSize < 0 {
		cfg.InChanSize = 0
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = time.Second
	}
	s := &FluentSender{BaseSender: &BaseSender{IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked}, FluentSenderCfg: cfg}
	s.SetSupportedTags(cfg.Tags)
	return s
}
func (s *FluentSender) GetName() string { return s.Name }
func (s *FluentSender) dialConnection(ctx context.Context, network, address string) (net.Conn, error) {
	if s.dialContext != nil {
		return s.dialContext(ctx, network, address)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, address)
}

// report preserves the explicit lossy configuration, but a transport error
// must never be reported as success. Cancellation must not block shutdown.
func (s *FluentSender) report(ctx context.Context, msg *library.FluentMsg, success bool) bool {
	ch := s.failedChan
	if success {
		ch = s.successedChan
	}
	select {
	case ch <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}
func (s *FluentSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	in := make(chan *library.FluentMsg, s.InChanSize)
	var children sync.Map
	var createMu sync.Mutex
	var workers sync.WaitGroup
	workers.Add(s.NFork)
	for i := 0; i < s.NFork; i++ {
		go func() {
			defer workers.Done()
			for {
				var msg *library.FluentMsg
				select {
				case <-ctx.Done():
					return
				case m, ok := <-in:
					if !ok {
						return
					}
					msg = m
				}
				// All lookup temporaries are worker-local; only the map and its
				// create lock are shared between dispatch workers.
				child, ok := children.Load(msg.Tag)
				if !ok {
					createMu.Lock()
					child, ok = children.Load(msg.Tag)
					if !ok {
						ch := make(chan *library.FluentMsg, s.InChanSize)
						child = ch
						children.Store(msg.Tag, ch)
						go s.spawnChildSenderForTag(ctx, msg.Tag, ch)
					}
					createMu.Unlock()
				}
				select {
				case child.(chan *library.FluentMsg) <- msg:
				default:
					if !s.report(ctx, msg, s.DiscardWhenBlocked()) {
						return
					}
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		children.Range(func(_, v interface{}) bool { close(v.(chan *library.FluentMsg)); return true })
	}()
	return in
}
func waitFluentRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (s *FluentSender) spawnChildSenderForTag(ctx context.Context, tag string, in chan *library.FluentMsg) {
	logger := log.Logger.With(zap.String("tag", tag), zap.String("addr", s.Addr))
	var conn net.Conn
	var encoder *library.FluentEncoder
	var stopCancel func() bool
	closeConn := func() {
		if stopCancel != nil {
			stopCancel()
			stopCancel = nil
		}
		if conn != nil {
			conn.Close()
			conn = nil
			encoder = nil
		}
	}
	defer closeConn()
	send := func(batch []*library.FluentMsg) bool {
		if utils.Settings.GetBool("dry") {
			logger.Info("dry send", zap.String("message", fmt.Sprint(batch[0].Message)))
			return true
		}
		for attempt := 0; attempt < 3; attempt++ {
			if ctx.Err() != nil {
				return false
			}
			if conn == nil {
				next, err := s.dialConnection(ctx, "tcp", s.Addr)
				if err != nil {
					logger.Warn("connect to Fluent downstream", zap.Error(err))
					if attempt < 2 && !waitFluentRetry(ctx) {
						return false
					}
					continue
				}
				conn = next
				// Capture this connection, not the variable reused on reconnect.
				stopCancel = context.AfterFunc(ctx, func() { next.Close() })
				encoder = library.NewFluentEncoder(conn)
			}
			err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err == nil {
				err = encoder.EncodeBatch(tag, batch)
			}
			if err == nil {
				err = encoder.Flush()
			}
			if err == nil {
				return true
			}
			logger.Warn("send Fluent batch", zap.Error(err))
			// A partial write leaves framing uncertain: retry on a fresh
			// connection, never append another batch to the damaged stream.
			closeConn()
		}
		return false
	}
	batch := make([]*library.FluentMsg, 0, s.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		success := send(batch)
		for i, msg := range batch {
			s.report(ctx, msg, success)
			batch[i] = nil
		}
		batch = batch[:0]
	}
	ticker := time.NewTicker(s.MaxWait)
	defer ticker.Stop()
	lastFlush := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-in:
			if !ok {
				flush()
				return
			}
			batch = append(batch, msg)
			if len(batch) < s.BatchSize && time.Since(lastFlush) < s.MaxWait {
				continue
			}
		case <-ticker.C:
			if len(batch) == 0 {
				continue
			}
		}
		lastFlush = time.Now()
		flush()
	}
}
