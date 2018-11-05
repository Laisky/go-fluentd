package acceptorFilters

import (
	"regexp"
	"strings"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type SpringReTagRule struct {
	MsgKey, NewTag string
	Regexp         *regexp.Regexp
}

type SpringFilterCfg struct {
	MsgPool  *sync.Pool
	Tag, Env string
	Rules    []*SpringReTagRule
}

type SpringFilter struct {
	*BaseFilter
	*SpringFilterCfg
}

// ParseSpringRules parse settings to rules
func ParseSpringRules(cfg []interface{}) []*SpringReTagRule {
	rules := []*SpringReTagRule{}
	for _, ruleI := range cfg {
		rule := ruleI.(map[interface{}]interface{})
		rules = append(rules, &SpringReTagRule{
			MsgKey: rule["msg_key"].(string),
			NewTag: rule["new_tag"].(string),
			Regexp: regexp.MustCompile(rule["regexp"].(string)),
		})
	}

	return rules
}

func NewSpringFilter(cfg *SpringFilterCfg) *SpringFilter {
	utils.Logger.Info("NewSpringFilter",
		zap.String("tag", cfg.Tag))

	f := &SpringFilter{
		BaseFilter:      &BaseFilter{},
		SpringFilterCfg: cfg,
	}

	// prepare rules new tag
	for _, rule := range f.Rules {
		rule.NewTag = strings.Replace(rule.NewTag, "{env}", f.Env, -1)
	}

	return f
}

func (f *SpringFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	// retag spring to cp/bot/app.spring
	for _, rule := range f.Rules {
		switch msg.Message[rule.MsgKey].(type) {
		case []byte:
			if rule.Regexp.Match(msg.Message[rule.MsgKey].([]byte)) {
				msg.Tag = rule.NewTag
				f.upstreamChan <- msg
				return nil
			}
		}
	}

	return msg
}
