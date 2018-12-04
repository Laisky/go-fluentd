package postFilters

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
)

type PostFilterItf interface {
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetCommittedChan(chan<- int64)

	Filter(*libs.FluentMsg) *libs.FluentMsg
	DiscardMsg(*libs.FluentMsg)
}

type BaseFilter struct {
	upstreamChan  chan *libs.FluentMsg
	committedChan chan<- int64
	msgPool       *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) SetCommittedChan(committedChan chan<- int64) {
	f.committedChan = committedChan
}

func (f *BaseFilter) DiscardMsg(msg *libs.FluentMsg) {
	f.committedChan <- msg.Id
	if msg.ExtIds != nil {
		for _, id := range msg.ExtIds {
			f.committedChan <- id
		}
	}

	msg.ExtIds = nil
	f.msgPool.Put(msg)
}
