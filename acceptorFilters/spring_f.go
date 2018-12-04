package acceptorFilters

import (
	"regexp"
	"strings"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type SpringReTagRule struct {
	NewTag string
	Regexp *regexp.Regexp
}

type SpringFilterCfg struct {
	Tag, Env, MsgKey string
	Rules            []*SpringReTagRule
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

func (f *SpringFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	switch msg.Message[f.MsgKey].(type) {
	case []byte:
	default:
		return msg
	}
	// retag spring to cp/bot/app.spring
	for _, rule := range f.Rules {
		if rule.Regexp.Match(msg.Message[f.MsgKey].([]byte)) {
			msg.Tag = rule.NewTag
			f.upstreamChan <- msg
			return nil
		}
	}

	return msg
}
