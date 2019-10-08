package tagFilters

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

type ConcatorCfg struct {
	MsgKey,
	Identifier string
	Regexp *regexp.Regexp
}

// LoadConcatorTagConfigs return the configurations about dispatch rules
func LoadConcatorTagConfigs(env string, plugins map[string]interface{}) (concatorcfgs map[string]*ConcatorCfg) {
	concatorcfgs = map[string]*ConcatorCfg{}
	for tag, tagcfgI := range plugins {
		cfg := tagcfgI.(map[string]interface{})
		concatorcfgs[tag+"."+env] = &ConcatorCfg{
			MsgKey:     cfg["msg_key"].(string),
			Identifier: cfg["identifier"].(string),
			Regexp:     regexp.MustCompile(cfg["regex"].(string)),
		}
	}

	return concatorcfgs
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *libs.FluentMsg
	lastT time.Time
}

// StartNewConcator starting Concator to concatenate messages,
// you should not run concator directly,
// it's better to create and run Concator by ConcatorFactory
//
// TODO: concator for each tag now,
// maybe set one concator for each identifier in the future for better performance
func (c *ConcatorFactory) StartNewConcator(ctx context.Context, cfg *ConcatorCfg, outChan chan<- *libs.FluentMsg, inChan <-chan *libs.FluentMsg) {
	defer utils.Logger.Info("concator exit")
	var (
		msg        *libs.FluentMsg
		pmsg       *PendingMsg
		identifier string
		log        []byte
		ok         bool

		initWaitTs      = 1 * time.Millisecond
		maxWaitTs       = 40 * time.Millisecond
		waitTs          = initWaitTs
		nWaits          = 0
		nWaitsToDouble  = 2
		concatTimeoutTs = 5 * time.Second
		timer           = libs.NewTimer(libs.NewTimerConfig(initWaitTs, maxWaitTs, waitTs, concatTimeoutTs, nWaits, nWaitsToDouble))
	)

	for {
		if len(c.slot) == 0 { // no msg waitting in slot
			utils.Logger.Debug("slot clear, waitting for new msg")
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-inChan:
				if !ok {
					utils.Logger.Info("inChan closed")
					return
				}
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-inChan:
				if !ok {
					utils.Logger.Info("inChan closed")
					return
				}
			default: // no new msg
				for identifier, pmsg = range c.slot {
					if utils.Clock.GetUTCNow().Sub(pmsg.lastT) > concatTimeoutTs { // timeout to flush
						// PAAS-210: I have no idea why this line could throw error
						// utils.Logger.Debug("timeout flush", zap.ByteString("log", pmsg.msg.Message[cfg.MsgKey].([]byte)))

						switch pmsg.msg.Message[cfg.MsgKey].(type) {
						case []byte:
							utils.Logger.Debug("timeout flush",
								zap.ByteString("log", pmsg.msg.Message[cfg.MsgKey].([]byte)),
								zap.String("tag", pmsg.msg.Tag))
						default:
							utils.Logger.Panic("[panic] unknown type of `pmsg.msg.Message[cfg.MsgKey]`",
								zap.String("tag", pmsg.msg.Tag),
								zap.String("log", fmt.Sprint(pmsg.msg.Message[cfg.MsgKey])),
								zap.String("msg", fmt.Sprint(pmsg.msg)))
						}

						outChan <- pmsg.msg
						c.pMsgPool.Put(pmsg)
						delete(c.slot, identifier)
					}
				}

				timer.Sleep()
				continue
			}
		}

		timer.Reset(utils.Clock.GetUTCNow())

		// unknown identifier
		switch msg.Message[cfg.Identifier].(type) {
		case []byte:
			identifier = string(msg.Message[cfg.Identifier].([]byte))
		case string:
			identifier = msg.Message[cfg.Identifier].(string)
		default:
			utils.Logger.Warn("unknown identifier or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("identifier_key", cfg.Identifier),
				zap.String("identifier", fmt.Sprint(msg.Message[cfg.Identifier])))
			outChan <- msg
			continue
		}

		// unknon msg key
		switch msg.Message[cfg.MsgKey].(type) {
		case []byte:
			log = msg.Message[cfg.MsgKey].([]byte)
		case string:
			log = []byte(msg.Message[cfg.MsgKey].(string))
			msg.Message[cfg.MsgKey] = log
		default:
			utils.Logger.Warn("unknown msg key or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("msg_key", cfg.MsgKey),
				zap.String("msg", fmt.Sprint(msg.Message)))
			outChan <- msg
			continue
		}

		if pmsg, ok = c.slot[identifier]; !ok { // new identifier
			// new line with incorrect format, skip
			if !cfg.Regexp.Match(log) {
				outChan <- msg
				continue
			}

			// new line with correct format, set as first line
			utils.Logger.Debug("got new identifier",
				zap.String("identifier", identifier),
				zap.ByteString("log", log))
			pmsg = c.pMsgPool.Get().(*PendingMsg)
			pmsg.lastT = utils.Clock.GetUTCNow()
			pmsg.msg = msg
			c.slot[identifier] = pmsg
			continue
		}

		// replace exists msg in slot
		if cfg.Regexp.Match(log) { // new line
			utils.Logger.Debug("got new line",
				zap.ByteString("log", log),
				zap.String("tag", msg.Tag))
			outChan <- c.slot[identifier].msg
			c.slot[identifier].msg = msg
			c.slot[identifier].lastT = utils.Clock.GetUTCNow()
			continue
		}

		// need to concat
		utils.Logger.Debug("concat lines",
			zap.String("tag", msg.Tag),
			zap.ByteString("log", msg.Message[cfg.MsgKey].([]byte)))
		// c.slot[identifier].msg.Message[cfg.MsgKey] =
		// 	append(c.slot[identifier].msg.Message[cfg.MsgKey].([]byte), '\n')
		c.slot[identifier].msg.Message[cfg.MsgKey] =
			append(c.slot[identifier].msg.Message[cfg.MsgKey].([]byte), msg.Message[cfg.MsgKey].([]byte)...)
		if c.slot[identifier].msg.ExtIds == nil {
			c.slot[identifier].msg.ExtIds = []int64{} // create ids, wait to append tail-msg's id
		}
		c.slot[identifier].msg.ExtIds = append(c.slot[identifier].msg.ExtIds, msg.Id)
		c.slot[identifier].lastT = utils.Clock.GetUTCNow()

		// too long to send
		if len(c.slot[identifier].msg.Message[cfg.MsgKey].([]byte)) >= c.MaxLen {
			utils.Logger.Debug("too long to send", zap.String("msgKey", cfg.MsgKey), zap.String("tag", msg.Tag))
			outChan <- c.slot[identifier].msg
			c.pMsgPool.Put(c.slot[identifier])
			delete(c.slot, identifier)
		}

		// discard concated msg
		c.DiscardMsg(msg)
	}
}

type ConcatorFactCfg struct {
	NFork, MaxLen int
	LBKey         string
	Plugins       map[string]*ConcatorCfg
}

// ConcatorFactory can spawn new Concator
type ConcatorFactory struct {
	*BaseTagFilterFactory
	*ConcatorFactCfg

	pMsgPool *sync.Pool
	slot     map[string]*PendingMsg
}

// NewConcatorFact create new ConcatorFactory
func NewConcatorFact(cfg *ConcatorFactCfg) *ConcatorFactory {
	utils.Logger.Info("create concatorFactory", zap.Int("max_len", cfg.MaxLen))

	if cfg.MaxLen <= 0 {
		utils.Logger.Panic("concator max_length should bigger than 0")
	} else if cfg.MaxLen < 10000 {
		utils.Logger.Warn("concator max_length maybe too short", zap.Int("len", cfg.MaxLen))
	}

	if cfg.NFork < 1 {
		utils.Logger.Panic("nfork should bigger than 1")
	}

	cf := &ConcatorFactory{
		BaseTagFilterFactory: &BaseTagFilterFactory{},
		ConcatorFactCfg:      cfg,
		slot:                 map[string]*PendingMsg{},
		pMsgPool: &sync.Pool{
			New: func() interface{} {
				return &PendingMsg{}
			},
		},
	}
	return cf
}

func (cf *ConcatorFactory) GetName() string {
	return "concator"
}

func (cf *ConcatorFactory) IsTagSupported(tag string) bool {
	// utils.Logger.Debug("IsTagSupported", zap.String("tag", tag))
	_, ok := cf.Plugins[tag]
	return ok
}

// Spawn create and run new Concator for new tag
func (cf *ConcatorFactory) Spawn(ctx context.Context, tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn concator tagfilter", zap.String("tag", tag))
	var (
		inChan  = make(chan *libs.FluentMsg, cf.defaultInternalChanSize)
		inchans = []chan *libs.FluentMsg{}
		cfg     = cf.Plugins[tag]
	)
	for i := 0; i < cf.NFork; i++ {
		eachInchan := make(chan *libs.FluentMsg, cf.defaultInternalChanSize)
		go cf.StartNewConcator(ctx, cfg, outChan, eachInchan)
		inchans = append(inchans, eachInchan)
	}

	go cf.runLB(ctx, cf.LBKey, inChan, inchans)
	return inChan
}
