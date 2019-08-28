package senders

import (
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type SenderItf interface {
	Spawn(string) chan<- *libs.FluentMsg // Spawn(tag) inChan
	IsTagSupported(string) bool
	DiscardWhenBlocked() bool
	GetName() string

	SetMsgPool(*sync.Pool)
	SetCommitChan(chan<- *libs.FluentMsg)
	SetSupportedTags([]string)
	SetDiscardChan(chan<- *libs.FluentMsg)
	SetDiscardWithoutCommitChan(chan<- *libs.FluentMsg)
}

// BaseSender
// should not put msg into msgpool in sender
type BaseSender struct {
	msgPool                               *sync.Pool
	commitChan                            chan<- *libs.FluentMsg
	discardChan, discardWithoutCommitChan chan<- *libs.FluentMsg
	tags                                  []string
	IsDiscardWhenBlocked                  bool
}

func (s *BaseSender) runFlusher(inChan chan *libs.FluentMsg) {
	for {
		time.Sleep(3 * time.Second)
		inChan <- nil
	}
}

func (s *BaseSender) SetMsgPool(msgPool *sync.Pool) {
	s.msgPool = msgPool
}

func (s *BaseSender) SetCommitChan(commitChan chan<- *libs.FluentMsg) {
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
