package concator

import (
	"fmt"
	"regexp"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-concator/tagFilters"
	utils "github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"go.uber.org/zap"
)

type DispatcherCfg struct {
	InChan      chan *libs.FluentMsg
	TagPipeline *tagFilters.TagPipeline
}

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	*DispatcherCfg
	concatorMap map[string]chan<- *libs.FluentMsg // tag:msgchan
	outChan     chan *libs.FluentMsg              // skip concator, direct to producer
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
		concatorMap:   map[string]chan<- *libs.FluentMsg{},
	}
}

// Run dispacher to distrubute messages to different concators
func (d *Dispatcher) Run() {
	utils.Logger.Info("run dispacher...")
	d.BindMonitor()
	go func() {
		var (
			inChanForEachTag chan<- *libs.FluentMsg
			ok               bool
			err              error
		)
		// send each message to appropriate concator by `tag`
		for msg := range d.InChan {
			inChanForEachTag, ok = d.concatorMap[msg.Tag]
			if ok {
				inChanForEachTag <- msg
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

			d.concatorMap[msg.Tag] = inChanForEachTag
			inChanForEachTag <- msg
		}
	}()
}

func (d *Dispatcher) BindMonitor() {
	utils.Logger.Info("bind `/monitor/dispatcher`")
	Server.Get("/monitor/dispatcher", func(ctx iris.Context) {
		cnt := "concatorMap tag:chan\n"
		for tag, c := range d.concatorMap {
			cnt += fmt.Sprintf("> %v: %v\n", tag, len(c))
		}
		ctx.Writef(cnt)
	})
}

func (d *Dispatcher) GetOutChan() chan *libs.FluentMsg {
	return d.outChan
}
