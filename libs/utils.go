package libs

import (
	"bytes"
	"errors"
	"regexp"

	"github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func LoadTagsAppendEnv(env string, tags []string) []string {
	ret := []string{}
	for _, t := range tags {
		ret = append(ret, t+"."+env)
	}

	return ret
}

func RegexNamedSubMatch(r *regexp.Regexp, log []byte, subMatchMap map[string]interface{}) error {
	matches := r.FindSubmatch(log)
	names := r.SubexpNames()
	if len(names) != len(matches) {
		return errors.New("the number of args in `regexp` and `str` not matched")
	}

	for i, name := range names {
		if i != 0 && name != "" && len(matches[i]) != 0 {
			subMatchMap[name] = bytes.TrimSpace(matches[i])
		}
	}
	return nil
}

func FlattenMap(data map[string]interface{}, delimiter string) {
	for k, vi := range data {
		if v2i, ok := vi.(map[string]interface{}); ok {
			FlattenMap(v2i, delimiter)
			for k3, v3i := range v2i {
				data[k+delimiter+k3] = v3i
			}
			delete(data, k)
		}
	}
}
