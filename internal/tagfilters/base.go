package tagfilters

import (
	"context"
	"fmt"
	"sync"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/zap"
	"github.com/cespare/xxhash"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type TagFilterFactoryItf interface {
	IsTagSupported(string) bool
	Spawn(context.Context, string, chan<- *library.FluentMsg) chan<- *library.FluentMsg // Spawn(tag, outChan) inChan
	GetName() string

	SetMsgPool(*sync.Pool)
	SetWaitCommitChan(chan<- *library.FluentMsg)
	SetDefaultIntervalChanSize(int)
	DiscardMsg(*library.FluentMsg)
}

type BaseTagFilterFactory struct {
	msgPool                 *sync.Pool
	waitCommitChan          chan<- *library.FluentMsg
	defaultInternalChanSize int
}

func (f *BaseTagFilterFactory) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseTagFilterFactory) SetWaitCommitChan(waitCommitChan chan<- *library.FluentMsg) {
	f.waitCommitChan = waitCommitChan
}

func (f *BaseTagFilterFactory) SetDefaultIntervalChanSize(size int) {
	f.defaultInternalChanSize = size
}

func (f *BaseTagFilterFactory) DiscardMsg(msg *library.FluentMsg) {
	f.waitCommitChan <- msg
}

func (f *BaseTagFilterFactory) runLB(ctx context.Context, lbkey string, inChan chan *library.FluentMsg, inchans []chan *library.FluentMsg) {
	if len(inchans) < 1 {
		log.Logger.Panic("nfork or inchans's length error",
			zap.Int("inchans_len", len(inchans)))
	}

	var (
		nfork          = len(inchans)
		hashkey        uint64
		defaultHashkey = xxhash.Sum64String("")
		downChan       = inchans[0]
		msg            *library.FluentMsg
		ok             bool
	)
	defer log.Logger.Info("concator lb exit", zap.String("msg", fmt.Sprint(msg)))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-inChan:
			if !ok {
				log.Logger.Info("inChan closed")
				return
			}
		}

		if nfork != 1 {
			switch msg.Message[lbkey].(type) {
			case []byte:
				hashkey = xxhash.Sum64(msg.Message[lbkey].([]byte))
			case string:
				hashkey = xxhash.Sum64String(msg.Message[lbkey].(string))
			case nil:
				hashkey = defaultHashkey
			default:
				log.Logger.Warn("unknown type of hash key",
					zap.String("lb_key", lbkey),
					zap.String("val", fmt.Sprint(msg.Message[lbkey])))
				hashkey = defaultHashkey
			}

			downChan = inchans[int(hashkey%uint64(nfork))]
		}

		select {
		case downChan <- msg:
		default:
			log.Logger.Warn("discard msg since downstream worker's inchan is full",
				zap.String("tag", msg.Tag),
				zap.Uint64("idx", hashkey%uint64(nfork)))
		}
	}
}
