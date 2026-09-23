package tagfilters

import (
	"context"
	"regexp"
	"sync"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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
//
//	maybe set one concator for each identifier in the future for better performance
func (cf *ConcatorFactory) StartNewConcator(ctx context.Context, cfg *ConcatorCfg, outChan chan<- *library.FluentMsg, inChan <-chan *library.FluentMsg) {
	defer log.Logger.Info("concator exit")
	// A factory is shared by tags and workers, but pending records must not be.
	slot := make(map[string]*PendingMsg)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	emit := func(msg *library.FluentMsg) bool {
		select {
		case outChan <- msg:
			return true
		case <-ctx.Done():
			return false
		}
	}
	release := func(key string, pending *PendingMsg) {
		delete(slot, key)
		pending.msg = nil
		cf.pMsgPool.Put(pending)
	}
	defer func() {
		for key, pending := range slot {
			release(key, pending)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			// Do not acknowledge unfinished records: their journal copies must replay.
			return
		case now := <-ticker.C:
			for key, pending := range slot {
				if now.Sub(pending.lastT) >= 5*time.Second {
					if !emit(pending.msg) {
						return
					}
					release(key, pending)
				}
			}
		case msg, ok := <-inChan:
			if !ok {
				for key, pending := range slot {
					if !emit(pending.msg) {
						return
					}
					release(key, pending)
				}
				return
			}
			var identifier string
			switch value := msg.Message[cfg.Identifier].(type) {
			case string:
				identifier = value
			case []byte:
				identifier = string(value)
			default:
				if !emit(msg) {
					return
				}
				continue
			}
			var text []byte
			switch value := msg.Message[cfg.MsgKey].(type) {
			case string:
				text = []byte(value)
				msg.Message[cfg.MsgKey] = text
			case []byte:
				text = value
			default:
				if !emit(msg) {
					return
				}
				continue
			}
			pending, exists := slot[identifier]
			isHead := cfg.Regexp.Match(text)
			if !exists {
				if !isHead {
					if !emit(msg) {
						return
					}
					continue
				}
				pending = cf.pMsgPool.Get().(*PendingMsg)
				pending.msg, pending.lastT = msg, time.Now()
				slot[identifier] = pending
				continue
			}
			if isHead {
				if !emit(pending.msg) {
					return
				}
				pending.msg, pending.lastT = msg, time.Now()
				continue
			}
			pending.msg.Message[cfg.MsgKey] = append(pending.msg.Message[cfg.MsgKey].([]byte), text...)
			pending.msg.ExtIds = append(pending.msg.ExtIds, msg.ID)
			pending.msg.ExtIds = append(pending.msg.ExtIds, msg.ExtIds...)
			pending.lastT = time.Now()
			// Transfer acknowledgement ownership to the head. Recycling a wrapper is
			// not a successful delivery and must never publish an ACK for the tail.
			msg.ExtIds = nil
			cf.msgPool.Put(msg)
			if len(pending.msg.Message[cfg.MsgKey].([]byte)) >= cf.MaxLen {
				if !emit(pending.msg) {
					return
				}
				release(identifier, pending)
			}
		}
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
