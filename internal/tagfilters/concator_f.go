package tagfilters

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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
	msg   *library.FluentMsg
	lastT time.Time
}

// StartNewConcator starting Concator to concatenate messages,
// you should not run concator directly,
// it's better to create and run Concator by ConcatorFactory
//
// TODO: concator for each tag now,
//       maybe set one concator for each identifier in the future for better performance
func (cf *ConcatorFactory) StartNewConcator(ctx context.Context, cfg *ConcatorCfg, outChan chan<- *library.FluentMsg, inChan <-chan *library.FluentMsg) {
	defer log.Logger.Info("concator exit")
	var (
		msg        *library.FluentMsg
		pmsg       *PendingMsg
		identifier string
		msgData    []byte
		ok         bool

		initWaitTs      = 1 * time.Millisecond
		maxWaitTs       = 40 * time.Millisecond
		waitTs          = initWaitTs
		nWaits          = 0
		nWaitsToDouble  = 2
		concatTimeoutTs = 5 * time.Second
		timer           = library.NewTimer(library.NewTimerConfig(initWaitTs, maxWaitTs, waitTs, concatTimeoutTs, nWaits, nWaitsToDouble))
	)

	for {
		if len(cf.slot) == 0 { // no msg waitting in slot
			log.Logger.Debug("slot clear, waitting for new msg")
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-inChan:
				if !ok {
					log.Logger.Info("inChan closed")
					return
				}
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-inChan:
				if !ok {
					log.Logger.Info("inChan closed")
					return
				}
			default: // no new msg
				for identifier, pmsg = range cf.slot {
					if utils.Clock.GetUTCNow().Sub(pmsg.lastT) > concatTimeoutTs { // timeout to flush
						// PAAS-210: I have no idea why this line could throw error
						// log.Logger.Debug("timeout flush", zap.ByteString("log", pmsg.msg.Message[cfg.MsgKey].([]byte)))

						switch pmsg.msg.Message[cfg.MsgKey].(type) {
						case []byte:
							log.Logger.Debug("timeout flush",
								zap.ByteString("log", pmsg.msg.Message[cfg.MsgKey].([]byte)),
								zap.String("tag", pmsg.msg.Tag))
						default:
							log.Logger.Panic("[panic] unknown type of `pmsg.msg.Message[cfg.MsgKey]`",
								zap.String("tag", pmsg.msg.Tag),
								zap.String("log", fmt.Sprint(pmsg.msg.Message[cfg.MsgKey])),
								zap.String("msg", fmt.Sprint(pmsg.msg)))
						}

						outChan <- pmsg.msg
						cf.pMsgPool.Put(pmsg)
						delete(cf.slot, identifier)
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
			log.Logger.Warn("unknown identifier or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("identifier_key", cfg.Identifier),
				zap.String("identifier", fmt.Sprint(msg.Message[cfg.Identifier])))
			outChan <- msg
			continue
		}

		// unknon msg key
		switch msg.Message[cfg.MsgKey].(type) {
		case []byte:
			msgData = msg.Message[cfg.MsgKey].([]byte)
		case string:
			msgData = []byte(msg.Message[cfg.MsgKey].(string))
			msg.Message[cfg.MsgKey] = msgData
		default:
			log.Logger.Warn("unknown msg key or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("msg_key", cfg.MsgKey),
				zap.String("msg", fmt.Sprint(msg.Message)))
			outChan <- msg
			continue
		}

		if _, ok = cf.slot[identifier]; !ok { // new identifier
			// new line with incorrect format, skip
			if !cfg.Regexp.Match(msgData) {
				outChan <- msg
				continue
			}

			// new line with correct format, set as first line
			log.Logger.Debug("got new identifier",
				zap.String("identifier", identifier),
				zap.ByteString("log", msgData))
			pmsg = cf.pMsgPool.Get().(*PendingMsg)
			pmsg.lastT = utils.Clock.GetUTCNow()
			pmsg.msg = msg
			cf.slot[identifier] = pmsg
			continue
		}

		pmsg = cf.slot[identifier]

		// replace exists msg in slot
		if cfg.Regexp.Match(msgData) { // new line
			log.Logger.Debug("got new line",
				zap.ByteString("log", msgData),
				zap.String("tag", msg.Tag))
			outChan <- pmsg.msg
			pmsg.msg = msg
			pmsg.lastT = utils.Clock.GetUTCNow()
			continue
		}

		// need to concat
		log.Logger.Debug("concat lines",
			zap.String("tag", msg.Tag),
			zap.ByteString("log", msg.Message[cfg.MsgKey].([]byte)))
		// pmsg.msg.Message[cfg.MsgKey] =
		// 	append(pmsg.msg.Message[cfg.MsgKey].([]byte), '\n')
		pmsg.msg.Message[cfg.MsgKey] =
			append(pmsg.msg.Message[cfg.MsgKey].([]byte), msg.Message[cfg.MsgKey].([]byte)...)
		if pmsg.msg.ExtIds == nil {
			pmsg.msg.ExtIds = []int64{} // create ids, wait to append tail-msg's id
		}
		pmsg.msg.ExtIds = append(pmsg.msg.ExtIds, msg.ID)
		pmsg.lastT = utils.Clock.GetUTCNow()

		// too long to send
		if len(pmsg.msg.Message[cfg.MsgKey].([]byte)) >= cf.MaxLen {
			log.Logger.Debug("too long to send", zap.String("msgKey", cfg.MsgKey), zap.String("tag", msg.Tag))
			outChan <- pmsg.msg
			cf.pMsgPool.Put(pmsg)
			delete(cf.slot, identifier)
		}

		// discard concated tail msg
		cf.DiscardMsg(msg)
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
	log.Logger.Info("create concatorFactory", zap.Int("max_len", cfg.MaxLen))

	if cfg.MaxLen <= 0 {
		log.Logger.Panic("concator max_length should bigger than 0")
	} else if cfg.MaxLen < 10000 {
		log.Logger.Warn("concator max_length maybe too short", zap.Int("len", cfg.MaxLen))
	}

	if cfg.NFork < 1 {
		log.Logger.Panic("nfork should bigger than 1")
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
	// log.Logger.Debug("IsTagSupported", zap.String("tag", tag))
	_, ok := cf.Plugins[tag]
	return ok
}

// Spawn create and run new Concator for new tag
func (cf *ConcatorFactory) Spawn(ctx context.Context, tag string, outChan chan<- *library.FluentMsg) chan<- *library.FluentMsg {
	log.Logger.Info("spawn concator tagfilter", zap.String("tag", tag))
	var (
		inChan  = make(chan *library.FluentMsg, cf.defaultInternalChanSize)
		inchans = []chan *library.FluentMsg{}
		cfg     = cf.Plugins[tag]
	)
	for i := 0; i < cf.NFork; i++ {
		eachInchan := make(chan *library.FluentMsg, cf.defaultInternalChanSize)
		go cf.StartNewConcator(ctx, cfg, outChan, eachInchan)
		inchans = append(inchans, eachInchan)
	}

	go cf.runLB(ctx, cf.LBKey, inChan, inchans)
	return inChan
}
