package postfilters

import (
	"strings"

	"gofluentd/library"

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
	library.Logger.Info("new ForwardTagRewriterFilter",
		zap.String("tag", cfg.Tag))

	return &ForwardTagRewriterFilter{
		ForwardTagRewriterFilterCfg: cfg,
		tagWithoutEnv:               strings.Split(cfg.Tag, ".")[0],
	}
}

func (f *ForwardTagRewriterFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	env := strings.Split(msg.Message[f.TagKey].(string), ".")[1]
	msg.Tag = f.tagWithoutEnv + "." + env
	// library.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	return msg
}
