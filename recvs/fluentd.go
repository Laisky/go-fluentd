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
	"github.com/tinylib/msgp/msgp"
)

// FluentdRecvCfg configuration of FluentdRecv
type FluentdRecvCfg struct {
	// Addr: like `127.0.0.1:24225;`
	// TagKey: set `msg.Message[TagKey] = tag`
	Name, Addr, TagKey string

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

	concatTagCfg   map[string]*concatCfg
	pendingMsgPool *sync.Pool
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *libs.FluentMsg
	lastT time.Time
}

// NewFluentdRecv create new FluentdRecv
func NewFluentdRecv(cfg *FluentdRecvCfg) *FluentdRecv {
	utils.Logger.Info("create FluentdRecv",
		zap.String("name", cfg.Name),
		zap.Bool("is_rewrite_tag_from_tag_key", cfg.IsRewriteTagFromTagKey),
		zap.String("origin_rewrite_tag_key", cfg.OriginRewriteTagKey))
	if cfg.IsRewriteTagFromTagKey {
		if cfg.OriginRewriteTagKey == "" {
			utils.Logger.Panic("if IsRewriteTagFromTagKey is setted, OriginRewriteTagKey should not empty")
		}
	}

	r := &FluentdRecv{
		BaseRecv:       &BaseRecv{},
		FluentdRecvCfg: cfg,
		pendingMsgPool: &sync.Pool{
			New: func() interface{} {
				return &PendingMsg{}
			},
		},
		concatTagCfg: map[string]*concatCfg{},
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
	utils.Logger.Info("enable concator for tags", zap.Strings("tags", tags))

	return r
}

// GetName return the name of this recv
func (r *FluentdRecv) GetName() string {
	return r.Name
}

// Run starting this recv
func (r *FluentdRecv) Run(ctx context.Context) {
	utils.Logger.Info("run FluentdRecv")
	var (
		conn net.Conn
	)

	utils.Logger.Info("listening on tcp...", zap.String("addr", r.Addr))
	ln, err := net.Listen("tcp", r.Addr)
	if err != nil {
		utils.Logger.Error("try to bind addr got error", zap.Error(err))
	}

	defer utils.Logger.Info("fluentd recv exit")
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, err = ln.Accept()
		if err != nil {
			utils.Logger.Error("try to accept connection got error", zap.Error(err))
			continue
		}

		utils.Logger.Info("accept new connection", zap.String("remote", conn.RemoteAddr().String()))
		go func(conn net.Conn) {
			r.decodeMsg(conn)
		}(conn)
	}
}

func (r *FluentdRecv) decodeMsg(conn net.Conn) {
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
		conCtx  *concatCtx
		eof     = msgp.WrapError(io.EOF)
	)

	for {
		if err = v.DecodeMsg(reader); err == eof {
			utils.Logger.Info("connection closed", zap.String("remote", conn.RemoteAddr().String()))
			r.closeConcatCtx(conCtx)
			return
		} else if err != nil {
			utils.Logger.Error("decode message got error", zap.Error(err))
			r.closeConcatCtx(conCtx)
			return
		}

		if len(v) < 2 {
			utils.Logger.Error("unknown message format for length", zap.String("msg", fmt.Sprint(v)))
			continue
		}

		switch msgTag := v[0].(type) {
		case []byte:
			tag = string(msgTag)
		case string:
			tag = msgTag
		default:
			utils.Logger.Error("message[0] is not `[]byte` or string", zap.String("tag", fmt.Sprint(v[0])))
			continue
		}

		switch msgBody := v[1].(type) {
		case []interface{}:
			utils.Logger.Debug("got message in format: `[]interface{}`")
			for _, entryI = range msgBody {
				msg = r.msgPool.Get().(*libs.FluentMsg)
				if msg.Message, ok = entryI.([]interface{})[1].(map[string]interface{}); !ok {
					utils.Logger.Error("failed to decode message", zap.String("tag", tag))
					r.msgPool.Put(msg)
					continue
				}
				msg.Tag = tag
				r.SendMsg(msg)
			}
		case []byte: // embedded format
			utils.Logger.Debug("got message in format: `[]byte`")
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
					utils.Logger.Error("failed to decode message")
					continue
				} else if len(v2) < 2 {
					utils.Logger.Error("unknown message format for length", zap.String("msg", fmt.Sprint(v2)))
					continue
				} else {
					msg = r.msgPool.Get().(*libs.FluentMsg)
					if msg.Message, ok = v2[1].(map[string]interface{}); !ok {
						utils.Logger.Error("msg format incorrect", zap.String("msg", fmt.Sprint(v2[1])))
						r.msgPool.Put(msg)
						continue
					}
					msg.Tag = tag
					r.SendMsg(msg)
				}
			}
		default:
			if len(v) < 3 {
				utils.Logger.Error("unknown message format for length", zap.String("msg", fmt.Sprint(v)))
				continue
			}

			utils.Logger.Debug("got message in format: default")
			switch msgBody := v[2].(type) {
			case map[string]interface{}:
				msg = r.msgPool.Get().(*libs.FluentMsg)
				msg.Message = msgBody
			default:
				utils.Logger.Error("unknown msg format", zap.String("msg", fmt.Sprint(v)))
				continue
			}

			msg.Tag = tag
			if _, ok = r.concatTagCfg[tag]; ok { // need concat
				if conCtx == nil {
					utils.Logger.Info("create new concatCtx for new conn")
					conCtx = &concatCtx{
						concatCfg:          r.concatTagCfg[tag],
						identifier2LastMsg: map[string]*PendingMsg{},
					}
				}
				msg = r.Concat(conCtx, msg)
			}

			if msg == nil {
				continue
			}
			r.SendMsg(msg)
		}
	}
}

// SendMsg put msg into downstream
func (r *FluentdRecv) SendMsg(msg *libs.FluentMsg) {
	if r.IsRewriteTagFromTagKey { // rewrite msg.Tag by msg.Message[OriginRewriteTagKey]
		switch msg.Message[r.OriginRewriteTagKey].(type) {
		case string:
			msg.Tag = msg.Message[r.OriginRewriteTagKey].(string)
		case []byte:
			msg.Tag = string(msg.Message[r.OriginRewriteTagKey].([]byte))
		default:
			utils.Logger.Warn("unknown type of msg tag key",
				zap.String("tag", fmt.Sprint(msg.Message[r.OriginRewriteTagKey])),
				zap.String("tag_key", r.OriginRewriteTagKey))
			r.msgPool.Put(msg)
			return
		}

		utils.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	}

	msg.Message[r.TagKey] = msg.Tag
	msg.Id = r.counter.Count()
	utils.Logger.Debug("receive new msg", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
	r.asyncOutChan <- msg
}

type concatCtx struct {
	*concatCfg
	identifier2LastMsg map[string]*PendingMsg

	ok         bool
	log        []byte
	identifier string
	pmsg       *PendingMsg
}

func (r *FluentdRecv) closeConcatCtx(ctx *concatCtx) {
	if ctx == nil {
		return
	}

	for _, ctx.pmsg = range ctx.identifier2LastMsg {
		r.SendMsg(ctx.pmsg.msg)
		r.pendingMsgPool.Put(ctx.pmsg)
	}
}

// Concat concatenate with old messages
func (r *FluentdRecv) Concat(ctx *concatCtx, msg *libs.FluentMsg) (newMsg *libs.FluentMsg) {
	switch msg.Message[ctx.msgKey].(type) {
	case []byte:
		ctx.log = msg.Message[ctx.msgKey].([]byte)
	case string:
		ctx.log = []byte(msg.Message[ctx.msgKey].(string))
		msg.Message[ctx.msgKey] = ctx.log
	default:
		utils.Logger.Warn("unknown msg key or unknown type",
			zap.String("tag", msg.Tag),
			zap.String("msg_key", ctx.msgKey),
			zap.String("msg", fmt.Sprint(msg.Message)))
		return msg
	}

	switch msg.Message[ctx.identifierKey].(type) {
	case []byte:
		ctx.identifier = string(msg.Message[ctx.identifierKey].([]byte))
	case string:
		ctx.identifier = msg.Message[ctx.identifierKey].(string)
	default:
		utils.Logger.Warn("unknown identifier or unknown type",
			zap.String("tag", msg.Tag),
			zap.String("identifier_key", ctx.identifier),
			zap.String("identifier", fmt.Sprint(msg.Message[ctx.identifierKey])))
		return msg
	}

	if ctx.pmsg, ctx.ok = ctx.identifier2LastMsg[ctx.identifier]; !ctx.ok { // new identifier
		// new line with incorrect format, skip
		if !ctx.headRegexp.Match(ctx.log) {
			utils.Logger.Debug("log not match head regexp and there is no identifier exists",
				zap.String("identifier", ctx.identifier),
				zap.String("identifier_key", ctx.identifierKey),
				zap.ByteString("log", ctx.log))
			return msg
		}

		// new line with correct format, set as first line
		utils.Logger.Debug("got new identifier",
			zap.String("indentifier", ctx.identifier),
			zap.ByteString("log", ctx.log))
		ctx.identifier2LastMsg[ctx.identifier] = r.pendingMsgPool.Get().(*PendingMsg)
		ctx.identifier2LastMsg[ctx.identifier].msg = msg
		ctx.identifier2LastMsg[ctx.identifier].lastT = utils.Clock.GetUTCNow()
		return nil
	}

	// replace exists msg in slot
	if ctx.headRegexp.Match(ctx.log) { // new line
		utils.Logger.Debug("got new line",
			zap.ByteString("log", ctx.log),
			zap.String("tag", msg.Tag))

		newMsg = ctx.identifier2LastMsg[ctx.identifier].msg
		ctx.identifier2LastMsg[ctx.identifier].msg = msg
		ctx.identifier2LastMsg[ctx.identifier].lastT = utils.Clock.GetUTCNow()
		return newMsg
	}

	// need to concat
	utils.Logger.Debug("concat lines",
		zap.String("tag", msg.Tag),
		zap.ByteString("log", msg.Message[ctx.msgKey].([]byte)))
	// ctx.identifier2LastMsg[ctx.identifier].msg.Message[ctx.msgKey] =
	// 	append(ctx.identifier2LastMsg[ctx.identifier].msg.Message[ctx.msgKey].([]byte), '\n')
	ctx.identifier2LastMsg[ctx.identifier].msg.Message[ctx.msgKey] =
		append(ctx.identifier2LastMsg[ctx.identifier].msg.Message[ctx.msgKey].([]byte), msg.Message[ctx.msgKey].([]byte)...)

	// too long to send
	if len(ctx.identifier2LastMsg[ctx.identifier].msg.Message[ctx.msgKey].([]byte)) >= r.ConcatMaxLen {
		utils.Logger.Debug("too long to send", zap.String("", ctx.msgKey), zap.String("tag", msg.Tag))
		newMsg = ctx.identifier2LastMsg[ctx.identifier].msg
		r.pendingMsgPool.Put(ctx.identifier2LastMsg[ctx.identifier])
		delete(ctx.identifier2LastMsg, ctx.identifier)
		return newMsg
	}

	// discard concated msg
	r.msgPool.Put(msg)
	return nil
}
