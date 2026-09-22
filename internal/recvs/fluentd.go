package recvs

import (
	"bytes"
	"context"
	"net"
	"regexp"
	"sync"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/cespare/xxhash"
	"github.com/tinylib/msgp/msgp"
)

const (
	defaultConcatorWait          = 3 * time.Second
	defaultConcatorCleanInterval = 1 * time.Minute
)

// FluentdRecvCfg configuration of FluentdRecv
type FluentdRecvCfg struct {
	Name,
	// Addr: like `127.0.0.1:24225;`
	Addr,
	// TagKey: set `msg.Message[TagKey] = tag`
	TagKey,
	// LBKey key to horizontal load balacing
	LBKey string

	// NFork fork concators
	NFork,
	ConcatorBufSize int
	ConcatorWait time.Duration

	// if IsRewriteTagFromTagKey, set `msg.Tag = msg.Message[OriginRewriteTagKey]`
	IsRewriteTagFromTagKey bool
	OriginRewriteTagKey    string

	ConcatMaxLen int
	ConcatCfg    map[string]interface{}
}

type concatCfg struct {
	headRegexp *regexp.Regexp
	msgKey,
	identifierKey string
}

// FluentdRecv recv for fluentd format
type FluentdRecv struct {
	*BaseRecv
	*FluentdRecvCfg
	logger *utils.LoggerType
	listen func(string, string) (net.Listener, error)

	concatTagCfg   map[string]*concatCfg
	pendingMsgPool *sync.Pool
	concators      []chan *library.FluentMsg
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *library.FluentMsg
	lastT time.Time
}

// NewFluentdRecv create new FluentdRecv
func NewFluentdRecv(cfg *FluentdRecvCfg) (r *FluentdRecv) {
	r = &FluentdRecv{
		logger:         log.Logger.Named(cfg.Name),
		BaseRecv:       &BaseRecv{},
		FluentdRecvCfg: cfg,
		pendingMsgPool: &sync.Pool{
			New: func() interface{} {
				return &PendingMsg{}
			},
		},
		concatTagCfg: map[string]*concatCfg{},
	}
	if err := r.valid(); err != nil {
		log.Logger.Panic("config invalid", zap.Error(err))
	}

	tags := []string{}
	for tag, cfgi := range cfg.ConcatCfg {
		tags = append(tags, tag)
		cfg := cfgi.(map[string]interface{})
		r.concatTagCfg[tag] = &concatCfg{
			identifierKey: cfg["identifier"].(string),
			msgKey:        cfg["msg_key"].(string),
			headRegexp:    regexp.MustCompile(cfg["head_regexp"].(string)),
		}
	}

	r.logger.Info("create fluentd recv",
		zap.String("lb_key", r.LBKey),
		zap.String("tag_key", r.TagKey),
		zap.String("addr", r.Addr),
		zap.Strings("tags", tags),
		zap.Int("n_fork", r.NFork),
		zap.Bool("is_rewrite_tag_from_tag_key", r.IsRewriteTagFromTagKey),
		zap.String("origin_rewrite_tag_key", r.OriginRewriteTagKey),
	)
	return r
}

func (r *FluentdRecv) valid() error {
	if r.IsRewriteTagFromTagKey {
		if r.OriginRewriteTagKey == "" {
			log.Logger.Panic("if IsRewriteTagFromTagKey is setted, OriginRewriteTagKey should not empty")
		}
	}

	if r.NFork <= 0 {
		r.NFork = 1
		log.Logger.Info("reset n_fork", zap.Int("n_fork", r.NFork))
	}

	if r.ConcatorBufSize <= 0 {
		r.ConcatorBufSize = 1024
		log.Logger.Info("reset internal_buf_size", zap.Int("internal_buf_size", r.ConcatorBufSize))

	} else if r.ConcatorBufSize < 1000 {
		log.Logger.Warn("internal_buf_size better greater than 1000", zap.Int("internal_buf_size", r.ConcatorBufSize))
	}

	if r.ConcatorWait < 1*time.Second {
		r.ConcatorWait = defaultConcatorWait
		log.Logger.Info("reset concat_with_sec", zap.Duration("concat_with_sec", r.ConcatorWait))
	}

	if r.ConcatMaxLen == 0 {
		r.ConcatMaxLen = 300000
		log.Logger.Warn("reset concat_max_len", zap.Int("concat_max_len", r.ConcatMaxLen))
	}

	if r.TagKey == "" {
		r.TagKey = "tag"
		log.Logger.Info("reset tag_key", zap.String("tag_key", r.TagKey))
	}

	if r.LBKey == "" {
		r.LBKey = "container_id"
		log.Logger.Info("reset lb_key", zap.String("lb_key", r.LBKey))
	}

	if r.Addr == "" {
		r.Addr = "0.0.0.0:24225"
		log.Logger.Info("reset addr", zap.String("addr", r.Addr))
	}

	return nil
}

// GetName return the name of this recv
func (r *FluentdRecv) GetName() string {
	return r.Name
}

// Run starting this recv
func (r *FluentdRecv) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	r.concators = make([]chan *library.FluentMsg, r.NFork)
	for i := range r.concators {
		r.concators[i] = make(chan *library.FluentMsg, r.ConcatorBufSize)
		workers.Add(1)
		go func(i int) { defer workers.Done(); r.runConcator(ctx, i, r.concators[i]) }(i)
	}
	for ctx.Err() == nil {
		listen := net.Listen
		if r.listen != nil {
			listen = r.listen
		}
		ln, err := listen("tcp", r.Addr)
		if err != nil {
			r.logger.Error("listen for Fluent input", zap.Error(err))
		} else {
			stop := context.AfterFunc(ctx, func() { ln.Close() })
			for ctx.Err() == nil {
				conn, err := ln.Accept()
				if err != nil {
					if ctx.Err() == nil {
						r.logger.Warn("accept Fluent connection", zap.Error(err))
					}
					break
				}
				workers.Add(1)
				go func() { defer workers.Done(); r.decodeMsg(ctx, conn) }()
			}
			stop()
			ln.Close()
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (r *FluentdRecv) decodeMsg(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	// A context check before Read is insufficient: a silent peer can block Read
	// forever. Closing this connection also interrupts blocked backpressure.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	reader := msgp.NewReader(conn)
	var frame library.FluentBatchMsg
	deliver := func(tag string, record interface{}) bool {
		fields, ok := record.(map[string]interface{})
		if !ok || fields == nil {
			return true
		} // invalid entry, not a valid record
		msg := r.getMsg()
		msg.Tag = tag
		msg.Message = fields
		return r.processMsg(ctx, msg)
	}
	for ctx.Err() == nil {
		if err := frame.DecodeMsg(reader); err != nil {
			return
		}
		if len(frame) < 2 {
			continue
		}
		var tag string
		switch value := frame[0].(type) {
		case string:
			tag = value
		case []byte:
			tag = string(value)
		default:
			continue
		}
		switch body := frame[1].(type) {
		case []interface{}:
			for _, raw := range body {
				entry, ok := raw.([]interface{})
				if !ok || len(entry) < 2 {
					continue
				}
				if !deliver(tag, entry[1]) {
					return
				}
			}
		case []byte:
			packed := msgp.NewReader(bytes.NewReader(body))
			for ctx.Err() == nil {
				var entry library.FluentBatchMsg
				// A malformed packed stream must terminate, not spin without consuming
				// bytes on a sticky decoder error.
				if err := entry.DecodeMsg(packed); err != nil {
					break
				}
				if len(entry) >= 2 && !deliver(tag, entry[1]) {
					return
				}
			}
		default:
			if len(frame) >= 3 && !deliver(tag, frame[2]) {
				return
			}
		}
	}
}

// ProcessMsg retains the standalone API. Network workers use the cancellable
// variant so neither an idle socket nor a full downstream queue pins shutdown.
func (r *FluentdRecv) ProcessMsg(msg *library.FluentMsg) { r.processMsg(context.Background(), msg) }
func (r *FluentdRecv) processMsg(ctx context.Context, msg *library.FluentMsg) bool {
	if msg == nil {
		return true
	}
	if msg.Message == nil {
		r.msgPool.Put(msg)
		return true
	}
	if r.IsRewriteTagFromTagKey {
		switch tag := msg.Message[r.OriginRewriteTagKey].(type) {
		case string:
			msg.Tag = tag
		case []byte:
			msg.Tag = string(tag)
		default:
			r.msgPool.Put(msg)
			return true
		}
		msg.Message[r.TagKey] = msg.Tag
	}
	if len(r.concators) == 0 {
		return r.sendMsg(ctx, msg)
	}
	idx := 0
	if len(r.concators) > 1 {
		var hash uint64
		switch key := msg.Message[r.LBKey].(type) {
		case string:
			hash = xxhash.Sum64String(key)
		case []byte:
			hash = xxhash.Sum64(key)
		default:
			return r.sendMsg(ctx, msg)
		}
		idx = int(hash % uint64(len(r.concators)))
	}
	select {
	case r.concators[idx] <- msg:
		return true
	case <-ctx.Done():
		r.msgPool.Put(msg)
		return false
	}
}

// SendMsg puts a newly received record into the acceptor, before journaling.
func (r *FluentdRecv) SendMsg(msg *library.FluentMsg) { r.sendMsg(context.Background(), msg) }
func (r *FluentdRecv) sendMsg(ctx context.Context, msg *library.FluentMsg) bool {
	msg.Message[r.TagKey] = msg.Tag
	msg.ID = r.counter.Count()
	select {
	case r.asyncOutChan <- msg:
		return true
	case <-ctx.Done():
		r.msgPool.Put(msg)
		return false
	}
}

func (r *FluentdRecv) runConcator(ctx context.Context, i int, inChan chan *library.FluentMsg) {
	type key struct{ tag, identifier string }
	pending := map[key]*PendingMsg{}
	timer := time.NewTimer(r.ConcatorWait)
	timer.Stop()
	defer timer.Stop()
	var tick <-chan time.Time
	release := func(k key, p *PendingMsg) { delete(pending, k); p.msg = nil; r.pendingMsgPool.Put(p) }
	defer func() {
		for k, p := range pending {
			r.msgPool.Put(p.msg)
			release(k, p)
		}
	}()
	// sendMsg consumes ownership on both success and cancellation.
	flush := func(k key, p *PendingMsg) bool { m := p.msg; release(k, p); return r.sendMsg(ctx, m) }
	text := func(v interface{}) ([]byte, bool) {
		switch v := v.(type) {
		case string:
			return []byte(v), true
		case []byte:
			return v, true
		default:
			return nil, false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			now := time.Now()
			wait := r.ConcatorWait
			for k, p := range pending {
				remaining := r.ConcatorWait - now.Sub(p.lastT)
				if remaining <= 0 {
					if !flush(k, p) {
						return
					}
				} else if remaining < wait {
					wait = remaining
				}
			}
			if len(pending) == 0 {
				tick = nil
			} else {
				timer.Reset(wait)
			}
		case msg, ok := <-inChan:
			if !ok {
				for k, p := range pending {
					if !flush(k, p) {
						return
					}
				}
				return
			}
			if msg == nil {
				continue
			}
			cfg, configured := r.concatTagCfg[msg.Tag]
			if !configured {
				if !r.sendMsg(ctx, msg) {
					return
				}
				continue
			}
			data, valid := text(msg.Message[cfg.msgKey])
			identifier, validID := text(msg.Message[cfg.identifierKey])
			if !valid || !validID {
				if !r.sendMsg(ctx, msg) {
					return
				}
				continue
			}
			msg.Message[cfg.msgKey] = data
			k := key{msg.Tag, string(identifier)}
			p, exists := pending[k]
			if exists && (cfg.headRegexp.Match(data) || time.Since(p.lastT) >= r.ConcatorWait) {
				if !flush(k, p) {
					r.msgPool.Put(msg)
					return
				}
				exists = false
			}
			if !exists {
				if !cfg.headRegexp.Match(data) {
					if !r.sendMsg(ctx, msg) {
						return
					}
					continue
				}
				if len(pending) == 0 {
					timer.Reset(r.ConcatorWait)
					tick = timer.C
				}
				p = r.pendingMsgPool.Get().(*PendingMsg)
				p.msg = msg
				p.lastT = time.Now()
				pending[k] = p
				continue
			}
			p.msg.Message[cfg.msgKey] = append(p.msg.Message[cfg.msgKey].([]byte), data...)
			p.lastT = time.Now()
			r.msgPool.Put(msg)
			if len(p.msg.Message[cfg.msgKey].([]byte)) >= r.ConcatMaxLen {
				if !flush(k, p) {
					return
				}
				if len(pending) == 0 {
					timer.Stop()
					tick = nil
				}
			}
		}
	}
}
