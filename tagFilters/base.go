package tagFilters

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/cespare/xxhash"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type TagFilterFactoryItf interface {
	IsTagSupported(string) bool
	Spawn(context.Context, string, chan<- *libs.FluentMsg) chan<- *libs.FluentMsg // Spawn(tag, outChan) inChan
	GetName() string

	SetMsgPool(*sync.Pool)
	SetWaitCommitChan(chan<- *libs.FluentMsg)
	SetDefaultIntervalChanSize(int)
	DiscardMsg(*libs.FluentMsg)
}

type BaseTagFilterFactory struct {
	msgPool                 *sync.Pool
	waitCommitChan          chan<- *libs.FluentMsg
	defaultInternalChanSize int
}

func (f *BaseTagFilterFactory) SetMsgPool(msgPool *sync.Pool) {
	f.msgPool = msgPool
}

func (f *BaseTagFilterFactory) SetWaitCommitChan(waitCommitChan chan<- *libs.FluentMsg) {
	f.waitCommitChan = waitCommitChan
}

func (f *BaseTagFilterFactory) SetDefaultIntervalChanSize(size int) {
	f.defaultInternalChanSize = size
}

func (f *BaseTagFilterFactory) DiscardMsg(msg *libs.FluentMsg) {
	f.waitCommitChan <- msg
}

func (f *BaseTagFilterFactory) runLB(ctx context.Context, lbkey string, inChan chan *libs.FluentMsg, inchans []chan *libs.FluentMsg) {
	if len(inchans) < 1 {
		utils.Logger.Panic("nfork or inchans's length error",
			zap.Int("inchans_len", len(inchans)))
	}

	var (
		nfork          = len(inchans)
		hashkey        uint64
		defaultHashkey = xxhash.Sum64String("")
		downChan       = inchans[0]
		msg            *libs.FluentMsg
		ok             bool
	)
	defer utils.Logger.Info("concator lb exit", zap.String("msg", fmt.Sprint(msg)))
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-inChan:
			if !ok {
				utils.Logger.Info("inChan closed")
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
				utils.Logger.Warn("unknown type of hash key",
					zap.String("lb_key", lbkey),
					zap.String("val", fmt.Sprint(msg.Message[lbkey])))
				hashkey = defaultHashkey
			}

			downChan = inchans[int(hashkey%uint64(nfork))]
		}

		select {
		case downChan <- msg:
		default:
			utils.Logger.Warn("discard msg since downstream worker's inchan is full",
				zap.String("tag", msg.Tag),
				zap.Uint64("idx", hashkey%uint64(nfork)))
		}
	}
}
