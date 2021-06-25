package senders

import (
	"context"
	"sync"

	"gofluentd/library"
)

type SenderItf interface {
	Spawn(context.Context) chan<- *library.FluentMsg // Spawn(ctx) inChan
	IsTagSupported(string) bool
	DiscardWhenBlocked() bool
	GetName() string

	SetMsgPool(*sync.Pool)
	SetCommitChan(chan<- *library.FluentMsg)
	SetSupportedTags([]string)
	SetSuccessedChan(chan<- *library.FluentMsg)
	SetFailedChan(chan<- *library.FluentMsg)
}

// BaseSender
// should not put msg into msgpool in sender
type BaseSender struct {
	msgPool                   *sync.Pool
	commitChan                chan<- *library.FluentMsg
	successedChan, failedChan chan<- *library.FluentMsg
	tags                      map[string]struct{}
	IsDiscardWhenBlocked      bool
}

func (s *BaseSender) SetMsgPool(msgPool *sync.Pool) {
	s.msgPool = msgPool
}

func (s *BaseSender) SetCommitChan(commitChan chan<- *library.FluentMsg) {
	s.commitChan = commitChan
}

func (s *BaseSender) SetSuccessedChan(successedChan chan<- *library.FluentMsg) {
	s.successedChan = successedChan
}

func (s *BaseSender) SetFailedChan(failedChan chan<- *library.FluentMsg) {
	s.failedChan = failedChan
}

func (s *BaseSender) SetSupportedTags(tags []string) {
	s.tags = map[string]struct{}{}
	for _, t := range tags {
		s.tags[t] = struct{}{}
	}
}

func (s *BaseSender) DiscardWhenBlocked() bool {
	return s.IsDiscardWhenBlocked
}

func (s *BaseSender) IsTagSupported(tag string) (ok bool) {
	_, ok = s.tags[tag]
	return ok
}
