package acceptorFilters

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
)

type AcceptorFilterItf interface {
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)

	Filter(*libs.FluentMsg) *libs.FluentMsg
	DiscardMsg(*libs.FluentMsg)
}

type BaseFilter struct {
	upstreamChan chan *libs.FluentMsg
	msgPool      *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) DiscardMsg(msg *libs.FluentMsg) {
	msg.ExtIds = nil
	f.msgPool.Put(msg)
}
