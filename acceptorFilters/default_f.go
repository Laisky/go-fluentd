package acceptorFilters

import (
	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/zap"
)

type DefaultFilterCfg struct {
	RemoveEmptyTag, RemoveUnsupportTag bool
	SupportedTags                      []string
	Name, Env                          string
}

type DefaultFilter struct {
	*BaseFilter
	*DefaultFilterCfg
	tags map[string]struct{}
}

func NewDefaultFilter(cfg *DefaultFilterCfg) *DefaultFilter {
	libs.Logger.Info("NewDefaultFilter")

	f := &DefaultFilter{
		BaseFilter:       &BaseFilter{},
		DefaultFilterCfg: cfg,
		tags:             map[string]struct{}{},
	}

	for _, tag := range cfg.SupportedTags {
		f.tags[tag+"."+f.Env] = struct{}{}
	}

	return f
}

func (f *DefaultFilter) GetName() string {
	return f.Name
}

func (f *DefaultFilter) IsTagInConfigs(tag string) (ok bool) {
	_, ok = f.tags[tag]
	return ok
}

func (f *DefaultFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if f.RemoveEmptyTag && msg.Tag == "" {
		libs.Logger.Warn("discard log since empty tag", zap.String("tag", msg.Tag))
		f.DiscardMsg(msg)
		return nil
	}

	if f.RemoveUnsupportTag && !f.IsTagInConfigs(msg.Tag) {
		libs.Logger.Warn("discard log since unsupported tag", zap.String("tag", msg.Tag))
		f.DiscardMsg(msg)
		return nil
	}

	return msg
}
