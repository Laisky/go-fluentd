package postfilters

import (
	"strings"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/zap"
)

type ESDispatcherFilterCfg struct {
	TagKey   string
	Tags     []string
	ReTagMap map[string]string
}

type ESDispatcherFilter struct {
	BaseFilter
	*ESDispatcherFilterCfg
	supportedTags map[string]struct{}
}

// LoadReTagMap parse retag config
// app.spring.{env}: es-general -> {app.spring.sit: es-general}
func LoadReTagMap(env string, mapi interface{}) map[string]string {
	retagMap := map[string]string{}
	for tag, retagi := range mapi.(map[string]interface{}) {
		retagMap[strings.Replace(tag, "{env}", env, -1)] = strings.Replace(retagi.(string), "{env}", env, -1)
	}

	return retagMap
}

func NewESDispatcherFilter(cfg *ESDispatcherFilterCfg) *ESDispatcherFilter {
	log.Logger.Info("new ESDispatcherFilter",
		zap.Strings("tags", cfg.Tags))
	f := &ESDispatcherFilter{
		ESDispatcherFilterCfg: cfg,
	}

	f.supportedTags = map[string]struct{}{}
	for _, t := range f.Tags {
		f.supportedTags[t] = struct{}{}
	}

	return f
}

func (f *ESDispatcherFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	if _, supported := f.supportedTags[msg.Tag]; !supported {
		return msg
	}
	origin, valid := msg.Message[f.TagKey].(string)
	target, mapped := f.ReTagMap[origin]
	if !valid || origin == "" || !mapped || target == "" {
		log.Logger.Warn("discard record with unmapped origin tag", zap.String("tag", msg.Tag))
		// Keep the journal tag intact until the discard reaches the commit writer.
		f.DiscardMsg(msg)
		return nil
	}
	msg.Tag = target
	return msg
}
