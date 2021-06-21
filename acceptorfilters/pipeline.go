package acceptorfilters

import (
	"context"
	"fmt"
	"sync"

	"gofluentd/library"
	"gofluentd/monitor"

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
	reEnterChan chan *library.FluentMsg
	counter     *utils.Counter
	throttle    *utils.Throttle
}

func NewAcceptorPipeline(ctx context.Context, cfg *AcceptorPipelineCfg, filters ...AcceptorFilterItf) (a *AcceptorPipeline, err error) {
	a = &AcceptorPipeline{
		AcceptorPipelineCfg: cfg,
		filters:             filters,
		reEnterChan:         make(chan *library.FluentMsg, cfg.ReEnterChanSize),
		counter:             utils.NewCounter(),
	}
	if err := a.valid(); err != nil {
		library.Logger.Panic("invalid cfg for acceptor pipeline")
	}

	a.registerMonitor()
	for _, filter := range a.filters {
		filter.SetUpstream(a.reEnterChan)
		filter.SetMsgPool(a.MsgPool)
	}

	if a.IsThrottle {
		library.Logger.Info("enable acceptor throttle",
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

	library.Logger.Info("new acceptor pipeline",
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
		library.Logger.Info("reset n_fork", zap.Int("n_fork", f.NFork))
	}

	if f.OutChanSize <= 0 {
		f.OutChanSize = 1000
		library.Logger.Info("reset out_buf_len", zap.Int("out_buf_len", f.OutChanSize))
	}

	if f.ReEnterChanSize <= 0 {
		f.ReEnterChanSize = 1000
		library.Logger.Info("reset reenter_chan_len", zap.Int("reenter_chan_len", f.ReEnterChanSize))
	}

	if f.NFork <= 0 {
		f.NFork = 4
		library.Logger.Info("reset n_fork", zap.Int("n_fork", f.NFork))
	}

	if f.IsThrottle {
		if f.ThrottleMax <= 0 {
			f.ThrottleMax = 10000
			library.Logger.Info("reset throttle_max", zap.Int("throttle_max", f.ThrottleMax))
		}

		if f.ThrottleNPerSec <= 0 {
			f.ThrottleNPerSec = 1000
			library.Logger.Info("reset throttle_per_sec", zap.Int("throttle_per_sec", f.ThrottleNPerSec))
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

func (f *AcceptorPipeline) DiscardMsg(msg *library.FluentMsg) {
	msg.ExtIds = nil
	f.MsgPool.Put(msg)
}

func (f *AcceptorPipeline) Wrap(ctx context.Context, asyncInChan, syncInChan chan *library.FluentMsg) (outChan, skipDumpChan chan *library.FluentMsg) {
	outChan = make(chan *library.FluentMsg, f.OutChanSize)
	skipDumpChan = make(chan *library.FluentMsg, f.OutChanSize)

	for i := 0; i < f.NFork; i++ {
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *library.FluentMsg
				ok     bool
			)
			defer library.Logger.Info("quit acceptorPipeline asyncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_ASYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-f.reEnterChan: // CAUTION: do not put msg into reEnterChan forever
					if !ok {
						library.Logger.Info("reEnterChan closed")
						return
					}
				case msg, ok = <-asyncInChan:
					if !ok {
						library.Logger.Info("asyncInChan closed")
						return
					}
				}
				f.counter.Count()

				// library.Logger.Debug("AcceptorPipeline got msg")

				if f.IsThrottle && !f.throttle.Allow() {
					library.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
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
						library.Logger.Error("discard msg since disk & downstream are busy", zap.String("tag", msg.Tag))
						f.MsgPool.Put(msg)
					}
				}
			}
		}()

		// starting blockable chan
		go func() {
			var (
				filter AcceptorFilterItf
				msg    *library.FluentMsg
				ok     bool
			)
			defer library.Logger.Info("quit acceptorPipeline syncChan", zap.String("last_msg", fmt.Sprint(msg)))

		NEXT_SYNC_MSG:
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-syncInChan:
					if !ok {
						library.Logger.Info("syncInChan closed")
						return
					}
				}
				// library.Logger.Debug("AcceptorPipeline got blockable msg")
				f.counter.Count()

				if f.IsThrottle && !f.throttle.Allow() {
					library.Logger.Warn("discard msg by throttle", zap.String("tag", msg.Tag))
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
