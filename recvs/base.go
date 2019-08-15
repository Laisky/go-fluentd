// Package recvs defines different kind of receivers.
//
// recvs are components applied in acceptor. Each recv can
// receiving specific kind of messages. All recv should
// satisfy `libs.AcceptorRecvItf`.
package recvs

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	jsoniter "github.com/json-iterator/go"
)

const (
	// RandomValOperator set this val in meta will replaced by random string
	RandomValOperator = "@RANDOM_STRING"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type AcceptorRecvItf interface {
	SetSyncOutChan(chan<- *libs.FluentMsg)
	SetAsyncOutChan(chan<- *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetCounter(libs.CounterIft)
	Run()
	GetName() string
}

type BaseRecv struct {
	syncOutChan  chan<- *libs.FluentMsg
	asyncOutChan chan<- *libs.FluentMsg
	msgPool      *sync.Pool
	counter      libs.CounterIft
}

func (r *BaseRecv) SetSyncOutChan(outchan chan<- *libs.FluentMsg) {
	r.syncOutChan = outchan
}

func (r *BaseRecv) SetAsyncOutChan(outchan chan<- *libs.FluentMsg) {
	r.asyncOutChan = outchan
}

func (r *BaseRecv) SetMsgPool(msgPool *sync.Pool) {
	r.msgPool = msgPool
}

func (r *BaseRecv) SetCounter(counter libs.CounterIft) {
	r.counter = counter
}
