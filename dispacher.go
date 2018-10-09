package concator

import (
	"fmt"
	"regexp"

	"github.com/kataras/iris"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
)

// DispatcherConfig configurations about how to dispatch messages
type DispatcherConfig struct {
	MsgKey, Identifier string
	Regex              *regexp.Regexp
}

// Dispatcher dispatch messages by tag to different concator
type Dispatcher struct {
	concatorMap      map[string]chan<- *FluentMsg // tag:msgchan
	msgChan          <-chan *FluentMsg
	cf               ConcatorFactoryItf
	dispatherConfigs map[string]*DispatcherConfig // tag:config
	bypassMsgChan    chan<- *FluentMsg            // skip concator, direct to producer
}

// ConcatorFactoryItf interface of ConcatorFactory,
// decoupling with specific ConcatorFactory
type ConcatorFactoryItf interface {
	Spawn(string, string, *regexp.Regexp) chan<- *FluentMsg
}

// NewDispatcher create new Dispatcher
func NewDispatcher(msgChan <-chan *FluentMsg, cf ConcatorFactoryItf, bypassMsgChan chan<- *FluentMsg) *Dispatcher {
	utils.Logger.Info("create Dispatcher")
	return &Dispatcher{
		msgChan:          msgChan,
		cf:               cf,
		dispatherConfigs: LoadDispatcherConfig(),
		bypassMsgChan:    bypassMsgChan,
		concatorMap:      map[string]chan<- *FluentMsg{},
	}
}

// Run dispacher to distrubute messages to different concators
func (d *Dispatcher) Run() {
	utils.Logger.Info("run dispacher...")
	d.RunMonitor()
	go func() {
		var (
			msgChan chan<- *FluentMsg
			ok      bool
			cfg     *DispatcherConfig
		)
		// send each message to appropriate concator by `tag`
		for msg := range d.msgChan {
			msgChan, ok = d.concatorMap[msg.Tag]
			if ok {
				msgChan <- msg
				continue
			}

			// new tag
			cfg, ok = d.dispatherConfigs[msg.Tag]
			if !ok { // unknown tag
				utils.Logger.Warn("got unknown tag", zap.String("tag", msg.Tag))
				d.bypassMsgChan <- msg
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

func (d *Dispatcher) RunMonitor() {
	utils.Logger.Info("bind `/monitor/dispatcher`")
	Server.Get("/monitor/dispatcher", func(ctx iris.Context) {
		cnt := "concatorMap tag:chan\n"
		for tag, c := range d.concatorMap {
			cnt += fmt.Sprintf("%v: %v\n", tag, len(c))
		}
		ctx.Writef(cnt)
	})
}

// LoadDispatcherConfig return the configurations about dispatch rules
func LoadDispatcherConfig() map[string]*DispatcherConfig {
	dispatherConfigs := map[string]*DispatcherConfig{}
	env := "." + utils.Settings.GetString("env")
	var cfg map[string]interface{}
	for tag, cfgI := range utils.Settings.Get("settings.tag_configs").(map[string]interface{}) {
		cfg = cfgI.(map[string]interface{})
		dispatherConfigs[tag+env] = &DispatcherConfig{
			MsgKey:     cfg["msg_key"].(string),
			Identifier: cfg["identifier"].(string),
			Regex:      regexp.MustCompile(cfg["regex"].(string)),
		}
	}

	return dispatherConfigs
}
