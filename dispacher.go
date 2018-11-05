package concator

import (
	"fmt"
	"regexp"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/kataras/iris"
	"go.uber.org/zap"
)

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	concatorMap      map[string]chan<- *libs.FluentMsg // tag:msgchan
	inChan           <-chan *libs.FluentMsg
	cf               ConcatorFactoryItf
	dispatherConfigs map[string]*libs.TagConfig // tag:config
	outChan          chan *libs.FluentMsg       // skip concator, direct to producer
}

// ConcatorFactoryItf interface of ConcatorFactory,
// decoupling with specific ConcatorFactory
type ConcatorFactoryItf interface {
	Spawn(string, string, *regexp.Regexp) chan<- *libs.FluentMsg
	MessageChan() chan *libs.FluentMsg
}

// NewDispatcher create new Dispatcher
func NewDispatcher(inChan <-chan *libs.FluentMsg, cf ConcatorFactoryItf) *Dispatcher {
	utils.Logger.Info("create Dispatcher")
	return &Dispatcher{
		inChan:           inChan,
		cf:               cf,
		dispatherConfigs: libs.LoadTagConfigs(),
		outChan:          cf.MessageChan(),
		concatorMap:      map[string]chan<- *libs.FluentMsg{},
	}
}

// Run dispacher to distrubute messages to different concators
func (d *Dispatcher) Run() {
	utils.Logger.Info("run dispacher...")
	d.BindMonitor()
	go func() {
		var (
			msgChan chan<- *libs.FluentMsg
			ok      bool
			cfg     *libs.TagConfig
		)
		// send each message to appropriate concator by `tag`
		for msg := range d.inChan {
			msgChan, ok = d.concatorMap[msg.Tag]
			if ok {
				msgChan <- msg
				continue
			}

			// new tag
			cfg, ok = d.dispatherConfigs[msg.Tag]
			if !ok { // unknown tag
				utils.Logger.Warn("got unknown tag", zap.String("tag", msg.Tag))
				d.outChan <- msg
				continue
			}

			// spawn an new concator
			utils.Logger.Info("got new tag", zap.String("tag", msg.Tag))
			msgChan = d.cf.Spawn(
				cfg.MsgKey,
				cfg.Identifier,
				cfg.Regex)
			d.concatorMap[msg.Tag] = msgChan

			msgChan <- msg
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
