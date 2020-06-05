package postFilters

import (
	"strings"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/zap"
)

type DefaultFilterCfg struct {
	MsgKey string
	MaxLen int
	libs.AddCfg
}

type DefaultFilter struct {
	BaseFilter
	*DefaultFilterCfg
}

func NewDefaultFilter(cfg *DefaultFilterCfg) *DefaultFilter {
	f := &DefaultFilter{
		DefaultFilterCfg: cfg,
	}
	if err := f.valid(); err != nil {
		libs.Logger.Panic("DefaultFilter invalid", zap.Error(err))
	}

	return f
}

func (f *DefaultFilter) valid() error {
	if f.MaxLen != 0 {
		libs.Logger.Info("enbale max_len")
		if f.MaxLen < 100 {
			libs.Logger.Warn("default_filter's max_len too short", zap.Int("max_len", f.MaxLen))
		}
	}

	if f.MsgKey == "" {
		f.MsgKey = "log"
		libs.Logger.Info("reset msg_key", zap.String("msg_key", f.MsgKey))
	}

	libs.Logger.Info("new default_filter",
		zap.Int("max_len", f.MaxLen),
		zap.String("msg_key", f.MsgKey),
	)
	return nil
}

func (f *DefaultFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	for k, v := range msg.Message {
		if k == "" {
			delete(msg.Message, k)
		}

		if strings.Contains(k, ".") {
			msg.Message[strings.Replace(k, ".", "__", -1)] = msg.Message[k]
			delete(msg.Message, k)
		}

		switch v := v.(type) {
		case []byte: // convert all bytes fields to string
			msg.Message[k] = string(v)
		case string:
			msg.Message[k] = v
		}

		if f.MaxLen != 0 {
			switch v := v.(type) {
			case string:
				if len(v) > f.MaxLen {
					msg.Message[k] = v[:f.MaxLen]
				}
			case []byte:
				if len(v) > f.MaxLen {
					msg.Message[k] = v[:f.MaxLen]
				}
			}
		}
	}

	libs.ProcessAdd(f.AddCfg, msg)
	return msg
}
