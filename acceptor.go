package concator

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
	"github.com/pkg/errors"
	"github.com/ugorji/go/codec"
)

// FluentMsg is the structure of fluent message
type FluentMsg struct {
	Tag     string
	Message map[string]interface{}
	Id      int64
	extIds  []int64
}

// Acceptor listening tcp connection, and decode messages
type Acceptor struct {
	addr    string
	msgChan chan *FluentMsg
	msgPool *sync.Pool
	journal *Journal
	couter  *utils.Counter
}

// NewAcceptor create new Acceptor
func NewAcceptor(addr string, msgpool *sync.Pool, journal *Journal) *Acceptor {
	utils.Logger.Info("create Acceptor")
	return &Acceptor{
		addr:    addr,
		msgChan: make(chan *FluentMsg, 5000),
		msgPool: msgpool,
		journal: journal,
	}
}

// Run starting acceptor to listening and receive messages,
// you can use `acceptor.MessageChan()` to load messages`
func (a *Acceptor) Run() (err error) {
	utils.Logger.Info("listen on tcp", zap.String("addr", a.addr))
	ln, err := net.Listen("tcp", a.addr)
	if err != nil {
		return errors.Wrap(err, "try to bind addr got error")
	}

	go func() {
		// legacy
		utils.Logger.Info("process legacy data...")
		maxId, err := a.journal.ProcessLegacyMsg(a.msgPool, a.msgChan)
		if err != nil {
			panic(fmt.Errorf("try to process legacy messages got error: %+v", err))
		}
		a.couter = utils.NewCounterFromN(maxId)

		utils.Logger.Info("starting to listening...")
		for {
			conn, err := ln.Accept()
			if err != nil {
				utils.Logger.Error("try to accept connection got error", zap.Error(err))
				continue
			}

			utils.Logger.Info("accept new connection", zap.String("remote", conn.RemoteAddr().String()))
			go func(conn net.Conn) {
				a.decodeMsg(conn)
			}(conn)
		}
	}()

	return nil
}

func (a *Acceptor) decodeMsg(conn net.Conn) {
	defer conn.Close()
	var (
		_codec = NewCodec()
		dec    = codec.NewDecoder(bufio.NewReader(conn), _codec)
		dec2   *codec.Decoder
		reader *bytes.Reader
		v      = []interface{}{nil, nil, nil} // tag, time, messages
		v2     = []interface{}{nil, nil}
		msg    *FluentMsg
		err    error
		tag    string
		ok     bool
	)
	for {
		v[2] = nil // create new map, avoid influenced by old data
		err = dec.Decode(&v)
		if err == io.EOF {
			utils.Logger.Info("connection closed", zap.String("remote", conn.RemoteAddr().String()))
			return
		} else if err != nil {
			utils.Logger.Error("decode message got error", zap.Error(err))
			return
		}

		tag = string(v[0].([]byte))
		switch v[1].(type) {
		case []interface{}:
			utils.Logger.Debug("got message in format: `[]interface{}`")
			for _, _entry := range v[1].([]interface{}) {
				msg = a.msgPool.Get().(*FluentMsg)
				if msg.Message, ok = _entry.([]interface{})[1].(map[string]interface{}); !ok {
					utils.Logger.Error("failed to decode message", zap.String("tag", tag))
					a.msgPool.Put(msg)
					continue
				}
				msg.Tag = tag
				a.SendMsg(msg)
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
					msg = a.msgPool.Get().(*FluentMsg)
					msg.Message = v2[1].(map[string]interface{})
					msg.Tag = tag
					a.SendMsg(msg)
				}
			}
		default:
			utils.Logger.Debug("got message in format: default")
			msg = a.msgPool.Get().(*FluentMsg)
			msg.Message = v[2].(map[string]interface{})
			msg.Tag = tag
			a.SendMsg(msg)
		}
	}
}

func (a *Acceptor) SendMsg(msg *FluentMsg) {
	msg.Id = a.couter.Count()
	if msg.Id == 100000000 {
		a.couter.Set(0)
	}
	a.msgChan <- msg
}

// MessageChan return the message chan that received by acceptor
func (a *Acceptor) MessageChan() <-chan *FluentMsg {
	return a.msgChan
}
