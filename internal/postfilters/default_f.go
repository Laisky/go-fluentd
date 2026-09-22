package postfilters

import (
	"sort"
	"strings"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/zap"
)

type DefaultFilterCfg struct {
	MsgKey string
	MaxLen int
	library.AddCfg
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
		log.Logger.Panic("DefaultFilter invalid", zap.Error(err))
	}

	return f
}

func (f *DefaultFilter) valid() error {
	if f.MaxLen != 0 {
		log.Logger.Info("enbale max_len")
		if f.MaxLen < 100 {
			log.Logger.Warn("default_filter's max_len too short", zap.Int("max_len", f.MaxLen))
		}
	}

	if f.MsgKey == "" {
		f.MsgKey = "log"
		log.Logger.Info("reset msg_key", zap.String("msg_key", f.MsgKey))
	}

	log.Logger.Info("new default_filter",
		zap.Int("max_len", f.MaxLen),
		zap.String("msg_key", f.MsgKey),
	)
	return nil
}

func (f *DefaultFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	keys := make([]string, 0, len(msg.Message))
	for key := range msg.Message {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	normalized := make(map[string]interface{}, len(msg.Message))
	for _, key := range keys {
		if key == "" {
			continue
		}
		target := strings.ReplaceAll(key, ".", "__")
		if target != key {
			if _, exists := msg.Message[target]; exists {
				continue
			}
		}
		value := msg.Message[key]
		if b, ok := value.([]byte); ok {
			value = string(b)
		}
		if text, ok := value.(string); ok && f.MaxLen > 0 && len(text) > f.MaxLen {
			value = text[:f.MaxLen]
		}
		normalized[target] = value
	}
	// Preserve the caller's map identity.
	clear(msg.Message)
	for key, value := range normalized {
		msg.Message[key] = value
	}
	library.ProcessAdd(f.AddCfg, msg)
	return msg
}
