package postFilters

import (
	"errors"
	"regexp"
	"sync"

	"github.com/Laisky/go-concator/libs"
)

type PostFilterItf interface {
	Filter(*libs.FluentMsg) *libs.FluentMsg
	SetUpstream(chan *libs.FluentMsg)
	SetMsgPool(*sync.Pool)
}

type BaseFilter struct {
	upstreamChan chan *libs.FluentMsg
	msgPool      *sync.Pool
}

func (f *BaseFilter) SetUpstream(upChan chan *libs.FluentMsg) {
	f.upstreamChan = upChan
}

func (f *BaseFilter) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

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
