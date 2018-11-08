package libs

import (
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
			subMatchMap[name] = match[i]
		}
	}
	return nil
}
