package acceptorFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
)

type AcceptorPipeline struct {
	filters     []AcceptorFilterItf
	msgPool     *sync.Pool
	reEnterChan chan *libs.FluentMsg
}

func NewAcceptorPipeline(msgPool *sync.Pool, filters ...AcceptorFilterItf) *AcceptorPipeline {
	utils.Logger.Info("NewAcceptorPipeline")
	return &AcceptorPipeline{
		filters:     filters,
		msgPool:     msgPool,
		reEnterChan: make(chan *libs.FluentMsg, 1000),
	}
}

func (f *AcceptorPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, 5000)
	var (
		filter AcceptorFilterItf
		msg    *libs.FluentMsg
	)

	for _, filter = range f.filters {
		filter.SetUpstream(f.reEnterChan)
		filter.SetMsgPool(f.msgPool)
	}

	go func() {
		defer utils.Logger.Error("quit acceptor pipeline")
		for {
			select {
			case msg = <-f.reEnterChan: // CAUTION: do not put msg into reEnterChan forever
			case msg = <-inChan:
			}

			utils.Logger.Debug("AcceptorPipeline got msg")
			for _, filter = range f.filters {
				if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
					goto NEXT_MSG
				}
			}

			outChan <- msg

		NEXT_MSG:
		}
	}()

	return outChan
}
