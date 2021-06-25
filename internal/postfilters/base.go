package postfilters

import (
	"sync"

	"gofluentd/library"
)

type PostFilterItf interface {
	SetUpstream(chan *library.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetWaitCommitChan(chan<- *library.FluentMsg)

	Filter(*library.FluentMsg) *library.FluentMsg
	DiscardMsg(*library.FluentMsg)
}

type BaseFilter struct {
	upstreamChan   chan *library.FluentMsg
	waitCommitChan chan<- *library.FluentMsg
	msgPool        *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *library.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) SetWaitCommitChan(waitCommitChan chan<- *library.FluentMsg) {
	f.waitCommitChan = waitCommitChan
}

func (f *BaseFilter) DiscardMsg(msg *library.FluentMsg) {
	f.waitCommitChan <- msg
}
