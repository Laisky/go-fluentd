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

func (f *BaseTagFilterFactory) runLB(lbkey string, inChan chan *libs.FluentMsg, inchans []chan *libs.FluentMsg) {
	if len(inchans) < 1 {
		utils.Logger.Panic("nfork or inchans's length error",
			zap.Int("inchans_len", len(inchans)))
	}
	defer utils.Logger.Panic("concator lb exit")

	var (
		nfork        = len(inchans)
		hashkey      uint64
		emptyHashkey = xxhash.Sum64String("")
		downChan     = inchans[0]
	)
	for msg := range inChan {
		if nfork != 1 {
			switch msg.Message[lbkey].(type) {
			case []byte:
				hashkey = xxhash.Sum64(msg.Message[lbkey].([]byte))
			case string:
				hashkey = xxhash.Sum64String(msg.Message[lbkey].(string))
			case nil:
				hashkey = emptyHashkey
			default:
				utils.Logger.Warn("unknown type of hash key",
					zap.String("lb_key", lbkey),
					zap.String("val", fmt.Sprint(msg.Message[lbkey])))
				hashkey = emptyHashkey
			}

			downChan = inchans[int(hashkey%uint64(nfork))]
		}

		select {
		case downChan <- msg:
		default:
			utils.Logger.Warn("concator worker's inchan is full",
				zap.String("tag", msg.Tag),
				zap.Uint64("idx", hashkey%uint64(nfork)))
		}
	}
}
