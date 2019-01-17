package postFilters

import (
	"strings"

	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
	"github.com/Laisky/go-fluentd/libs"
)

type ForwardTagRewriterFilterCfg struct {
	TagKey, Tag string
}

// ForwardTagRewriterFilter rewrite tag for msgs received by forward-recv.
type ForwardTagRewriterFilter struct {
	*BaseFilter
	*ForwardTagRewriterFilterCfg
}

func NewForwardTagRewriterFilter(cfg *ForwardTagRewriterFilterCfg) *ForwardTagRewriterFilter {
	utils.Logger.Info("new ForwardTagRewriterFilter",
		zap.String("tag", cfg.Tag))

	return &ForwardTagRewriterFilter{
		BaseFilter:                  &BaseFilter{},
		ForwardTagRewriterFilterCfg: cfg,
	}
}

func (f *ForwardTagRewriterFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	env := strings.Split(msg.Message[f.TagKey].(string), ".")[1]
	msg.Tag = strings.Split(msg.Tag, ".")[0] + "." + env
	utils.Logger.Debug("rewrite msg tag", zap.String("new_tag", msg.Tag))
	return msg
}
