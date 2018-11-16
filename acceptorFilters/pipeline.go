package acceptorFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
)

type AcceptorPipelineCfg struct {
	MsgPool    *sync.Pool
	OutBufSize int
}

type AcceptorPipeline struct {
	*AcceptorPipelineCfg
	filters     []AcceptorFilterItf
	reEnterChan chan *libs.FluentMsg
}

func NewAcceptorPipeline(cfg *AcceptorPipelineCfg, filters ...AcceptorFilterItf) *AcceptorPipeline {
	utils.Logger.Info("NewAcceptorPipeline")
	a := &AcceptorPipeline{
		AcceptorPipelineCfg: cfg,
		filters:             filters,
		reEnterChan:         make(chan *libs.FluentMsg, 1000),
	}

	for _, filter := range a.filters {
		filter.SetUpstream(a.reEnterChan)
		filter.SetMsgPool(a.MsgPool)
	}

	return a
}

func (f *AcceptorPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan, skipDumpChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, f.OutBufSize)
	skipDumpChan = make(chan *libs.FluentMsg, f.OutBufSize)
	var (
		filter AcceptorFilterItf
		msg    *libs.FluentMsg
	)

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

			select {
			case outChan <- msg:
			case skipDumpChan <- msg:
			}

		NEXT_MSG:
		}
	}()

	return outChan, skipDumpChan
}
