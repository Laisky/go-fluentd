package senders

import (
	"context"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type SenderItf interface {
	Spawn(context.Context) chan<- *libs.FluentMsg // Spawn(ctx) inChan
	IsTagSupported(string) bool
	DiscardWhenBlocked() bool
	GetName() string

	SetMsgPool(*sync.Pool)
	SetCommitChan(chan<- *libs.FluentMsg)
	SetSupportedTags([]string)
	SetSuccessedChan(chan<- *libs.FluentMsg)
	SetFailedChan(chan<- *libs.FluentMsg)
}

// BaseSender
// should not put msg into msgpool in sender
type BaseSender struct {
	msgPool                   *sync.Pool
	commitChan                chan<- *libs.FluentMsg
	successedChan, failedChan chan<- *libs.FluentMsg
	tags                      []string
	IsDiscardWhenBlocked      bool
}

func (s *BaseSender) SetMsgPool(msgPool *sync.Pool) {
	s.msgPool = msgPool
}

func (s *BaseSender) SetCommitChan(commitChan chan<- *libs.FluentMsg) {
	s.commitChan = commitChan
}

func (s *BaseSender) SetSuccessedChan(successedChan chan<- *libs.FluentMsg) {
	s.successedChan = successedChan
}

func (s *BaseSender) SetFailedChan(failedChan chan<- *libs.FluentMsg) {
	s.failedChan = failedChan
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
