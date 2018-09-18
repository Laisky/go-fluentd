package concator

import (
	"bufio"
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
		dec = codec.NewDecoder(bufio.NewReader(conn), NewCodec())
		v   = []interface{}{nil, nil, nil} // tag, time, messages
		msg *FluentMsg
		err error
	)
	for {
		msg = a.msgPool.Get().(*FluentMsg)
		msg.Message = make(map[string]interface{}) // create new map, avoid influenced by old data
		v[2] = msg.Message                         // v[2] is a map
		err = dec.Decode(&v)
		if err == io.EOF {
			utils.Logger.Info("connection closed", zap.String("remote", conn.RemoteAddr().String()))
			return
		} else if err != nil {
			utils.Logger.Error("decode message got error", zap.Error(err))
			return
		}

		msg.Tag = string(v[0].([]byte))
		msg.Id = a.couter.Count()

		if msg.Id == 100000000 {
			a.couter.Set(0)
		}

		a.msgChan <- msg
	}
}

// MessageChan return the message chan that received by acceptor
func (a *Acceptor) MessageChan() <-chan *FluentMsg {
	return a.msgChan
}
