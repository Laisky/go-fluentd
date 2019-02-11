package postFilters

import (
	"fmt"
	"strings"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
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

const DefaultSearchField = "message"

func (f *DefaultFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	for k, v := range msg.Message {
		if k == "" {
			delete(msg.Message, k)
		}

		if strings.Contains(k, ".") {
			msg.Message[strings.Replace(k, ".", "__", -1)] = msg.Message[k]
			delete(msg.Message, k)
		}

		switch v.(type) {
		case []byte: // convert all bytes fields to string
			msg.Message[k] = string(v.([]byte))
		}
	}

	// DefaultSearchField must exists, let kibana can display this msg
	switch msg.Message[DefaultSearchField].(type) {
	case string:
	case nil:
		msg.Message[DefaultSearchField] = ""
	default:
		utils.Logger.Error("unknown message type", zap.String("message", fmt.Sprint(msg.Message[DefaultSearchField])))
	}

	return msg
}
