package libs

import (
	"regexp"

	utils "github.com/Laisky/go-utils"
)

// ConcatorTagCfg configurations about how to dispatch messages
type ConcatorTagCfg struct {
	MsgKey, Identifier string
	Regexp             *regexp.Regexp
}

// LoadConcatorTagConfigs return the configurations about dispatch rules
func LoadConcatorTagConfigs() (concatorcfgs map[string]*ConcatorTagCfg) {
	concatorcfgs = map[string]*ConcatorTagCfg{}
	env := utils.Settings.GetString("env")
	for tag, tagcfgI := range utils.Settings.Get("settings.tag_filters.tenants.concator.tenants").(map[string]interface{}) {
		cfg := tagcfgI.(map[string]interface{})
		concatorcfgs[tag+"."+env] = &ConcatorTagCfg{
			MsgKey:     cfg["msg_key"].(string),
			Identifier: cfg["identifier"].(string),
			Regexp:     regexp.MustCompile(cfg["regex"].(string)),
		}
	}

	return concatorcfgs
}
