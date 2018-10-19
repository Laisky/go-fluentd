package postFilters

import (
	"github.com/Laisky/go-concator/libs"
)

type DefaultFilterCfg struct {
	MsgKey string
	MaxLen int
}

type DefaultFilter struct {
	*BaseFilter
	*DefaultFilterCfg
}

func NewDefaultFilter(cfg *DefaultFilterCfg) *DefaultFilter {
	return &DefaultFilter{
		BaseFilter:       &BaseFilter{},
		DefaultFilterCfg: cfg,
	}
}

func (f *DefaultFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if len(msg.Message[f.MsgKey].([]byte)) > f.MaxLen {
		msg.Message[f.MsgKey] = msg.Message[f.MsgKey].([]byte)[:f.MaxLen]
	}

	return msg
}
