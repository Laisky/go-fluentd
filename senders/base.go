package senders

import (
	"sync"

	"github.com/Laisky/go-fluentd/libs"
)

func TagsAppendEnv(env string, tags []string) []string {
	ret := []string{}
	for _, t := range tags {
		ret = append(ret, t+"."+env)
	}

	return ret
}

type SenderItf interface {
	Spawn(string) chan<- *libs.FluentMsg // Spawn(tag) inChan
	IsTagSupported(string) bool
	DiscardWhenBlocked() bool
	GetName() string

	SetMsgPool(*sync.Pool)
	SetCommitChan(chan<- int64)
	SetSupportedTags([]string)
	SetDiscardChan(chan<- *libs.FluentMsg)
	SetDiscardWithoutCommitChan(chan<- *libs.FluentMsg)
}

// BaseSender
// should not put msg into msgpool in sender
type BaseSender struct {
	msgPool                               *sync.Pool
	commitChan                            chan<- int64
	discardChan, discardWithoutCommitChan chan<- *libs.FluentMsg
	tags                                  []string
	IsDiscardWhenBlocked                  bool
}

func (s *BaseSender) SetMsgPool(msgPool *sync.Pool) {
	s.msgPool = msgPool
}

func (s *BaseSender) SetCommitChan(commitChan chan<- int64) {
	s.commitChan = commitChan
}

func (s *BaseSender) SetDiscardChan(discardChan chan<- *libs.FluentMsg) {
	s.discardChan = discardChan
}

func (s *BaseSender) SetDiscardWithoutCommitChan(discardWithoutCommitChan chan<- *libs.FluentMsg) {
	s.discardWithoutCommitChan = discardWithoutCommitChan
}

func (s *BaseSender) SetSupportedTags(tags []string) {
	s.tags = tags
}

func (s *BaseSender) DiscardWhenBlocked() bool {
	return s.IsDiscardWhenBlocked
}

func (s *BaseSender) IsTagSupported(tag string) bool {
	for _, t := range s.tags {
		if t == tag {
			return true
		}
	}

	return false
}
