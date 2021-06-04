package postfilters

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
)

type PostFilterItf interface {
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
	SetWaitCommitChan(chan<- *libs.FluentMsg)

	Filter(*libs.FluentMsg) *libs.FluentMsg
	DiscardMsg(*libs.FluentMsg)
}

type BaseFilter struct {
	upstreamChan   chan *libs.FluentMsg
	waitCommitChan chan<- *libs.FluentMsg
	msgPool        *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseFilter) SetWaitCommitChan(waitCommitChan chan<- *libs.FluentMsg) {
	f.waitCommitChan = waitCommitChan
}

func (f *BaseFilter) DiscardMsg(msg *libs.FluentMsg) {
	f.waitCommitChan <- msg
}
