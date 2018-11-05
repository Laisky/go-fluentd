package libs

import (
	"regexp"

	utils "github.com/Laisky/go-utils"
)

// TagConfig configurations about how to dispatch messages
type TagConfig struct {
	MsgKey, Identifier string
	Regex              *regexp.Regexp
}

// LoadTagConfigs return the configurations about dispatch rules
func LoadTagConfigs() map[string]*TagConfig {
	dispatherConfigs := map[string]*TagConfig{}
	env := "." + utils.Settings.GetString("env")
	var cfg map[string]interface{}
	for tag, cfgI := range utils.Settings.Get("settings.tag_configs").(map[string]interface{}) {
		cfg = cfgI.(map[string]interface{})
		dispatherConfigs[tag+env] = &TagConfig{
			MsgKey:     cfg["msg_key"].(string),
			Identifier: cfg["identifier"].(string),
			Regex:      regexp.MustCompile(cfg["regex"].(string)),
		}
	}

	return dispatherConfigs
}
