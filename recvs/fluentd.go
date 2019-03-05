package recvs

import (
	"bytes"
	"fmt"
	"io"
	"net"

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

	// if IsRewriteTagFromTagKey, set `msg.Tag = msg.Message[TagKey]`
	IsRewriteTagFromTagKey bool
}

// FluentdRecv recv for fluentd format
type FluentdRecv struct {
	*BaseRecv
	*FluentdRecvCfg
}

// NewFluentdRecv create new FluentdRecv
func NewFluentdRecv(cfg *FluentdRecvCfg) *FluentdRecv {
	utils.Logger.Info("create FluentdRecv")
	return &FluentdRecv{
		BaseRecv:       &BaseRecv{},
		FluentdRecvCfg: cfg,
	}
}

// GetName return the name of this recv
func (r *FluentdRecv) GetName() string {
	return r.Name
}

// Run starting this recv
func (r *FluentdRecv) Run() {
	utils.Logger.Info("run FluentdRecv")
	var (
		conn net.Conn
	)

	utils.Logger.Info("listening on tcp...", zap.String("addr", r.Addr))
	ln, err := net.Listen("tcp", r.Addr)
	if err != nil {
		utils.Logger.Error("try to bind addr got error", zap.Error(err))
	}

	for {
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
		eof     = msgp.WrapError(io.EOF)
	)

	for {
		if err = v.DecodeMsg(reader); err == eof {
			utils.Logger.Info("connection closed", zap.String("remote", conn.RemoteAddr().String()))
			return
		} else if err != nil {
			utils.Logger.Error("decode message got error", zap.Error(err))
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
			r.SendMsg(msg)
		}
	}
}

// SendMsg put msg into downstream
func (r *FluentdRecv) SendMsg(msg *libs.FluentMsg) {
	if r.IsRewriteTagFromTagKey {
		switch msg.Message[r.TagKey].(type) {
		case string:
			msg.Tag = msg.Message[r.TagKey].(string)
		case []byte:
			msg.Tag = string(msg.Message[r.TagKey].([]byte))
		default:
			utils.Logger.Warn("unknown type of msg tag key",
				zap.String("tag", fmt.Sprint(msg.Message[r.TagKey])),
				zap.String("tag_key", r.TagKey))
			r.msgPool.Put(msg)
			return
		}

		utils.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	} else {
		msg.Message[r.TagKey] = msg.Tag
	}

	msg.Id = r.counter.Count()
	utils.Logger.Debug("receive new msg", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
	r.asyncOutChan <- msg
}
