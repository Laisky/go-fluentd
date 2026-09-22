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
	// Pending records belong to this worker, never the shared factory. Tags
	// remain part of the key even when identifiers happen to be identical.
	type key struct{ tag, identifier string }
	slot := map[key]*PendingMsg{}
	const timeout = 5 * time.Second
	timer := time.NewTimer(timeout)
	timer.Stop()
	defer timer.Stop()
	var tick <-chan time.Time
	recycle := func(msg *library.FluentMsg) {
		msg.ExtIds = nil
		if cf.msgPool != nil {
			cf.msgPool.Put(msg)
		}
	}
	release := func(k key, p *PendingMsg) { delete(slot, k); p.msg = nil; cf.pMsgPool.Put(p) }
	defer func() {
		for k, p := range slot {
			recycle(p.msg)
			release(k, p)
		}
	}()
	forward := func(msg *library.FluentMsg) bool {
		select {
		case outChan <- msg:
			return true
		case <-ctx.Done():
			return false
		}
	}
	text := func(v interface{}) ([]byte, bool) {
		switch v := v.(type) {
		case string:
			return []byte(v), true
		case []byte:
			return v, true
		default:
			return nil, false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			now := time.Now()
			wait := timeout
			for k, p := range slot {
				remaining := timeout - now.Sub(p.lastT)
				if remaining <= 0 {
					if !forward(p.msg) {
						return
					}
					release(k, p)
				} else if remaining < wait {
					wait = remaining
				}
			}
			if len(slot) == 0 {
				tick = nil
			} else {
				timer.Reset(wait)
			}
		case msg, ok := <-inChan:
			if !ok {
				for k, p := range slot {
					if !forward(p.msg) {
						return
					}
					release(k, p)
				}
				return
			}
			if msg == nil {
				continue
			}
			identifier, valid := text(msg.Message[cfg.Identifier])
			data, validData := text(msg.Message[cfg.MsgKey])
			if !valid || !validData {
				if !forward(msg) {
					recycle(msg)
					return
				}
				continue
			}
			msg.Message[cfg.MsgKey] = data
			k := key{msg.Tag, string(identifier)}
			p, exists := slot[k]
			if !exists {
				if !cfg.Regexp.Match(data) {
					if !forward(msg) {
						recycle(msg)
						return
					}
					continue
				}
				if len(slot) == 0 {
					timer.Reset(timeout)
					tick = timer.C
				}
				p = cf.pMsgPool.Get().(*PendingMsg)
				p.msg = msg
				p.lastT = time.Now()
				slot[k] = p
				continue
			}
			if cfg.Regexp.Match(data) {
				if !forward(p.msg) {
					recycle(msg)
					return
				}
				p.msg = msg
				p.lastT = time.Now()
				continue
			}
			p.msg.Message[cfg.MsgKey] = append(p.msg.Message[cfg.MsgKey].([]byte), data...)
			// The head owns every constituent ID until downstream success. Returning
			// the tail to the object pool is not an acknowledgement of its contents.
			p.msg.ExtIds = append(p.msg.ExtIds, msg.ID)
			p.msg.ExtIds = append(p.msg.ExtIds, msg.ExtIds...)
			p.lastT = time.Now()
			recycle(msg)
			if len(p.msg.Message[cfg.MsgKey].([]byte)) >= cf.MaxLen {
				if !forward(p.msg) {
					return
				}
				release(k, p)
				if len(slot) == 0 {
					timer.Stop()
					tick = nil
				}
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
