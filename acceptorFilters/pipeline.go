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
	a = &AcceptorPipeline{
		AcceptorPipelineCfg: cfg,
		filters:             filters,
		reEnterChan:         make(chan *libs.FluentMsg, cfg.ReEnterChanSize),
		counter:             utils.NewCounter(),
	}
	if err := a.valid(); err != nil {
		libs.Logger.Panic("invalid cfg for acceptor pipeline")
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

	libs.Logger.Info("new acceptor pipeline",
		zap.Int("n_fork", a.NFork),
		zap.Int("out_buf_len", a.OutChanSize),
		zap.Int("reenter_chan_len", a.ReEnterChanSize),
		zap.Int("throttle_max", a.ThrottleMax),
		zap.Int("throttle_per_sec", a.ThrottleNPerSec),
		zap.Bool("is_throttle", a.IsThrottle),
	)
	return a, nil
}

func (f *AcceptorPipeline) valid() error {
	if f.NFork <= 0 {
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

	if f.NFork <= 0 {
		f.NFork = 4
		libs.Logger.Info("reset n_fork", zap.Int("n_fork", f.NFork))
	}

	if f.IsThrottle {
		if f.ThrottleMax <= 0 {
			f.ThrottleMax = 10000
			libs.Logger.Info("reset throttle_max", zap.Int("throttle_max", f.ThrottleMax))
		}

		if f.ThrottleNPerSec <= 0 {
			f.ThrottleNPerSec = 1000
			libs.Logger.Info("reset throttle_per_sec", zap.Int("throttle_per_sec", f.ThrottleNPerSec))
		}
	}

	return nil
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
