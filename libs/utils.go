package libs

import (
	"bytes"
	"errors"
	"regexp"
)

func RegexNamedSubMatch(r *regexp.Regexp, log []byte, subMatchMap map[string]interface{}) error {
	match := r.FindSubmatch(log)
	names := r.SubexpNames()
	if len(names) != len(match) {
		return errors.New("the number of args in `regexp` and `str` not matched")
	}

	for i, name := range r.SubexpNames() {
		if name != "" && i != 0 && len(match[i]) != 0 {
			subMatchMap[name] = bytes.TrimSpace(match[i])
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
