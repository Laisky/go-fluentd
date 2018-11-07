package tagFilters

import (
	"regexp"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type GeelyCfg struct {
	Tag, MsgKey     string
	Regexp          *regexp.Regexp
	OutChan         chan<- *libs.FluentMsg
	InChan          <-chan *libs.FluentMsg
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
}

type Geely struct {
	*GeelyCfg
}

func NewGeely(cfg *GeelyCfg) *Geely {
	utils.Logger.Info("create new Geely tagfilter")
	return &Geely{
		GeelyCfg: cfg,
	}
}

func (f *Geely) Run() {
	for msg := range f.InChan {
		if msg.Tag != f.Tag {
			f.OutChan <- msg
		}

		switch msg.Message[f.MsgKey].(type) {
		case []byte:
		default:
			utils.Logger.Warn("msg key not exists",
				zap.String("tag", f.Tag),
				zap.String("msg_key", f.MsgKey))
			f.OutChan <- msg
		}

		// parse log string
		if err := libs.RegexNamedSubMatch(f.Regexp, msg.Message[f.MsgKey].([]byte), msg.Message); err != nil {
			utils.Logger.Warn("message format not matched",
				zap.String("tag", msg.Tag),
				zap.ByteString("log", msg.Message[f.MsgKey].([]byte)))
			f.MsgPool.Put(msg)
			continue
		}

		// remove origin log
		if f.IsRemoveOrigLog {
			delete(msg.Message, f.MsgKey)
		}

		f.OutChan <- msg
	}
}

type GeelyFactCfg struct {
	Tag, MsgKey     string
	Regexp          *regexp.Regexp
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
}

type GeelyFact struct {
	*GeelyFactCfg
}

func NewGeelyFact(cfg *GeelyFactCfg) *GeelyFact {
	utils.Logger.Info("create new Geelyfactory")
	return &GeelyFact{
		GeelyFactCfg: cfg,
	}
}

func (cf *GeelyFact) GetName() string {
	return "Geely_tagfilter"
}

func (cf *GeelyFact) IsTagSupported(tag string) bool {
	return tag == cf.Tag
}

func (cf *GeelyFact) Spawn(tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn Geely tagfilter", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, 1000)
	f := NewGeely(&GeelyCfg{
		Tag:             tag,
		InChan:          inChan,
		OutChan:         outChan,
		MsgKey:          cf.MsgKey,
		MsgPool:         cf.MsgPool,
		Regexp:          cf.Regexp,
		IsRemoveOrigLog: cf.IsRemoveOrigLog,
	})
	go f.Run()
	return inChan
}
