package postfilters

import (
	"fmt"
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
	var ok bool
	if _, ok = f.supportedTags[msg.Tag]; !ok {
		return msg
	}

	origin, valid := textField(msg.Message[f.TagKey])
	target, configured := f.ReTagMap[origin]
	if !valid || origin == "" || !configured || target == "" {
		log.Logger.Warn("discard log with invalid route", zap.String("tag", fmt.Sprint(msg.Message[f.TagKey])))
		// Keep the original journal route until the rejected record is committed.
		f.DiscardMsg(msg)
		return nil
	}
	msg.Tag = target

	return msg
}
