package tagFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
)

type TagFilterFactoryItf interface {
	IsTagSupported(string) bool
	Spawn(string, chan<- *libs.FluentMsg) chan<- *libs.FluentMsg // Spawn(tag, outChan) inChan
	GetName() string

	SetMsgPool(*sync.Pool)
	SetCommittedChan(chan<- int64)
	SetDefaultIntervalChanSize(int)
	DiscardMsg(*libs.FluentMsg)
}

type BaseTagFilterFactory struct {
	msgPool                 *sync.Pool
	committedChan           chan<- int64
	defaultInternalChanSize int
}

func (f *BaseTagFilterFactory) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseTagFilterFactory) SetCommittedChan(committedChan chan<- int64) {
	f.committedChan = committedChan
}

func (f *BaseTagFilterFactory) SetDefaultIntervalChanSize(size int) {
	f.defaultInternalChanSize = size
}

func (f *BaseTagFilterFactory) DiscardMsg(msg *libs.FluentMsg) {
	f.committedChan <- msg.Id
	if msg.ExtIds != nil {
		for _, id := range msg.ExtIds {
			f.committedChan <- id
		}
	}

	msg.ExtIds = nil
	f.msgPool.Put(msg)
}
