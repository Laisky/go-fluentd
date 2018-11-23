package acceptorFilters

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
)

type AcceptorPipelineCfg struct {
	MsgPool                             *sync.Pool
	OutChanSize, ReEnterChanSize, NFork int
}

type AcceptorPipeline struct {
	*AcceptorPipelineCfg
	filters     []AcceptorFilterItf
	reEnterChan chan *libs.FluentMsg
}

func NewAcceptorPipeline(cfg *AcceptorPipelineCfg, filters ...AcceptorFilterItf) *AcceptorPipeline {
	utils.Logger.Info("NewAcceptorPipeline")

	if cfg.NFork < 1 {
		panic(fmt.Errorf("NFork should greater than 1, got: %v", cfg.NFork))
	}

	a := &AcceptorPipeline{
		AcceptorPipelineCfg: cfg,
		filters:             filters,
		reEnterChan:         make(chan *libs.FluentMsg, cfg.ReEnterChanSize),
	}

	for _, filter := range a.filters {
		filter.SetUpstream(a.reEnterChan)
		filter.SetMsgPool(a.MsgPool)
	}

	return a
}

func (f *AcceptorPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan, skipDumpChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, f.OutChanSize)
	skipDumpChan = make(chan *libs.FluentMsg, f.OutChanSize)

	for i := 0; i < f.NFork; i++ {
		go func() {
			defer panic(fmt.Errorf("quit acceptorPipeline"))

			var (
				filter AcceptorFilterItf
				msg    *libs.FluentMsg
			)
			for {
			NEXT_MSG:
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
			}
		}()
	}

	return outChan, skipDumpChan
}
