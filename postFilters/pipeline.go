package postFilters

import (
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
)

type PostPipelineCfg struct {
	MsgPool                             *sync.Pool
	CommittedChan                       chan<- int64
	ReEnterChanSize, OutChanSize, NFork int
}

type PostPipeline struct {
	*PostPipelineCfg
	filters     []PostFilterItf
	reEnterChan chan *libs.FluentMsg
}

func NewPostPipeline(cfg *PostPipelineCfg, filters ...PostFilterItf) *PostPipeline {
	utils.Logger.Info("NewPostPipeline")

	if cfg.NFork < 1 {
		panic(fmt.Errorf("NFork should greater than 1, got: %v", cfg.NFork))
	}

	pp := &PostPipeline{
		PostPipelineCfg: cfg,
		filters:         filters,
		reEnterChan:     make(chan *libs.FluentMsg, cfg.ReEnterChanSize),
	}

	for _, filter := range pp.filters {
		filter.SetUpstream(pp.reEnterChan)
		filter.SetMsgPool(pp.MsgPool)
		filter.SetCommittedChan(pp.CommittedChan)
	}

	return pp
}

func (f *PostPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, f.OutChanSize)

	for i := 0; i < f.NFork; i++ {
		go func() {
			defer panic(fmt.Errorf("quit postPipeline"))

			var (
				filter PostFilterItf
				msg    *libs.FluentMsg
			)
			for {
			NEW_MSG:
				select {
				case msg = <-f.reEnterChan:
				case msg = <-inChan:
				}

				for _, filter = range f.filters {
					if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
						goto NEW_MSG
					}
				}

				outChan <- msg
			}
		}()
	}

	return outChan
}
