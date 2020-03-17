package recvs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
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

	logger         *utils.LoggerType
	concatTagCfg   map[string]*concatCfg
	pendingMsgPool *sync.Pool
	concators      []chan *libs.FluentMsg
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *libs.FluentMsg
	lastT time.Time
}

// NewFluentdRecv create new FluentdRecv
func NewFluentdRecv(cfg *FluentdRecvCfg) (r *FluentdRecv) {
	utils.Logger.Info("create FluentdRecv",
		zap.String("name", cfg.Name),
		zap.String("lb_key", cfg.LBKey),
		zap.Bool("is_rewrite_tag_from_tag_key", cfg.IsRewriteTagFromTagKey),
		zap.String("origin_rewrite_tag_key", cfg.OriginRewriteTagKey))

	validateConfigs(cfg)
	r = &FluentdRecv{
		BaseRecv:       &BaseRecv{},
		FluentdRecvCfg: cfg,
		pendingMsgPool: &sync.Pool{
			New: func() interface{} {
				return &PendingMsg{}
			},
		},
		concatTagCfg: map[string]*concatCfg{},
		logger:       utils.Logger.With(zap.String("name", cfg.Name)),
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
	r.logger.Info("enable concator for tags", zap.Strings("tags", tags))
	return r
}

func validateConfigs(cfg *FluentdRecvCfg) {
	if cfg.IsRewriteTagFromTagKey {
		if cfg.OriginRewriteTagKey == "" {
			utils.Logger.Panic("if IsRewriteTagFromTagKey is setted, OriginRewriteTagKey should not empty")
		}
	}
	if cfg.NFork < 1 {
		utils.Logger.Panic("NFork must greater than 0", zap.Int("NFork", cfg.NFork))
	}
	if cfg.ConcatorBufSize < 1 {
		utils.Logger.Panic("ConcatorBufSize must greater than 0", zap.Int("ConcatorBufSize", cfg.ConcatorBufSize))

	} else if cfg.ConcatorBufSize < 1000 {
		utils.Logger.Warn("ConcatorBufSize better greater than 1000", zap.Int("ConcatorBufSize", cfg.ConcatorBufSize))
	}
	if cfg.ConcatorWait < 1*time.Second {
		utils.Logger.Warn("reset ConcatorWait", zap.Duration("old", cfg.ConcatorWait), zap.Duration("new", defaultConcatorWait))
		cfg.ConcatorWait = defaultConcatorWait
	}
}

// GetName return the name of this recv
func (r *FluentdRecv) GetName() string {
	return r.Name
}

// Run starting this recv
func (r *FluentdRecv) Run(ctx context.Context) {
	r.logger.Info("run FluentdRecv")
	defer r.logger.Info("fluentd recv exist")
	r.concators = r.startConcators(ctx)
	var conn net.Conn
LISTENER_LOOP:
	for {
		select {
		case <-ctx.Done():
			break LISTENER_LOOP
		default:
		}

		r.logger.Info("listening on tcp...", zap.String("addr", r.Addr))
		ln, err := net.Listen("tcp", r.Addr)
		if err != nil {
			r.logger.Error("try to bind addr got error", zap.Error(err))
		}

	ACCEPT_LOOP:
		for {
			select {
			case <-ctx.Done():
				break ACCEPT_LOOP
			default:
			}

			conn, err = ln.Accept()
			if err != nil {
				r.logger.Error("try to accept connection got error", zap.Error(err))
				break ACCEPT_LOOP
			}

			r.logger.Info("accept new connection", zap.String("remote", conn.RemoteAddr().String()))
			go r.decodeMsg(ctx, conn)
		}

		r.logger.Info("close listener", zap.String("addr", r.Addr))
		ln.Close()
	}
}

func (r *FluentdRecv) decodeMsg(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	var (
		reader = msgp.NewReader(conn)
		v      = libs.FluentBatchMsg{nil, nil, nil} // tag, time, messages
		// 2 means inner decoder for embedded format such like [][]interface{tag, messages}
		buf2    *bytes.Reader
		reader2 *msgp.Reader
		v2      = libs.FluentBatchMsg{nil, nil, nil} // tag, time, messages
		msg     *libs.FluentMsg
		err     error
		tag     string
		ok      bool
		entryI  interface{}
		eof     = msgp.WrapError(io.EOF)

		msgCnt, totalMsgCnt int
	)
	defer r.logger.Info("close connection",
		zap.String("remote", conn.RemoteAddr().String()))

	for {
		msgCnt = 0
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err = v.DecodeMsg(reader); err == eof {
			r.logger.Info("remote closed",
				zap.String("remote", conn.RemoteAddr().String()))
			return
		} else if err != nil {
			r.logger.Error("decode connection", zap.Error(err))
			return
		}

		if len(v) < 2 {
			r.logger.Warn("discard msg since unknown message format, length should be 2", zap.String("msg", fmt.Sprint(v)))
			continue
		}

		switch msgTag := v[0].(type) {
		case []byte:
			tag = string(msgTag)
		case string:
			tag = msgTag
		default:
			r.logger.Warn("discard msg since unknown message format, message[0] is not `[]byte` or string",
				zap.String("tag", fmt.Sprint(v[0])))
			continue
		}
		r.logger.Debug("got message tag", zap.String("tag", tag))

		switch msgBody := v[1].(type) {
		case []interface{}:
			for _, entryI = range msgBody {
				msg = r.msgPool.Get().(*libs.FluentMsg)
				if msg.Message, ok = entryI.([]interface{})[1].(map[string]interface{}); !ok {
					r.logger.Warn("discard msg since unknown message format, cannot decode",
						zap.String("tag", tag))
					r.msgPool.Put(msg)
					continue
				}
				// []interface{})[0] is
				// "pateo.qingcloud.kube.sit.aitimer-7b6b654d8-7hpsw_ai_aitimer-f25c8bfea7b30ed7ba7c600cdb75e6aa7326ba4b67139e3338bf873bd5036921"
				msg.Tag = tag
				msgCnt++
				r.ProcessMsg(msg)
			}
			r.logger.Debug("got message in format: `[]interface{}`", zap.Int("n", msgCnt))
		case []byte: // embedded format
			if buf2 == nil {
				buf2 = bytes.NewReader(msgBody)
			} else {
				buf2.Reset(msgBody)
			}

			if reader2 == nil {
				reader2 = msgp.NewReader(buf2)
			} else {
				reader2.Reset(buf2)
			}

			for {
				if err = v2.DecodeMsg(reader2); err == eof {
					break
				} else if err != nil {
					r.logger.Warn("discard msg since unknown message format, cannot decode")
					continue
				} else if len(v2) < 2 {
					r.logger.Warn("discard msg since unknown message format, length should be 2",
						zap.String("msg", fmt.Sprint(v2)))
					continue
				} else {
					msg = r.msgPool.Get().(*libs.FluentMsg)
					if msg.Message, ok = v2[1].(map[string]interface{}); !ok {
						r.logger.Warn("discard msg since unknown message format",
							zap.String("msg", fmt.Sprint(v2[1])))
						r.msgPool.Put(msg)
						continue
					}
					msg.Tag = tag
					r.ProcessMsg(msg)
					msgCnt++
				}
			}
			r.logger.Debug("got message in format: `[]byte`", zap.Int("n", msgCnt))
		default:
			if len(v) < 3 {
				r.logger.Warn("discard msg since unknown message format for length, length should be 3",
					zap.String("msg", fmt.Sprint(v)))
				continue
			}

			switch msgBody := v[2].(type) {
			case map[string]interface{}:
				msg = r.msgPool.Get().(*libs.FluentMsg)
				msg.Message = msgBody
			default:
				r.logger.Warn("discard msg since unknown msg format", zap.String("msg", fmt.Sprint(v)))
				continue
			}
			msg.Tag = tag
			r.ProcessMsg(msg)
			msgCnt++
			r.logger.Debug("got message in format: default", zap.Int("n", msgCnt))
		}

		totalMsgCnt += msgCnt
		utils.Logger.Debug("msg stats", zap.Int("total", totalMsgCnt))
	}
}

// ProcessMsg process msg
func (r *FluentdRecv) ProcessMsg(msg *libs.FluentMsg) {
	if r.IsRewriteTagFromTagKey { // rewrite msg.Tag by msg.Message[OriginRewriteTagKey]
		switch tag := msg.Message[r.OriginRewriteTagKey].(type) {
		case string:
			msg.Tag = tag
		case []byte:
			msg.Tag = string(tag)
		default:
			r.logger.Warn("discard msg since unknown type of tag key",
				zap.String("tag", fmt.Sprint(tag)),
				zap.String("tag_key", r.OriginRewriteTagKey))
			r.msgPool.Put(msg)
			return
		}
		r.logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
		msg.Message[r.TagKey] = msg.Tag
	}

	switch lbkey := msg.Message[r.LBKey].(type) {
	case []byte:
		r.concators[int(xxhash.Sum64(lbkey)%uint64(r.NFork))] <- msg
	case string:
		r.concators[int(xxhash.Sum64String(lbkey)%uint64(r.NFork))] <- msg
	default:
		r.logger.Warn("discard msg since unknown type of LBKey",
			zap.String("LBKey", r.LBKey),
			zap.String("val", fmt.Sprint(lbkey)))
		r.msgPool.Put(msg)
	}
}

// SendMsg put msg into downstream
func (r *FluentdRecv) SendMsg(msg *libs.FluentMsg) {
	msg.Message[r.TagKey] = msg.Tag
	msg.Id = r.counter.Count()
	r.logger.Debug("receive new msg", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
	r.asyncOutChan <- msg
}

func (r *FluentdRecv) startConcators(ctx context.Context) (concators []chan *libs.FluentMsg) {
	concators = make([]chan *libs.FluentMsg, r.NFork)
	for i := 0; i < r.NFork; i++ {
		r.logger.Info("start concator", zap.Int("fork", i))
		concators[i] = make(chan *libs.FluentMsg, r.ConcatorBufSize)
		go r.runConcator(ctx, i, concators[i])
	}
	return
}

func (r *FluentdRecv) runConcator(ctx context.Context, i int, inChan chan *libs.FluentMsg) {
	logger := r.logger.With(zap.Int("i", i))
	defer logger.Info("fluentd concator exit")
	var (
		tag, identifier    string
		msg, oldMsg        *libs.FluentMsg
		log                []byte
		pmsg               *PendingMsg
		identifier2LastMsg = map[string]*PendingMsg{}
		ok                 bool
		cfg                *concatCfg
		cleanTicker        = time.NewTicker(defaultConcatorCleanInterval)
		ts                 time.Time
		idenN, deletN      int
	)
	defer cleanTicker.Stop()

NEW_MSG_LOOP:
	for {
		select {
		case <-ctx.Done():
			break NEW_MSG_LOOP
		case msg, ok = <-inChan:
			if !ok {
				break NEW_MSG_LOOP
			}
		case <-cleanTicker.C: // clean old msgs
			ts = utils.Clock.GetUTCNow()
			idenN = 0
			deletN = 0
			for identifier, pmsg = range identifier2LastMsg {
				idenN++
				if utils.Clock.GetUTCNow().Sub(pmsg.lastT) > r.ConcatorWait {
					deletN++
					r.SendMsg(pmsg.msg)
					r.pendingMsgPool.Put(pmsg)
					delete(identifier2LastMsg, identifier)
					continue
				}
			}
			logger.Info("clean identifier2LastMsg",
				zap.Int("total", idenN),
				zap.Int("deleted", deletN),
				zap.Duration("cost", utils.Clock.GetUTCNow().Sub(ts)))
			continue
		}

		tag = msg.Tag
		if cfg, ok = r.concatTagCfg[tag]; !ok {
			logger.Debug("unknown tag for concator", zap.String("tag", tag))
			r.SendMsg(msg)
			continue
		}

		switch msg.Message[cfg.msgKey].(type) {
		case []byte:
			log = msg.Message[cfg.msgKey].([]byte)
		case string:
			log = []byte(msg.Message[cfg.msgKey].(string))
			msg.Message[cfg.msgKey] = log
		default:
			logger.Warn("unknown msg key or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("msg_key", cfg.msgKey),
				zap.String("msg", fmt.Sprint(msg.Message)))
			r.SendMsg(msg)
			continue
		}

		switch msg.Message[cfg.identifierKey].(type) {
		case []byte:
			identifier = string(msg.Message[cfg.identifierKey].([]byte))
		case string:
			identifier = msg.Message[cfg.identifierKey].(string)
		default:
			logger.Warn("unknown identifier or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("identifier_key", identifier),
				zap.String("identifier", fmt.Sprint(msg.Message[cfg.identifierKey])))
			r.SendMsg(msg)
			continue
		}

		if pmsg, ok = identifier2LastMsg[identifier]; !ok { // new identifier
			// new line with incorrect format, skip
			if !cfg.headRegexp.Match(log) {
				logger.Debug("log not match head regexp and there is no identifier exists",
					zap.String("identifier", identifier),
					zap.String("identifier_key", cfg.identifierKey),
					zap.ByteString("log", log))
				r.SendMsg(msg)
				continue
			}

			// new line with correct format, set as first line
			logger.Debug("got new identifier",
				zap.String("indentifier", identifier),
				zap.ByteString("log", log))
			pmsg = r.pendingMsgPool.Get().(*PendingMsg)
			pmsg.msg = msg
			pmsg.lastT = utils.Clock.GetUTCNow()
			identifier2LastMsg[identifier] = pmsg
			continue
		}

		// replace exists msg in slot
		if cfg.headRegexp.Match(log) || utils.Clock.GetUTCNow().Sub(pmsg.lastT) > r.ConcatorWait { // new line
			logger.Debug("got new line",
				zap.ByteString("log", log),
				zap.String("tag", msg.Tag))

			oldMsg = pmsg.msg
			pmsg.msg = msg
			pmsg.lastT = utils.Clock.GetUTCNow()
			r.SendMsg(oldMsg)
			continue
		}

		// need to concat
		logger.Debug("concat lines",
			zap.String("tag", msg.Tag),
			zap.ByteString("log", msg.Message[cfg.msgKey].([]byte)))
		// pmsg.msg.Message[cfg.msgKey] =
		// 	append(pmsg.msg.Message[cfg.msgKey].([]byte), '\n')
		pmsg.msg.Message[cfg.msgKey] =
			append(pmsg.msg.Message[cfg.msgKey].([]byte), msg.Message[cfg.msgKey].([]byte)...)
		pmsg.lastT = utils.Clock.GetUTCNow()
		r.msgPool.Put(msg) // discard concated msg

		// too long to send
		if len(pmsg.msg.Message[cfg.msgKey].([]byte)) >= r.ConcatMaxLen {
			logger.Debug("too long to send", zap.String("msgKey", cfg.msgKey), zap.String("tag", msg.Tag))
			msg = pmsg.msg
			r.pendingMsgPool.Put(pmsg)
			delete(identifier2LastMsg, identifier)
			r.SendMsg(msg)
			continue
		}
	}

	// do clean
	for _, pmsg = range identifier2LastMsg {
		r.SendMsg(pmsg.msg)
		r.pendingMsgPool.Put(pmsg)
	}
}
