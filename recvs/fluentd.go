// Package recvs defines different kind of receivers.
//
// recvs are components applied in acceptor. Each recv can
// receiving specific kind of messages. All recv should
// satisfy `libs.AcceptorRecvItf`.
package recvs

import (
	"bufio"
	"bytes"
	"io"
	"net"

	utils "github.com/Laisky/go-utils"
	"github.com/ugorji/go/codec"
	"go.uber.org/zap"
	"github.com/Laisky/go-fluentd/libs"
)

type FluentdRecvCfg struct {
	Addr, TagKey string
}

type FluentdRecv struct {
	*BaseRecv
	*FluentdRecvCfg
}

func NewFluentdRecv(cfg *FluentdRecvCfg) *FluentdRecv {
	utils.Logger.Info("create FluentdRecv")
	return &FluentdRecv{
		BaseRecv:       &BaseRecv{},
		FluentdRecvCfg: cfg,
	}
}

func (r *FluentdRecv) GetName() string {
	return "FluentdRecv"
}

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
		_codec = libs.NewCodec()
		dec    = codec.NewDecoder(bufio.NewReader(conn), _codec)
		dec2   *codec.Decoder
		reader *bytes.Reader
		v      = []interface{}{nil, nil, nil} // tag, time, messages
		v2     = []interface{}{nil, nil}
		msg    *libs.FluentMsg
		err    error
		tag    string
		ok     bool
		entryI interface{}
	)
	for {
		utils.Logger.Debug("wait to decode new message")
		v[2] = nil // create new map, avoid influenced by old data
		err = dec.Decode(&v)
		if err == io.EOF {
			utils.Logger.Info("connection closed", zap.String("remote", conn.RemoteAddr().String()))
			return
		} else if err != nil {
			utils.Logger.Error("decode message got error", zap.Error(err))
			return
		}

		switch v[0].(type) {
		case []byte:
			tag = string(v[0].([]byte))
		default:
			utils.Logger.Error("message[0] is not `[]byte`")
			continue
		}

		switch v[1].(type) {
		case []interface{}:
			utils.Logger.Debug("got message in format: `[]interface{}`")
			for _, entryI = range v[1].([]interface{}) {
				msg = r.msgPool.Get().(*libs.FluentMsg)
				if msg.Message, ok = entryI.([]interface{})[1].(map[string]interface{}); !ok {
					utils.Logger.Error("failed to decode message", zap.String("tag", tag))
					r.msgPool.Put(msg)
					continue
				}
				msg.Tag = tag
				r.SendMsg(msg)
			}
		case []byte:
			utils.Logger.Debug("got message in format: `[]byte`")
			if reader == nil {
				reader = bytes.NewReader(v[1].([]byte))
			} else {
				reader.Reset(v[1].([]byte))
			}

			if dec2 != nil {
				dec2.Reset(reader)
			} else {
				dec2 = codec.NewDecoder(reader, _codec)
			}

			for reader.Len() > 0 {
				v2[1] = nil
				if err = dec2.Decode(&v2); err == io.EOF {
					break
				} else if err != nil {
					utils.Logger.Error("failed to decode message")
					break
				} else {
					msg = r.msgPool.Get().(*libs.FluentMsg)
					msg.Message = v2[1].(map[string]interface{})
					msg.Tag = tag
					r.SendMsg(msg)
				}
			}
		default:
			utils.Logger.Debug("got message in format: default")
			msg = r.msgPool.Get().(*libs.FluentMsg)
			msg.Message = v[2].(map[string]interface{})
			msg.Tag = tag
			r.SendMsg(msg)
		}
	}
}

func (r *FluentdRecv) SendMsg(msg *libs.FluentMsg) {
	msg.Id = r.counter.Count()
	msg.Message[r.TagKey] = msg.Tag
	r.asyncOutChan <- msg
}
