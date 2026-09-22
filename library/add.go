package library

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
	// variableNow generate time string in RFC3339
	variableNow = "@now"
	// variableNowUnix generate unix epoch in string
	variableNowUnix = "@unix"
	// variableLower `%{@lower:<key>}` convert value of key to lowercase
	variableLower = "@lower"
	// variableUpper `%{@upper:<key>}` convert value of key to uppercase
	variableUpper = "@upper"
)

var keyReplaceRegexp = regexp.MustCompile(`\%\{(@?[\w\-_\.]+(:[^{}\s]+)?)\}`)

// AddCfg config of add
//
// config in yaml file:
//
//	add:
//	  log-conn.{env}:
//	    agent_id: "%{agentid}"
//	    src_ip: "%{source.ip}"
//	    src_port: "%{source.port}"
//	    dst_ip: "%{destination.ip}"
//	    dst_port: "%{destination.port}"
//	    conn_type: "%{network.transport}"
//	    package_size: "%{network.bytes}"
//	    "lvl": "%{@upper:info}"
//	    "@metadata": null
//	    "host": null
type AddCfg map[string][]map[string]interface{}

// ReplaceStrByMsg replace variable in v by lib.FluentMsg
//
//   - `%{key}`            ->    `msg.Message["key"]`
//   - `%{a.b}`            ->    `msg.Message["a"]["b"]`
//   - `%{@tag}`           ->    `msg.Tag`
//   - `%{@id}`            ->    `msg.ID`
//   - `%{@str}`           ->    `<random_string>`
//   - `%{@now}`           ->    `2006-01-02T15:04:05Z07:00`
//   - `%{@unix}`          ->    `1590722923`
//   - `%{@lower:key}`     ->    `xxxx`
//   - `%{@upper:key}`     ->    `XXXX`
func ReplaceStrByMsg(msg *FluentMsg, v string) string {
	// Cache by the entire expression, not the underlying field: upper/lower
	// and plain lookups of one field are different substitutions.
	values := make(map[string]string)
	return keyReplaceRegexp.ReplaceAllStringFunc(v, func(expr string) string {
		if text, ok := values[expr]; ok {
			return text
		}
		key := expr[2 : len(expr)-1]
		var value interface{}
		command := ""
		switch key {
		case variableRandomString:
			value = utils.RandomStringWithLength(8)
		case variableMsgTag:
			value = msg.Tag
		case variableMsgID:
			value = msg.ID
		case variableNow:
			value = utils.Clock.GetUTCNow().Format(time.RFC3339)
		case variableNowUnix:
			value = utils.Clock.GetUTCNow().Unix()
		default:
			if prefix, field, found := strings.Cut(key, ":"); found && (prefix == variableLower || prefix == variableUpper) {
				command, key = prefix, field
			}
			var ok bool
			if value, ok = msg.Message[key]; !ok {
				value = GetValFromMap(msg.Message, key)
			}
		}
		text := ""
		switch val := value.(type) {
		case nil:
		case []byte:
			text = string(val)
		default:
			text = fmt.Sprint(val)
		}
		switch command {
		case variableLower:
			text = strings.ToLower(text)
		case variableUpper:
			text = strings.ToUpper(text)
		}
		values[expr] = text
		return text
	})
}

// ParseAddCfg load auto config
//
// config file like:
//
//	add:
//	  <tag>:
//	    - <key>: <val>
//	  app.{env}:
//	    - key: %{key2}-xx
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
