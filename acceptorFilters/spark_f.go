package acceptorFilters

import (
	"regexp"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type SparkFilterCfg struct {
	IgnoreRegex *regexp.Regexp
	MsgKey, Tag string
}

// SparkFilter filter spark messages.
// some old spark messages need tobe discard
type SparkFilter struct {
	*BaseFilter
	*SparkFilterCfg
}

func NewSparkFilter(cfg *SparkFilterCfg) *SparkFilter {
	utils.Logger.Info("NewSparkFilter",
		zap.String("regex", cfg.IgnoreRegex.String()),
		zap.String("tag", cfg.Tag))

	return &SparkFilter{
		BaseFilter:     &BaseFilter{},
		SparkFilterCfg: cfg,
	}
}

func (f *SparkFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	// discard some format
	if f.IgnoreRegex.Match(msg.Message[f.MsgKey].([]byte)) {
		f.msgPool.Put(msg)
		return nil
	}

	return msg
}
