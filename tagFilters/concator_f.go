package tagFilters

import (
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type ConcatorCfg struct {
	Cf                      TagFilterFactoryItf
	MaxLen                  int
	Tag, MsgKey, Identifier string
	OutChan                 chan<- *libs.FluentMsg
	MsgPool, PMsgPool       *sync.Pool
	Regexp                  *regexp.Regexp
}

// Concator work for one tag, contains many identifier("container_id")
// Warn: Concator should not blocking
type Concator struct {
	*ConcatorCfg
	slot map[string]*PendingMsg
}

// PendingMsg is the message wait tobe concatenate
type PendingMsg struct {
	msg   *libs.FluentMsg
	lastT time.Time
}

// NewConcator create new Concator
func NewConcator(cfg *ConcatorCfg) *Concator {
	utils.Logger.Debug("create new concator",
		zap.String("tag", cfg.Tag),
		zap.String("identifier", cfg.Identifier),
		zap.String("msgKey", cfg.MsgKey))

	return &Concator{
		ConcatorCfg: cfg,
		slot:        map[string]*PendingMsg{},
	}
}

// Run starting Concator to concatenate messages,
// you should not run concator directly,
// it's better to create and run Concator by ConcatorFactory
//
// TODO: concator for each tag now,
// maybe set one concator for each identifier in the future for better performance
func (c *Concator) Run(inChan <-chan *libs.FluentMsg) {
	var (
		msg        *libs.FluentMsg
		pmsg       *PendingMsg
		identifier string
		log        []byte
		ok         bool

		now             time.Time
		initWaitTs      = 1 * time.Millisecond
		maxWaitTs       = 40 * time.Millisecond
		waitTs          = initWaitTs
		nWaits          = 0
		nWaitsToDouble  = 2
		concatTimeoutTs = 5 * time.Second
		timer           = libs.NewTimer(libs.NewTimerConfig(initWaitTs, maxWaitTs, waitTs, concatTimeoutTs, nWaits, nWaitsToDouble))
	)

	for {
		now = time.Now()
		if len(c.slot) == 0 { // no msg waitting in slot
			utils.Logger.Debug("slot clear, waitting for new msg")
			msg = <-inChan
		} else {
			select {
			case msg = <-inChan:
			default:
				for identifier, pmsg = range c.slot {
					if now.Sub(pmsg.lastT) > concatTimeoutTs { // timeout to flush
						// PAAS-210: I have no idea why this line could throw error
						// utils.Logger.Debug("timeout flush", zap.ByteString("log", pmsg.msg.Message[c.MsgKey].([]byte)))

						switch pmsg.msg.Message[c.MsgKey].(type) {
						case []byte:
							utils.Logger.Debug("timeout flush",
								zap.ByteString("log", pmsg.msg.Message[c.MsgKey].([]byte)),
								zap.String("tag", pmsg.msg.Tag))
						default:
							utils.Logger.Error("[panic] unknown type of `pmsg.msg.Message[c.MsgKey]`",
								zap.String("tag", pmsg.msg.Tag),
								zap.String("log", fmt.Sprint(pmsg.msg.Message[c.MsgKey])),
								zap.String("msg", fmt.Sprint(pmsg.msg)))
						}

						c.PutDownstream(pmsg.msg)
						c.PMsgPool.Put(pmsg)
						delete(c.slot, identifier)
					}
				}

				timer.Sleep()
				continue
			}
		}

		timer.Reset(time.Now())

		// unknown identifier
		switch msg.Message[c.Identifier].(type) {
		case []byte:
			identifier = string(msg.Message[c.Identifier].([]byte))
		case string:
			identifier = msg.Message[c.Identifier].(string)
		default:
			utils.Logger.Warn("unknown identifier or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("identifier_key", c.Identifier),
				zap.String("identifier", fmt.Sprint(msg.Message[c.Identifier])))
			c.PutDownstream(msg)
			continue
		}

		// unknon msg key
		switch msg.Message[c.MsgKey].(type) {
		case []byte:
			log = msg.Message[c.MsgKey].([]byte)
		case string:
			log = []byte(msg.Message[c.MsgKey].(string))
			msg.Message[c.MsgKey] = log
		default:
			utils.Logger.Warn("unknown msg key or unknown type",
				zap.String("tag", msg.Tag),
				zap.String("msg_key", c.MsgKey),
				zap.String("msg", fmt.Sprint(msg.Message[c.MsgKey])))
			c.PutDownstream(msg)
			continue
		}

		if pmsg, ok = c.slot[identifier]; !ok { // new identifier
			// new line with incorrect format, skip
			if !c.Regexp.Match(log) {
				c.PutDownstream(msg)
				continue
			}

			// new line with correct format, set as first line
			utils.Logger.Debug("got new identifier",
				zap.String("identifier", identifier),
				zap.ByteString("log", log))
			pmsg = c.PMsgPool.Get().(*PendingMsg)
			pmsg.lastT = now
			pmsg.msg = msg
			c.slot[identifier] = pmsg
			continue
		}

		// replace exists msg in slot
		if c.Regexp.Match(log) { // new line
			utils.Logger.Debug("got new line",
				zap.ByteString("log", log),
				zap.String("tag", msg.Tag))
			c.PutDownstream(c.slot[identifier].msg)
			c.slot[identifier].msg = msg
			c.slot[identifier].lastT = now
			continue
		}

		// need to concat
		utils.Logger.Debug("concat lines", zap.ByteString("log", msg.Message[c.MsgKey].([]byte)))
		c.slot[identifier].msg.Message[c.MsgKey] =
			append(c.slot[identifier].msg.Message[c.MsgKey].([]byte), '\n')
		c.slot[identifier].msg.Message[c.MsgKey] =
			append(c.slot[identifier].msg.Message[c.MsgKey].([]byte), msg.Message[c.MsgKey].([]byte)...)
		if c.slot[identifier].msg.ExtIds == nil {
			c.slot[identifier].msg.ExtIds = []int64{} // create ids, wait to append tail-msg's id
		}
		c.slot[identifier].msg.ExtIds = append(c.slot[identifier].msg.ExtIds, msg.Id)
		c.slot[identifier].lastT = now

		// too long to send
		if len(c.slot[identifier].msg.Message[c.MsgKey].([]byte)) >= c.MaxLen {
			utils.Logger.Debug("too long to send", zap.String("msgKey", c.MsgKey), zap.String("tag", msg.Tag))
			c.PutDownstream(c.slot[identifier].msg)
			c.PMsgPool.Put(c.slot[identifier])
			delete(c.slot, identifier)
		}

		c.Cf.DiscardMsg(msg)
	}
}

func (c *Concator) PutDownstream(msg *libs.FluentMsg) {
	c.OutChan <- msg
}

type ConcatorFactCfg struct {
	MaxLen       int
	ConcatorCfgs map[string]*libs.ConcatorTagCfg
}

// ConcatorFactory can spawn new Concator
type ConcatorFactory struct {
	*BaseTagFilterFactory
	*ConcatorFactCfg
	pMsgPool *sync.Pool
}

// NewConcatorFact create new ConcatorFactory
func NewConcatorFact(cfg *ConcatorFactCfg) *ConcatorFactory {
	utils.Logger.Info("create concatorFactory", zap.Int("max_len", cfg.MaxLen))

	if cfg.MaxLen < 10000 {
		utils.Logger.Warn("concator max_length maybe too short", zap.Int("len", cfg.MaxLen))
	}

	return &ConcatorFactory{
		BaseTagFilterFactory: &BaseTagFilterFactory{},
		ConcatorFactCfg:      cfg,
		pMsgPool: &sync.Pool{
			New: func() interface{} {
				return &PendingMsg{}
			},
		},
	}
}

func (cf *ConcatorFactory) GetName() string {
	return "concator_tagfilter"
}

func (cf *ConcatorFactory) IsTagSupported(tag string) bool {
	utils.Logger.Debug("IsTagSupported", zap.String("tag", tag))
	_, ok := cf.ConcatorCfgs[tag]
	return ok
}

// Spawn create and run new Concator for new tag
func (cf *ConcatorFactory) Spawn(tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	inChan := make(chan *libs.FluentMsg, cf.defaultInternalChanSize)
	concator := NewConcator(&ConcatorCfg{
		Cf:         cf,
		MaxLen:     cf.MaxLen,
		Tag:        tag,
		OutChan:    outChan,
		MsgKey:     cf.ConcatorCfgs[tag].MsgKey,
		Identifier: cf.ConcatorCfgs[tag].Identifier,
		PMsgPool:   cf.pMsgPool,
		Regexp:     cf.ConcatorCfgs[tag].Regexp,
	})
	go concator.Run(inChan)
	return inChan
}
