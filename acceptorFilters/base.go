package acceptorFilters

import (
	"github.com/Laisky/go-concator/libs"
)

type AcceptorFilterItf interface {
	Filter(*libs.FluentMsg) *libs.FluentMsg
	SetUpstream(chan *libs.FluentMsg)
}

type BaseFilter struct {
	upstreamChan chan *libs.FluentMsg
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}
