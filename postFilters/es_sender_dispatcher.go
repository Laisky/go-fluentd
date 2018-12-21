package postFilters

import (
	"fmt"
	"strings"

	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
	"github.com/Laisky/go-fluentd/libs"
)

type ESDispatcherFilterCfg struct {
	Tag, TagKey string
	ReTagMap    map[string]string
}

type ESDispatcherFilter struct {
	*BaseFilter
	*ESDispatcherFilterCfg
}

// LoadReTagMap parse retag config
// app.spring.{env}: es-general -> {app.spring.sit: es-general}
func LoadReTagMap(env string, mapi interface{}) map[string]string {
	retagMap := map[string]string{}
	for tag, retagi := range mapi.(map[string]interface{}) {
		retagMap[strings.Replace(tag, "{env}", env, -1)] = retagi.(string)
	}

	return retagMap
}

func NewESDispatcherFilter(cfg *ESDispatcherFilterCfg) *ESDispatcherFilter {
	return &ESDispatcherFilter{
		BaseFilter:            &BaseFilter{},
		ESDispatcherFilterCfg: cfg,
	}
}

func (f *ESDispatcherFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	if msg.Message[f.TagKey].(string) == "" {
		utils.Logger.Error("msg tag should not empty", zap.String("msg", fmt.Sprintf("%+v", msg)))
		f.DiscardMsg(msg)
		return nil
	}
	var ok bool
	if msg.Tag, ok = f.ReTagMap[msg.Message[f.TagKey].(string)]; !ok {
		utils.Logger.Error("tag not exists in retagmap", zap.String("tag", msg.Message[f.TagKey].(string)))
		f.DiscardMsg(msg)
		return nil
	}

	utils.Logger.Debug("change msg tag",
		zap.String("old_tag", msg.Message[f.TagKey].(string)),
		zap.String("new_tag", msg.Tag))
	return msg
}
