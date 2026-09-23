package postfilters

import (
	"fmt"
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
	var (
		err error
		t   time.Time
		ok  bool
	)
	if _, ok = f.supportedTags[msg.Tag]; !ok {
		return msg
	}

	timestamp, validTime := textField(msg.Message[tsKey])
	vin, validVIN := textField(msg.Message["vin"])
	if !validTime || !validVIN || vin == "" {
		f.DiscardMsg(msg)
		return nil
	}
	if t, err = time.Parse(timeFormat, timestamp); err != nil {
		log.Logger.Error("unknown format of @timestamp for bigdata",
			zap.String("tag", msg.Tag),
			zap.String(tsKey, fmt.Sprint(msg.Message[tsKey])),
			zap.Error(err),
		)
		f.DiscardMsg(msg)
		return nil
	}

	msg.Message["rowkey"] = vin + "_" + strconv.FormatInt(t.Unix(), 10)
	return msg
}
