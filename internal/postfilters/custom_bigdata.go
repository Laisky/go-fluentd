package postfilters

import (
	"strconv"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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

	log.Logger.Info("create new CustomBigDataFilter",
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

func (f *CustomBigDataFilter) Filter(msg *library.FluentMsg) *library.FluentMsg {
	if _, supported := f.supportedTags[msg.Tag]; !supported {
		return msg
	}
	timestamp, validTime := msg.Message[tsKey].(string)
	vin, validVIN := msg.Message["vin"].(string)
	parsed, err := time.Parse(timeFormat, timestamp)
	if !validTime || !validVIN || vin == "" || err != nil {
		log.Logger.Warn("discard invalid bigdata record", zap.String("tag", msg.Tag))
		f.DiscardMsg(msg)
		return nil
	}
	msg.Message["rowkey"] = vin + "_" + strconv.FormatInt(parsed.Unix(), 10)
	return msg
}
