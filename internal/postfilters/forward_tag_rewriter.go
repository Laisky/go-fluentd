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

	return &ForwardTagRewriterFilter{
		ForwardTagRewriterFilterCfg: cfg,
		tagWithoutEnv:               tagPrefix(cfg.Tag),
	}
}

func (f *ForwardTagRewriterFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	origin, ok := msg.Message[f.TagKey].(string)
	if !ok {
		return msg
	}
	idx := strings.LastIndexByte(origin, '.')
	if idx <= 0 || idx == len(origin)-1 {
		return msg
	}
	msg.Tag = f.tagWithoutEnv + "." + origin[idx+1:]
	// log.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	return msg
}

func tagPrefix(tag string) string {
	if i := strings.LastIndexByte(tag, '.'); i >= 0 {
		return tag[:i]
	}
	return tag
}
