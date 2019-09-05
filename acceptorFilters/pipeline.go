package acceptorFilters

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type AcceptorPipelineCfg struct {
	MsgPool                             *sync.Pool
	OutChanSize, ReEnterChanSize, NFork int
	IsThrottle                          bool
	ThrottleNPerSec, ThrottleMax        int
}

type AcceptorPipeline struct {
	*AcceptorPipelineCfg
	filters     []AcceptorFilterItf
	reEnterChan chan *libs.FluentMsg
	counter     *utils.Counter
	throttle    *utils.Throttle
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *libs.FluentMsg
	lastT time.Time
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
		counter:             utils.NewCounter(),
	}
	a.registerMonitor()
	for _, filter := range a.filters {
		filter.SetUpstream(a.reEnterChan)
		filter.SetMsgPool(a.MsgPool)
	}

	if a.IsThrottle {
		utils.Logger.Info("enable acceptor throttle",
			zap.Int("max", a.ThrottleMax),
			zap.Int("n_perf_sec", a.ThrottleNPerSec))
		a.throttle = utils.NewThrottle(&utils.ThrottleCfg{
			NPerSec: a.ThrottleNPerSec,
			Max:     a.ThrottleMax,
		})
	}

	return a
}

func (f *AcceptorPipeline) registerMonitor() {
	monitor.AddMetric("acceptorPipeline", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"msgPerSec": f.counter.GetSpeed(),
		}
		return metrics
	})
}

func (f *AcceptorPipeline) DiscardMsg(msg *libs.FluentMsg) {
	msg.ExtIds = nil
	f.MsgPool.Put(msg)
}

func (f *AcceptorPipeline) Wrap(ctx context.Context, asyncInChan, syncInChan chan *libs.FluentMsg) (outChan, skipDumpChan chan *libs.FluentMsg) {
	outChan = make(chan *libs.FluentMsg, f.OutChanSize)
	skipDumpChan = make(chan *libs.FluentMsg, f.OutChanSize)

	if f.IsThrottle {
		f.throttle.Run()
	}

	for i := 0; i < f.NFork; i++ {
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *libs.FluentMsg
			)
			defer utils.Logger.Info("quit acceptorPipeline asyncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_ASYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg = <-f.reEnterChan: // CAUTION: do not put msg into reEnterChan forever
				case msg = <-asyncInChan:
				}
				f.counter.Count()

				// utils.Logger.Debug("AcceptorPipeline got msg")

				if f.IsThrottle && !f.throttle.Allow() {
					utils.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
					f.DiscardMsg(msg)
					continue
				}

				for _, filter = range f.filters {
					if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
						continue NEXT_ASYNC_MSG
					}
				}

				select {
				case outChan <- msg:
				case skipDumpChan <- msg: // baidu has low disk performance
				default:
					utils.Logger.Error("discard log", zap.String("tag", msg.Tag))
					f.MsgPool.Put(msg)
				}
			}
		}()

		// starting blockable chan
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *libs.FluentMsg
			)
			defer utils.Logger.Info("quit acceptorPipeline syncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_SYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg = <-syncInChan:
				}
				// utils.Logger.Debug("AcceptorPipeline got blockable msg")
				f.counter.Count()

				if f.IsThrottle && !f.throttle.Allow() {
					utils.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
					f.DiscardMsg(msg)
					continue
				}

				for _, filter = range f.filters {
					if msg = filter.Filter(msg); msg == nil { // quit filters for this msg
						// do not discard in pipeline
						// filter can make decision to bypass or discard msg
						continue NEXT_SYNC_MSG
					}
				}

				outChan <- msg
			}

		}()
	}

	return outChan, skipDumpChan
}
