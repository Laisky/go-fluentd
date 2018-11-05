package acceptorFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
)

type AcceptorFilterItf interface {
	Filter(*libs.FluentMsg) *libs.FluentMsg
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
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
