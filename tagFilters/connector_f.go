package tagFilters

import (
	"encoding/json"
	"regexp"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type ConnectorCfg struct {
	Tag, MsgKey     string
	Regexp          *regexp.Regexp
	OutChan         chan<- *libs.FluentMsg
	InChan          <-chan *libs.FluentMsg
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
}

type Connector struct {
	*ConnectorCfg
}

func NewConnector(cfg *ConnectorCfg) *Connector {
	utils.Logger.Info("create new connector tagfilter")
	return &Connector{
		ConnectorCfg: cfg,
	}
}

func (f *Connector) Run() {
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

		// parse json args
		// embeddedMap := map[string]interface{}{}
		switch msg.Message["args"].(type) {
		case []byte:
			if err := json.Unmarshal(msg.Message["args"].([]byte), &msg.Message); err != nil {
				utils.Logger.Error("unmarshal connector args got error", zap.Error(err))
			}
		}

		f.OutChan <- msg
	}
}

type ConnectorFactCfg struct {
	Tag, MsgKey     string
	Regexp          *regexp.Regexp
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
}

type ConnectorFact struct {
	*ConnectorFactCfg
}

func NewConnectorFact(cfg *ConnectorFactCfg) *ConnectorFact {
	utils.Logger.Info("create new connectorfactory")
	return &ConnectorFact{
		ConnectorFactCfg: cfg,
	}
}

func (cf *ConnectorFact) GetName() string {
	return "connector_tagfilter"
}

func (cf *ConnectorFact) IsTagSupported(tag string) bool {
	return tag == cf.Tag
}

func (cf *ConnectorFact) Spawn(tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn connector tagfilter", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, 1000)
	f := NewConnector(&ConnectorCfg{
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
