package postFilters

import (
	"encoding/json"
	"regexp"

	"github.com/Laisky/go-concator/libs"
	"github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

type ConnectorCfg struct {
	Tag             string
	MsgKey          string
	Reg             *regexp.Regexp
	IsRemoveOrigLog bool
}

type ConnectorFilter struct {
	*BaseFilter
	*ConnectorCfg
}

func NewConnectorFilter(cfg *ConnectorCfg) *ConnectorFilter {
	return &ConnectorFilter{
		BaseFilter:   &BaseFilter{},
		ConnectorCfg: cfg,
	}
}

func (f *ConnectorFilter) Filter(msg *libs.FluentMsg) *libs.FluentMsg {
	if msg.Tag != f.Tag {
		return msg
	}

	switch msg.Message[f.MsgKey].(type) {
	case []byte:
	default:
		utils.Logger.Warn("msg key not exists",
			zap.String("tag", f.Tag),
			zap.String("msg_key", f.MsgKey))
		return msg
	}

	// parse log string
	if err := RegexNamedSubMatch(f.Reg, msg.Message[f.MsgKey].([]byte), msg.Message); err != nil {
		utils.Logger.Warn("message format not matched",
			zap.String("tag", msg.Tag),
			zap.ByteString("log", msg.Message[f.MsgKey].([]byte)))
		f.msgPool.Put(msg)
		return nil
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

	return msg
}
