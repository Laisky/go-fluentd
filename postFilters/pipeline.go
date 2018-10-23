package postFilters

import (
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
)

type PostPipeline struct {
	filters []PostFilterItf
	msgPool *sync.Pool
}

func NewPostPipeline(msgPool *sync.Pool, filters ...PostFilterItf) *PostPipeline {
	utils.Logger.Info("NewPostPipeline")
	return &PostPipeline{
		filters: filters,
		msgPool: msgPool,
	}
}

func (f *PostPipeline) Wrap(inChan chan *libs.FluentMsg) (outChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, 1000)
	var (
		filter PostFilterItf
		msg    *libs.FluentMsg
	)

	for _, filter = range f.filters {
		filter.SetUpstream(inChan)
		filter.SetMsgPool(f.msgPool)
	}

	go func() {
	NEXT_MSG:
		for msg = range inChan {
			for _, filter = range f.filters {
				if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
					goto NEXT_MSG
				}
			}

			outChan <- msg
		}
	}()

	return outChan
}
