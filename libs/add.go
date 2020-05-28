package libs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Laisky/go-utils"
)

const (
	// VariableRandomString set this val in meta will replaced by random string
	VariableRandomString = "@RANDOM_STRING"
)

var keyReplaceRegexp = regexp.MustCompile(`\%\{([\w\-_]+)\}`)

type AddCfg map[string]map[string]interface{}

func replaceByKey(msg *FluentMsg, v string) string {
	var (
		keys = map[string]struct{}{}
		key  string
		ok   bool
		vi   interface{}
	)
	for _, grp := range keyReplaceRegexp.FindAllStringSubmatch(v, -1) {
		if len(grp) < 2 || grp[1] == "" {
			continue
		}

		key = grp[1]
		// already replaced this key
		if _, ok = keys[key]; ok {
			continue
		}

		switch key {
		case VariableRandomString:
			vi = utils.RandomStringWithLength(8)
		default:
			if vi, ok = msg.Message[key]; !ok {
				vi = ""
			}
		}

		keys[key] = struct{}{}
		v = strings.ReplaceAll(v, "%{"+key+"}", fmt.Sprint(vi))
	}

	return v
}

// ParseAddCfg load auto config
//
// config file like:
// ::
//    add:
//      <tag>:
//        <key>: <val>
//      app.{env}:
//        key: %{key2}-xx
//
func ParseAddCfg(env string, cfg interface{}) AddCfg {
	ret := AddCfg{}
	if cfg == nil {
		return ret
	}

	utils.FallBack(func() interface{} {
		for tag, vi := range cfg.(map[string]interface{}) {
			tag = strings.ReplaceAll(tag, "{env}", env)
			if _, ok := ret[tag]; !ok {
				ret[tag] = map[string]interface{}{}
			}

			for nk, nvi := range vi.(map[string]interface{}) {
				ret[tag][nk] = nvi
			}
		}

		return nil
	}, nil)

	return ret
}

// ProcessAdd change in-place msg to apply add config
func ProcessAdd(addCfg AddCfg, msg *FluentMsg) {
	var ok bool
	if _, ok = addCfg[msg.Tag]; ok {
		for k, vi := range addCfg[msg.Tag] {
			switch vi := vi.(type) {
			case nil:
				delete(msg.Message, k)
				continue
			case string:
				msg.Message[k] = replaceByKey(msg, vi)
			case []byte:
				msg.Message[k] = replaceByKey(msg, string(vi))
			default:
				msg.Message[k] = vi
			}
		}
	}
}
