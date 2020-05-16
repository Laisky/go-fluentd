package postFilters

import (
	"fmt"
	"strings"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/zap"
)

type DefaultFilterCfg struct {
	MsgKey string
	MaxLen int
}

type DefaultFilter struct {
	BaseFilter
	*DefaultFilterCfg
}

func NewDefaultFilter(cfg *DefaultFilterCfg) *DefaultFilter {
	return &DefaultFilter{
		DefaultFilterCfg: cfg,
	}
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

		switch v := v.(type) {
		case string:
			if len(v) > f.MaxLen {
				msg.Message[k] = v[:f.MaxLen]
			}
		}
	}

	// DefaultSearchField must exists, let kibana can display this msg
	switch msg.Message[libs.DefaultFieldForMessage].(type) {
	case string:
	case nil:
		// Kibana needs `DefaultFieldForMessage` to display and search
		msg.Message[libs.DefaultFieldForMessage] = ""
	default:
		libs.Logger.Error("unknown message type", zap.String("message", fmt.Sprint(msg.Message[libs.DefaultFieldForMessage])))
	}

	return msg
}
