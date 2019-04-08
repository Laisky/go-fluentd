package tagFilters

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/cespare/xxhash"
	"github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

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

func (f *BaseTagFilterFactory) runLB(lbkey string, nfork int, inChan chan *libs.FluentMsg, inchans []chan *libs.FluentMsg) {
	defer utils.Logger.Panic("concator lb exit")
	emptyHashkey := xxhash.Sum64String("")
	var hashkey uint64
	for msg := range inChan {
		switch key := msg.Message[lbkey].(type) {
		case []byte:
			hashkey = xxhash.Sum64(key)
		case string:
			hashkey = xxhash.Sum64String(key)
		case nil:
			hashkey = emptyHashkey
		default:
			utils.Logger.Warn("unknown type of hash key",
				zap.String("lb_key", lbkey),
				zap.String("val", fmt.Sprint(key)))
			hashkey = emptyHashkey
		}

		select {
		case inchans[int(hashkey%uint64(nfork))] <- msg:
		default:
			utils.Logger.Warn("concator worker's inchan is full",
				zap.String("tag", msg.Tag),
				zap.Uint64("idx", hashkey%uint64(nfork)))
		}
	}
}
