package acceptorfilters

import (
	"sync"

	"gofluentd/library"
)

type AcceptorFilterItf interface {
	SetUpstream(chan *library.FluentMsg)
	SetMsgPool(*sync.Pool)

	Filter(*library.FluentMsg) *library.FluentMsg
	DiscardMsg(*library.FluentMsg)
}

type BaseFilter struct {
	upstreamChan chan *library.FluentMsg
	msgPool      *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *library.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) DiscardMsg(msg *library.FluentMsg) {
	msg.ExtIds = nil
	f.msgPool.Put(msg)
}
