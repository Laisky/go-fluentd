package concator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	"github.com/Laisky/go-fluentd/tagFilters"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type DispatcherCfg struct {
	InChan             chan *libs.FluentMsg
	TagPipeline        tagFilters.TagPipelineItf
	NFork, OutChanSize int
}

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	*DispatcherCfg
	tag2Concator *sync.Map            // tag:msgchan
	tag2Counter  *sync.Map            // tag:counter
	tag2Cancel   *sync.Map            // tag:cancel
	outChan      chan *libs.FluentMsg // skip concator, direct to producer
	counter      *utils.Counter
}

// NewDispatcher create new Dispatcher
func NewDispatcher(cfg *DispatcherCfg) *Dispatcher {
	utils.Logger.Info("create Dispatcher")

	if cfg.NFork < 0 {
		utils.Logger.Panic("nfork should bigger than 1")
	}

	return &Dispatcher{
		DispatcherCfg: cfg,
		outChan:       make(chan *libs.FluentMsg, cfg.OutChanSize),
		tag2Concator:  &sync.Map{},
		tag2Counter:   &sync.Map{},
		tag2Cancel:    &sync.Map{},
		counter:       utils.NewCounter(),
	}
}

// Run dispacher to dispatch messages to different concators
func (d *Dispatcher) Run(ctx context.Context) {
	utils.Logger.Info("run dispacher...")
	d.registerMonitor()
	lock := &sync.Mutex{}

	for i := 0; i < d.NFork; i++ {
		go func() {
			var (
				inChanForEachTagi interface{}
				inChanForEachTag  chan<- *libs.FluentMsg
				ok                bool
				err               error
				counterI          interface{}
				msg               *libs.FluentMsg
			)
			defer utils.Logger.Info("dispatcher exist with msg", zap.String("msg", fmt.Sprint(msg)))

			// send each message to appropriate tagfilter by `tag`
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-d.InChan:
					if !ok {
						utils.Logger.Info("inchan closed")
						return
					}
				}

				d.counter.Count()
				if inChanForEachTagi, ok = d.tag2Concator.Load(msg.Tag); !ok {
					// create new inChanForEachTag
					lock.Lock()
					if inChanForEachTagi, ok = d.tag2Concator.Load(msg.Tag); !ok { // double check
						// new tag, create new tagfilter and its inchan
						utils.Logger.Info("got new tag", zap.String("tag", msg.Tag))
						ctx2Tag, cancel := context.WithCancel(ctx)
						if inChanForEachTag, err = d.TagPipeline.Spawn(ctx2Tag, msg.Tag, d.outChan); err != nil {
							utils.Logger.Error("try to spawn new tagpipeline got error",
								zap.Error(err),
								zap.String("tag", msg.Tag))
							cancel()
							continue
						} else {
							d.tag2Concator.Store(msg.Tag, inChanForEachTag)
							d.tag2Counter.Store(msg.Tag, utils.NewCounter())
							d.tag2Cancel.Store(msg.Tag, cancel)
							go func(tag string) {
								<-ctx2Tag.Done()
								utils.Logger.Info("remove tag in dispatcher", zap.String("tag", tag))
								lock.Lock()
								d.tag2Concator.Delete(tag)
								d.tag2Counter.Delete(tag)
								d.tag2Cancel.Delete(tag)
								lock.Unlock()
							}(msg.Tag)
						}
					} else {
						inChanForEachTag = inChanForEachTagi.(chan<- *libs.FluentMsg)
					}

					lock.Unlock()
				} else {
					inChanForEachTag = inChanForEachTagi.(chan<- *libs.FluentMsg)
				}

				// count
				if counterI, ok = d.tag2Counter.Load(msg.Tag); !ok {
					utils.Logger.Panic("counter must exists", zap.String("tag", msg.Tag))
				}
				counterI.(*utils.Counter).Count()

				// put msg into tagfilter's inchan
				select {
				case inChanForEachTag <- msg:
				default:
					utils.Logger.Warn("tagfilter's inchan is blocked", zap.String("tag", msg.Tag))
				}
			}
		}()
	}
}

func (d *Dispatcher) registerMonitor() {
	lastT := time.Now()
	monitor.AddMetric("dispatcher", func() map[string]interface{} {
		metrics := map[string]interface{}{
			"msgPerSec": utils.Round(float64(d.counter.Get())/(time.Now().Sub(lastT).Seconds()), .5, 1),
		}
		d.counter.Set(0)
		d.tag2Counter.Range(func(tagi interface{}, ci interface{}) bool {
			metrics[tagi.(string)+".MsgPerSec"] = utils.Round(float64(ci.(*utils.Counter).Get())/(time.Now().Sub(lastT).Seconds()), .5, 1)
			ci.(*utils.Counter).Set(0)
			return true
		})
		lastT = time.Now()

		d.tag2Concator.Range(func(tagi interface{}, ci interface{}) bool {
			metrics[tagi.(string)+".ChanLen"] = len(ci.(chan<- *libs.FluentMsg))
			metrics[tagi.(string)+".ChanCap"] = cap(ci.(chan<- *libs.FluentMsg))
			return true
		})
		return metrics
	})
}

func (d *Dispatcher) GetOutChan() chan *libs.FluentMsg {
	return d.outChan
}
