package libs

import (
	"bytes"
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
	// variableNow generate time string in RFC3339
	variableNow = "@now"
	// variableNowUnix generate unix epoch in string
	variableNowUnix = "@unix"
	// variableLower `%{@lower:<key>}` convert value of key to lowercase
	variableLower = "@lower"
	// variableUpper `%{@upper:<key>}` convert value of key to uppercase
	variableUpper = "@upper"
)

var keyReplaceRegexp = regexp.MustCompile(`\%\{(@?[\w\-_\.]+(:\S+)?)\}`)

// AddCfg config of add
//
// config in yaml file:
//
//   add:
//     log-conn.{env}:
//       agent_id: "%{agentid}"
//       src_ip: "%{source.ip}"
//       src_port: "%{source.port}"
//       dst_ip: "%{destination.ip}"
//       dst_port: "%{destination.port}"
//       conn_type: "%{network.transport}"
//       package_size: "%{network.bytes}"
//       "lvl": "%{@upper:info}"
//       "@metadata": null
//       "host": null
type AddCfg map[string][]map[string]interface{}

// ReplaceStrByMsg replace variable in v by lib.FluentMsg
//
//   * `%{key}`            ->    `msg.Message["key"]`
//   * `%{a.b}`            ->    `msg.Message["a"]["b"]`
//   * `%{@tag}`           ->    `msg.Tag`
//   * `%{@id}`            ->    `msg.ID`
//   * `%{@str}`           ->    `<random_string>`
//   * `%{@now}`           ->    `2006-01-02T15:04:05Z07:00`
//   * `%{@unix}`          ->    `1590722923`
//   * `%{@lower:key}`     ->    `xxxx`
//   * `%{@upper:key}`     ->    `XXXX`
func ReplaceStrByMsg(msg *FluentMsg, v string) string {
	var (
		keys   = map[string]struct{}{}
		key    string
		cmds   []string
		newVal interface{}
		ok,
		isLower, isUpper bool
	)

	// fmt.Println(">>>>>>>>>>>>>>>>>>>>>>>>")

	for _, grp := range keyReplaceRegexp.FindAllStringSubmatch(v, -1) {
		isLower, isUpper = false, false
		if len(grp) < 2 || grp[1] == "" {
			continue
		}

		// fmt.Println("grp", grp)

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
			cmds = strings.Split(key, ":")
			if len(cmds) == 2 {
				switch cmds[0] {
				case variableLower:
					key = cmds[1]
					isLower = true
				case variableUpper:
					key = cmds[1]
					isUpper = true
				}
			}

			if newVal, ok = msg.Message[key]; !ok {
				// replace val from map
				if strings.Contains(key, ".") {
					newVal = GetValFromMap(msg.Message, key)
				} else {
					newVal = ""
				}
			}
		}

		// fmt.Println("key", key)
		// fmt.Println("isLower", isLower)
		// fmt.Println("isUpper", isUpper)
		// fmt.Println("newVal", newVal)

		switch newVal.(type) {
		case string:
			if isLower {
				newVal = strings.ToLower(newVal.(string))
			} else if isUpper {
				newVal = strings.ToUpper(newVal.(string))
			}
		case []byte:
			if isLower {
				newVal = bytes.ToLower(newVal.([]byte))
			} else if isUpper {
				newVal = bytes.ToUpper(newVal.([]byte))
			}
		case nil:
			newVal = ""
		}

		keys[key] = struct{}{}
		v = strings.ReplaceAll(v, grp[0], fmt.Sprint(newVal))
	}

	return v
}

// ParseAddCfg load auto config
//
// config file like:
//   add:
//     <tag>:
//       - <key>: <val>
//     app.{env}:
//       - key: %{key2}-xx
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
	// fmt.Println(m, key)

	ks := strings.Split(key, ".")
	deep := 0
	v := reflect.ValueOf(m)
	k := ks[deep]

INNER_KEY:
	for {
		// if deep == len(ks) {
		// 	return nil
		// }

		if v.Kind() == reflect.Interface {
			v = v.Elem()
		}

		if v.Kind() != reflect.Map {
			// fmt.Println("return: not map")
			return nil
		}

		for _, rk := range v.MapKeys() {
			if k == rk.String() {
				deep++
				v = v.MapIndex(rk)
				if deep == len(ks) {
					// fmt.Println("return: found key", ks[deep-1], v)
					return v.Interface()
				}

				k = ks[deep]
				continue INNER_KEY
			}
		}

		// fmt.Println("return: not found key")
		return nil
	}
}
