package tagfilters

import (
	"context"
	"regexp"
	"time"

	"gofluentd/internal/concatstate"
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

// StartNewConcator runs a worker with process-shared finite reservations.
func (cf *ConcatorFactory) StartNewConcator(ctx context.Context, cfg *ConcatorCfg, outChan chan<- *library.FluentMsg, inChan <-chan *library.FluentMsg) {
	defer log.Logger.Info("concator exit")
	concatstate.Run(ctx, inChan, cf.ConcatBudget, 5*time.Second, cf.MaxLen, true,
		func(*library.FluentMsg) (concatstate.Rule, bool) {
			return concatstate.Rule{MessageKey: cfg.MsgKey, IdentifierKey: cfg.Identifier, Head: cfg.Regexp}, true
		},
		func(m *library.FluentMsg) bool {
			select {
			case outChan <- m:
				return true
			case <-ctx.Done():
				return false
			}
		},
		func(m *library.FluentMsg) { cf.msgPool.Put(m) })
}

type ConcatorFactCfg struct {
	ConcatBudget  *concatstate.Budget
	NFork, MaxLen int
	LBKey         string
	Plugins       map[string]*ConcatorCfg
}

// ConcatorFactory can spawn new Concator
type ConcatorFactory struct {
	*BaseTagFilterFactory
	*ConcatorFactCfg
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
	}
	cf.ConcatBudget = concatstate.Default(cf.ConcatBudget)
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
