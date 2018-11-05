package postFilters

import (
	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
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
	switch msg.Message[f.MsgKey].(type) {
	case []byte:
	default:
		utils.Logger.Warn("incorrect message key",
			zap.String("tag", msg.Tag),
			zap.String("msg_key", f.MsgKey))
		return msg
	}

	if len(msg.Message[f.MsgKey].([]byte)) > f.MaxLen {
		msg.Message[f.MsgKey] = msg.Message[f.MsgKey].([]byte)[:f.MaxLen]
	}

	return msg
}
