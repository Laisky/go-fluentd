package postFilters

import (
	"github.com/Laisky/go-fluentd/libs"
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
	for k, v := range msg.Message {
		switch v.(type) {
		case []byte: // convert all bytes fields to string
			msg.Message[k] = string(v.([]byte))
		}
	}

	return msg
}
