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
	// Normalize from a stable snapshot: inserting renamed keys while ranging
	// can revisit them or resurrect the old key. Existing canonical keys win.
	original := msg.Message
	normalized := make(map[string]interface{}, len(original))
	keys := make([]string, 0, len(original))
	for key := range original {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "" {
			continue
		}
		target := strings.ReplaceAll(key, ".", "__")
		if target != key {
			if _, exists := original[target]; exists {
				continue
			}
			if _, exists := normalized[target]; exists {
				continue
			}
		}
		value := original[key]
		if raw, ok := value.([]byte); ok {
			value = string(raw)
		}
		if text, ok := value.(string); ok && f.MaxLen > 0 && len(text) > f.MaxLen {
			value = text[:f.MaxLen]
		}
		normalized[target] = value
	}
	msg.Message = normalized

	library.ProcessAdd(f.AddCfg, msg)
	return msg
}
