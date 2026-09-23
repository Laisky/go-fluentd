// Package recvs defines different kind of receivers.
//
// recvs are components applied in acceptor. Each recv can
// receiving specific kind of messages. All recv should
// satisfy `library.AcceptorRecvItf`.
package recvs

import (
	"context"
	"sync"

	"gofluentd/library"

	"github.com/Laisky/go-utils"
)

var json = utils.JSON

type AcceptorRecvItf interface {
	SetSyncOutChan(chan<- *library.FluentMsg)
	SetAsyncOutChan(chan<- *library.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetCounter(library.CounterIft)
	Run(context.Context)
	GetName() string
}

type BaseRecv struct {
	syncOutChan  chan<- *library.FluentMsg
	asyncOutChan chan<- *library.FluentMsg
	msgPool      *sync.Pool
	counter      library.CounterIft
}

func (r *BaseRecv) SetSyncOutChan(outchan chan<- *library.FluentMsg) {
	r.syncOutChan = outchan
}

func (r *BaseRecv) SetAsyncOutChan(outchan chan<- *library.FluentMsg) {
	r.asyncOutChan = outchan
}

func (r *BaseRecv) SetMsgPool(msgPool *sync.Pool) {
	r.msgPool = msgPool
}

func (r *BaseRecv) SetCounter(counter library.CounterIft) {
	r.counter = counter
}

// newMsg starts a new receiver-owned record. Pool contents may come from failed
// deliveries or concatenation and must never carry acknowledgement IDs forward.
func (r *BaseRecv) newMsg() *library.FluentMsg {
	msg := r.msgPool.Get().(*library.FluentMsg)
	*msg = library.FluentMsg{}
	return msg
}
