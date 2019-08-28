package postFilters

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
)

type PostFilterItf interface {
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetCommittedChan(chan<- *libs.FluentMsg)

	Filter(*libs.FluentMsg) *libs.FluentMsg
	DiscardMsg(*libs.FluentMsg)
}

type BaseFilter struct {
	upstreamChan  chan *libs.FluentMsg
	committedChan chan<- *libs.FluentMsg
	msgPool       *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) SetCommittedChan(committedChan chan<- *libs.FluentMsg) {
	f.committedChan = committedChan
}

func (f *BaseFilter) DiscardMsg(msg *libs.FluentMsg) {
	f.committedChan <- msg
}
