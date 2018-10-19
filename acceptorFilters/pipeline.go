package acceptorFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
)

type AcceptorPipeline struct {
	filters []AcceptorFilterItf
	msgPool *sync.Pool
}

func NewAcceptorPipeline(msgPool *sync.Pool, filters ...AcceptorFilterItf) *AcceptorPipeline {
	utils.Logger.Info("NewAcceptorPipeline")
	return &AcceptorPipeline{
		filters: filters,
		msgPool: msgPool,
	}
}

func (f *AcceptorPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, 1000)
	var (
		filter AcceptorFilterItf
		msg    *libs.FluentMsg
	)

	for _, filter = range f.filters {
		filter.SetUpstream(inChan)
		filter.SetMsgPool(f.msgPool)
	}

	go func() {
	NEXT_MSG:
		for origMsg := range inChan {
			msg = origMsg
			for _, filter = range f.filters {
				if msg = filter.Filter(msg); msg == nil { // msg has been discarded
					goto NEXT_MSG
				}
			}

			outChan <- msg
		}
	}()

	return outChan
}
