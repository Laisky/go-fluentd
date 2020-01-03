package acceptorFilters

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type SpringReTagRule struct {
	NewTag string
	Regexp *regexp.Regexp
}

type SpringFilterCfg struct {
	Name, Tag, Env, MsgKey, TagKey string
	Rules                          []*SpringReTagRule
}

type SpringFilter struct {
	*BaseFilter
	*SpringFilterCfg
}

// ParseSpringRules parse settings to rules
func ParseSpringRules(env string, cfg []interface{}) []*SpringReTagRule {
	rules := []*SpringReTagRule{}
	for _, ruleI := range cfg {
		rule := ruleI.(map[interface{}]interface{})
		rules = append(rules, &SpringReTagRule{
			NewTag: strings.Replace(rule["new_tag"].(string), "{env}", env, -1),
			Regexp: regexp.MustCompile(rule["regexp"].(string)),
		})
	}

	return rules
}

func NewSpringFilter(cfg *SpringFilterCfg) *SpringFilter {
	utils.Logger.Info("NewSpringFilter",
		zap.String("tag", cfg.Tag))

	return &SpringFilter{
		BaseFilter:      &BaseFilter{},
		SpringFilterCfg: cfg,
	}
}

func (f *SpringFilter) GetName() string {
	return f.Name
}

func (f *SpringFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	switch msg.Message[f.MsgKey].(type) {
	case []byte:
	case string:
		msg.Message[f.MsgKey] = []byte(msg.Message[f.MsgKey].(string))
	default:
		utils.Logger.Warn("discard log since unknown type of msg",
			zap.String("tag", msg.Tag),
			zap.String("msg", fmt.Sprint(msg.Message[f.MsgKey])))
		f.DiscardMsg(msg)
		return nil
	}
	// retag spring to cp/bot/app.spring
	for _, rule := range f.Rules {
		if rule.Regexp.Match(msg.Message[f.MsgKey].([]byte)) {
			msg.Tag = rule.NewTag
			msg.Message[f.TagKey] = msg.Tag
			f.upstreamChan <- msg
			return nil
		}
	}

	return msg
}
