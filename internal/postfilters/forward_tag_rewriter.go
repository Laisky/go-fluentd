package postfilters

import (
	"strings"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/zap"
)

type ForwardTagRewriterFilterCfg struct {
	TagKey, Tag string
}

// ForwardTagRewriterFilter rewrite tag for msgs received by forward-recv.
// for example, change `forward-wechat.perf` -> `forward-wechat.prod`.
type ForwardTagRewriterFilter struct {
	BaseFilter
	*ForwardTagRewriterFilterCfg

	tagWithoutEnv string
}

func NewForwardTagRewriterFilter(cfg *ForwardTagRewriterFilterCfg) *ForwardTagRewriterFilter {
	log.Logger.Info("new ForwardTagRewriterFilter",
		zap.String("tag", cfg.Tag))

	prefix := cfg.Tag
	if idx := strings.LastIndexByte(prefix, '.'); idx >= 0 {
		prefix = prefix[:idx]
	}
	return &ForwardTagRewriterFilter{
		ForwardTagRewriterFilterCfg: cfg,
		tagWithoutEnv:               prefix,
	}
}

func (f *ForwardTagRewriterFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	origin, ok := textField(msg.Message[f.TagKey])
	idx := strings.LastIndexByte(origin, '.')
	if !ok || idx <= 0 || idx == len(origin)-1 {
		f.DiscardMsg(msg)
		return nil
	}
	msg.Tag = f.tagWithoutEnv + "." + origin[idx+1:]
	// log.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	return msg
}
