package concator

import (
	"fmt"
	"regexp"
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
	TagPipeline        *tagFilters.TagPipeline
	NFork, OutChanSize int
}

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	*DispatcherCfg
	concatorMap *sync.Map            // tag:msgchan
	tagsCounter *sync.Map            // tag:counter
	outChan     chan *libs.FluentMsg // skip concator, direct to producer
	counter     *utils.Counter
}

// ConcatorFactoryItf interface of ConcatorFactory,
// decoupling with specific ConcatorFactory
type ConcatorFactoryItf interface {
	Spawn(string, string, *regexp.Regexp) chan<- *libs.FluentMsg
	MessageChan() chan *libs.FluentMsg
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
		concatorMap:   &sync.Map{},
		tagsCounter:   &sync.Map{},
		counter:       utils.NewCounter(),
	}
}

// Run dispacher to dispatch messages to different concators
func (d *Dispatcher) Run() {
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
			defer utils.Logger.Panic("dispatcher exit with msg", zap.String("msg", fmt.Sprint(msg)))
			// send each message to appropriate tagfilter by `tag`
			for msg = range d.InChan {
				d.counter.Count()
				if inChanForEachTagi, ok = d.concatorMap.Load(msg.Tag); !ok {
					// create new inChanForEachTag
					lock.Lock()
					if inChanForEachTagi, ok = d.concatorMap.Load(msg.Tag); !ok { // double check
						// new tag, create new tagfilter and its inchan
						utils.Logger.Info("got new tag", zap.String("tag", msg.Tag))
						if inChanForEachTag, err = d.TagPipeline.Spawn(msg.Tag, d.outChan); err != nil {
							utils.Logger.Error("try to spawn new tagpipeline got error",
								zap.Error(err),
								zap.String("tag", msg.Tag))
							continue
						} else {
							d.concatorMap.Store(msg.Tag, inChanForEachTag)
							d.tagsCounter.Store(msg.Tag, utils.NewCounter())
						}
					} else {
						inChanForEachTag = inChanForEachTagi.(chan<- *libs.FluentMsg)
					}

					lock.Unlock()
				} else {
					inChanForEachTag = inChanForEachTagi.(chan<- *libs.FluentMsg)
				}

				// count
				if counterI, ok = d.tagsCounter.Load(msg.Tag); !ok {
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
		d.tagsCounter.Range(func(tagi interface{}, ci interface{}) bool {
			metrics[tagi.(string)+".MsgPerSec"] = utils.Round(float64(ci.(*utils.Counter).Get())/(time.Now().Sub(lastT).Seconds()), .5, 1)
			ci.(*utils.Counter).Set(0)
			return true
		})
		lastT = time.Now()

		d.concatorMap.Range(func(tagi interface{}, ci interface{}) bool {
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
