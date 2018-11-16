package concator

import (
	"regexp"
	"sync"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-concator/monitor"
	"github.com/Laisky/go-concator/tagFilters"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type DispatcherCfg struct {
	InChan      chan *libs.FluentMsg
	TagPipeline *tagFilters.TagPipeline
}

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	*DispatcherCfg
	concatorMap *sync.Map            // tag:msgchan
	outChan     chan *libs.FluentMsg // skip concator, direct to producer
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
	return &Dispatcher{
		DispatcherCfg: cfg,
		outChan:       make(chan *libs.FluentMsg, 5000),
		concatorMap:   &sync.Map{},
	}
}

// Run dispacher to distrubute messages to different concators
func (d *Dispatcher) Run() {
	utils.Logger.Info("run dispacher...")
	d.registerMonitor()
	go func() {
		var (
			inChanForEachTagi interface{}
			inChanForEachTag  chan<- *libs.FluentMsg
			ok                bool
			err               error
		)
		// send each message to appropriate concator by `tag`
		for msg := range d.InChan {
			inChanForEachTagi, ok = d.concatorMap.Load(msg.Tag)
			if ok {
				// tagfilters should not blocking
				inChanForEachTagi.(chan<- *libs.FluentMsg) <- msg
				continue
			}

			// new tag
			utils.Logger.Info("got new tag", zap.String("tag", msg.Tag))
			inChanForEachTag, err = d.TagPipeline.Spawn(msg.Tag, d.outChan)
			if err != nil {
				utils.Logger.Error("try to spawn new tagpipeline got error",
					zap.Error(err),
					zap.String("tag", msg.Tag))
				continue
			}

			d.concatorMap.Store(msg.Tag, inChanForEachTag)
			inChanForEachTag <- msg
		}
	}()
}

func (d *Dispatcher) registerMonitor() {
	monitor.AddMetric("dispatcher", func() map[string]interface{} {
		metrics := map[string]interface{}{}
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
