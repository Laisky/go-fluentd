package acceptorFilters

import (
	"context"
	"fmt"
	"sync"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
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

func NewAcceptorPipeline(ctx context.Context, cfg *AcceptorPipelineCfg, filters ...AcceptorFilterItf) (a *AcceptorPipeline, err error) {
	libs.Logger.Info("NewAcceptorPipeline")
	if cfg.NFork < 1 {
		panic(fmt.Errorf("NFork should greater than 1, got: %v", cfg.NFork))
	}

	a = &AcceptorPipeline{
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
		libs.Logger.Info("enable acceptor throttle",
			zap.Int("max", a.ThrottleMax),
			zap.Int("n_perf_sec", a.ThrottleNPerSec))
		if a.throttle, err = utils.NewThrottleWithCtx(
			ctx,
			&utils.ThrottleCfg{
				NPerSec: a.ThrottleNPerSec,
				Max:     a.ThrottleMax,
			}); err != nil {
			return nil, errors.Wrap(err, "enable accrptor throttle")
		}
	}

	return a, nil
}

func (f *AcceptorPipeline) registerMonitor() {
	monitor.AddMetric("acceptorPipeline", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"msgTotal":  f.counter.Get(),
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

	for i := 0; i < f.NFork; i++ {
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *libs.FluentMsg
				ok     bool
			)
			defer libs.Logger.Info("quit acceptorPipeline asyncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_ASYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-f.reEnterChan: // CAUTION: do not put msg into reEnterChan forever
					if !ok {
						libs.Logger.Info("reEnterChan closed")
						return
					}
				case msg, ok = <-asyncInChan:
					if !ok {
						libs.Logger.Info("asyncInChan closed")
						return
					}
				}
				f.counter.Count()

				// libs.Logger.Debug("AcceptorPipeline got msg")

				if f.IsThrottle && !f.throttle.Allow() {
					libs.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
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
				default:
					select {
					case outChan <- msg:
					case skipDumpChan <- msg: // baidu has low disk performance
					default:
						libs.Logger.Error("discard msg since disk & downstream are busy", zap.String("tag", msg.Tag))
						f.MsgPool.Put(msg)
					}
				}
			}
		}()

		// starting blockable chan
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *libs.FluentMsg
				ok     bool
			)
			defer libs.Logger.Info("quit acceptorPipeline syncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_SYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-syncInChan:
					if !ok {
						libs.Logger.Info("syncInChan closed")
						return
					}
				}
				// libs.Logger.Debug("AcceptorPipeline got blockable msg")
				f.counter.Count()

				if f.IsThrottle && !f.throttle.Allow() {
					libs.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
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
