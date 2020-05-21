package postFilters

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type PostPipelineCfg struct {
	MsgPool                             *sync.Pool
	WaitCommitChan                      chan<- *libs.FluentMsg
	ReEnterChanSize, OutChanSize, NFork int
}

type PostPipeline struct {
	*PostPipelineCfg
	counter     *utils.Counter
	filters     []PostFilterItf
	reEnterChan chan *libs.FluentMsg
}

func NewPostPipeline(cfg *PostPipelineCfg, filters ...PostFilterItf) *PostPipeline {
	pp := &PostPipeline{
		PostPipelineCfg: cfg,
		counter:         utils.NewCounter(),
		filters:         filters,
		reEnterChan:     make(chan *libs.FluentMsg, cfg.ReEnterChanSize),
	}
	if err := pp.valid(); err != nil {
		libs.Logger.Panic("cfg invalid", zap.Error(err))
	}

	pp.registerMonitor()

	for _, filter := range pp.filters {
		filter.SetUpstream(pp.reEnterChan)
		filter.SetMsgPool(pp.MsgPool)
		filter.SetWaitCommitChan(pp.WaitCommitChan)
	}

	libs.Logger.Info("new post pipeline",
		zap.Int("n_fork", pp.NFork),
		zap.Int("out_buf_len", pp.OutChanSize),
		zap.Int("reenter_chan_len", pp.ReEnterChanSize),
	)
	return pp
}

func (f *PostPipeline) valid() error {
	if f.NFork < 1 {
		f.NFork = 4
		libs.Logger.Info("reset n_fork", zap.Int("n_fork", f.NFork))
	}

	if f.OutChanSize <= 0 {
		f.OutChanSize = 1000
		libs.Logger.Info("reset out_buf_len", zap.Int("out_buf_len", f.OutChanSize))
	}

	if f.ReEnterChanSize <= 0 {
		f.ReEnterChanSize = 1000
		libs.Logger.Info("reset reenter_chan_len", zap.Int("reenter_chan_len", f.ReEnterChanSize))
	}

	return nil
}

func (f *PostPipeline) registerMonitor() {
	monitor.AddMetric("postpipeline", func() map[string]interface{} {
		return map[string]interface{}{
			"msgPerSec": f.counter.GetSpeed(),
			"msgTotal":  f.counter.Get(),
		}
	})
}

func (f *PostPipeline) Wrap(ctx context.Context, inChan chan *libs.FluentMsg) (outChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, f.OutChanSize)

	for i := 0; i < f.NFork; i++ {
		go func(i int) {
			var (
				filter PostFilterItf
				msg    *libs.FluentMsg
				ok     bool
			)
			defer libs.Logger.Info("quit postPipeline", zap.Int("i", i), zap.String("msg", fmt.Sprint(msg)))

		NEW_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-f.reEnterChan:
					if !ok {
						libs.Logger.Info("reEnterChan closed")
						return
					}
				case msg, ok = <-inChan:
					if !ok {
						libs.Logger.Info("inChan closed")
						return
					}
				}

				f.counter.Count()
				for _, filter = range f.filters {
					if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
						continue NEW_MSG
					}
				}

				outChan <- msg
			}
		}(i)
	}

	return outChan
}
