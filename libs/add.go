package libs

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/Laisky/go-utils"
)

const (
	// variableRandomString set this val in meta will replaced by random string
	variableRandomString = "@str"
	variableMsgID        = "@id"
	variableMsgTag       = "@tag"
	variableNow          = "@now"
	variableNowUnix      = "@unix"
)

var keyReplaceRegexp = regexp.MustCompile(`\%\{(@?[\w\-_]+)\}`)

type AddCfg map[string][]map[string]interface{}

// ReplaceStrByMsg replace variable in v by lib.FluentMsg
//
// * `%{key}`     ->    `msg.Message["key"]`
// * `%{a.b}`     ->    `msg.Message["a"]["b"]`
// * `%{@tag}`    ->    `msg.Tag`
// * `%{@id}`     ->    `msg.ID`
// * `%{@str}`    ->    `<random_string>`
// * `%{@now}`    ->    `2006-01-02T15:04:05Z07:00`
// * `%{@unix}`   ->    `1590722923`
func ReplaceStrByMsg(msg *FluentMsg, v string) string {
	var (
		keys   = map[string]struct{}{}
		key    string
		ok     bool
		newVal interface{}
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
		case variableRandomString:
			newVal = utils.RandomStringWithLength(8)
		case variableMsgTag:
			newVal = msg.Tag
		case variableMsgID:
			newVal = msg.Id
		case variableNow:
			newVal = utils.Clock.GetUTCNow().Format(time.RFC3339)
		case variableNowUnix:
			newVal = utils.Clock.GetUTCNow().Unix()
		default:
			if newVal, ok = msg.Message[key]; !ok {
				// replace val from map
				if strings.Contains(key, ".") {
					msg.Message[key] = GetValFromMap(msg, key)
				}

				newVal = ""
			}
		}

		keys[key] = struct{}{}
		v = strings.ReplaceAll(v, "%{"+key+"}", fmt.Sprint(newVal))
	}

	return v
}

// ParseAddCfg load auto config
//
// config file like:
// ::
//    add:
//      <tag>:
//        - <key>: <val>
//      app.{env}:
//        - key: %{key2}-xx
//
func ParseAddCfg(env string, cfg interface{}) AddCfg {
	ret := AddCfg{}
	if cfg == nil {
		return ret
	}

	utils.FallBack(func() interface{} {
		for tag, items := range cfg.(map[string]interface{}) {
			tag = strings.ReplaceAll(tag, "{env}", env)
			if _, ok := ret[tag]; !ok {
				ret[tag] = []map[string]interface{}{}
			}

			for _, itemi := range items.([]interface{}) {
				ret[tag] = append(ret[tag], itemi.(map[string]interface{}))
			}
		}

		return nil
	}, nil)

	return ret
}

// ProcessAdd change in-place msg to apply add config
func ProcessAdd(addCfg AddCfg, msg *FluentMsg) {
	if addCfg == nil {
		return
	}

	var ok bool
	if _, ok = addCfg[msg.Tag]; ok {
		for _, item := range addCfg[msg.Tag] {
			for src, dst := range item {
				switch dst := dst.(type) {
				case nil:
					delete(msg.Message, src)
					continue
				case string:
					msg.Message[src] = ReplaceStrByMsg(msg, dst)
				case []byte:
					msg.Message[src] = ReplaceStrByMsg(msg, string(dst))
				default:
					msg.Message[src] = dst
				}
			}
		}
	}
}

// GetValFromMap load val from map by joined key like `a.b.c`
//
// load `a.b` from `map[string]string{"a": {"b": "c"}}` will get "c"
func GetValFromMap(m interface{}, key string) interface{} {
	ks := strings.Split(key, ".")
	ei := 0
	v := reflect.ValueOf(m)
	k := ks[ei]

INNER_KEY:
	for {
		if ei == len(ks) {
			return nil
		}

		if v.Kind() == reflect.Interface {
			v = v.Elem()
		}

		if v.Kind() != reflect.Map {
			return nil
		}

		for _, rk := range v.MapKeys() {
			if k == rk.String() {
				ei += 1
				v = v.MapIndex(rk)
				if ei == len(ks) {
					return v.Interface()
				}
				k = ks[ei]
				continue INNER_KEY
			}
		}
	}
}
