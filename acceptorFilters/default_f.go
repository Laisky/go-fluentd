package acceptorFilters

import (
	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type DefaultFilterCfg struct {
	RemoveEmptyTag, RemoveUnsupportTag bool
	SupportedTags                      []string
	Env                                string
}

type DefaultFilter struct {
	*BaseFilter
	*DefaultFilterCfg
	tags map[string]struct{}
}

func NewDefaultFilter(cfg *DefaultFilterCfg) *DefaultFilter {
	utils.Logger.Info("NewDefaultFilter")

	f := &DefaultFilter{
		BaseFilter:       &BaseFilter{},
		DefaultFilterCfg: cfg,
		tags:             map[string]struct{}{},
	}

	for _, tag := range cfg.SupportedTags {
		f.tags[tag+"."+cfg.Env] = struct{}{}
	}

	return f
}

func (f *DefaultFilter) IsTagInConfigs(tag string) (ok bool) {
	_, ok = f.tags[tag]
	return ok
}

func (f *DefaultFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if f.RemoveEmptyTag && msg.Tag == "" {
		utils.Logger.Warn("remove empty tag", zap.String("tag", msg.Tag))
		f.DiscardMsg(msg)
		return nil
	}

	if f.RemoveUnsupportTag && !f.IsTagInConfigs(msg.Tag) {
		utils.Logger.Warn("remove unsupported tag", zap.String("tag", msg.Tag))
		f.DiscardMsg(msg)
		return nil
	}

	return msg
}
