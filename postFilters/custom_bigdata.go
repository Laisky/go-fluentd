package postFilters

import (
	"fmt"
	"strconv"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type CustomBigDataFilterCfg struct {
	Tags []string
}

// CustomBigDataFilter specific hardcoding
type CustomBigDataFilter struct {
	BaseFilter
	*CustomBigDataFilterCfg
	supportedTags map[string]struct{}
}

func NewCustomBigDataFilter(cfg *CustomBigDataFilterCfg) *CustomBigDataFilter {
	f := &CustomBigDataFilter{
		CustomBigDataFilterCfg: cfg,
	}
	f.supportedTags = map[string]struct{}{}
	for _, t := range f.Tags {
		f.supportedTags[t] = struct{}{}
	}

	utils.Logger.Info("create new CustomBigDataFilter",
		zap.Strings("tags", cfg.Tags),
	)
	return f
}

const (
	tsKey      = "@timestamp"
	timeFormat = "2006-01-02T15:04:05.000Z"
	// rowkeyTimeFormat = "2006-01-02 15:04:05"
)

var (
// loc, _ = time.LoadLocation("Asia/Shanghai")
)

func (f *CustomBigDataFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	var (
		err error
		t   time.Time
		ok  bool
	)
	if _, ok = f.supportedTags[msg.Tag]; !ok {
		return msg
	}

	if t, err = time.Parse(timeFormat, msg.Message[tsKey].(string)); err != nil {
		utils.Logger.Error("unknown format of @timestamp for bigdata",
			zap.String("tag", msg.Tag),
			zap.String(tsKey, fmt.Sprint(msg.Message[tsKey])),
			zap.Error(err),
		)
		return nil
	}

	msg.Message["rowkey"] = msg.Message["vin"].(string) + "_" + strconv.FormatInt(t.Unix(), 10)
	return msg
}
