package tagFilters

import (
	"encoding/json"
	"regexp"
	"sync"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"go.uber.org/zap"
)

func ParseAddCfg(env string, cfg interface{}) map[string]map[string]string {
	ret := map[string]map[string]string{}
	if cfg == nil {
		return ret
	}

	for tag, vi := range cfg.(map[string]interface{}) {
		tag = tag + "." + env
		if _, ok := ret[tag]; !ok {
			ret[tag] = map[string]string{}
		}

		for nk, nvi := range vi.(map[string]interface{}) {
			ret[tag][nk] = nvi.(string)
		}
	}

	return ret
}

type ParserCfg struct {
	Cf                        *ParserFact
	Tag, MsgKey               string
	Regexp                    *regexp.Regexp
	OutChan                   chan<- *libs.FluentMsg
	InChan                    <-chan *libs.FluentMsg
	MsgPool                   *sync.Pool
	IsRemoveOrigLog           bool
	Add                       map[string]map[string]string
	ParseJsonKey, MustInclude string
}

// Parser is generanal parser
type Parser struct {
	*ParserCfg
}

func NewParser(cfg *ParserCfg) *Parser {
	utils.Logger.Info("create new Parser tagfilter")
	return &Parser{
		ParserCfg: cfg,
	}
}

func (f *Parser) Run() {
	var (
		err  error
		ok   bool
		msg  *libs.FluentMsg
		k, v string
	)
	for msg = range f.InChan {
		if !f.Cf.IsTagSupported(msg.Tag) {
			f.OutChan <- msg
		}

		switch msg.Message[f.MsgKey].(type) {
		case []byte:
		default:
			utils.Logger.Warn("msg key not exists",
				zap.String("tag", msg.Tag),
				zap.String("msg_key", f.MsgKey))
			f.OutChan <- msg
			continue
		}

		// parse log string
		if err = libs.RegexNamedSubMatch(f.Regexp, msg.Message[f.MsgKey].([]byte), msg.Message); err != nil {
			utils.Logger.Warn("message format not matched",
				zap.String("tag", msg.Tag),
				zap.ByteString("log", msg.Message[f.MsgKey].([]byte)))
			f.Cf.DiscardMsg(msg)
			continue
		}

		// remove origin log
		if f.IsRemoveOrigLog {
			delete(msg.Message, f.MsgKey)
		}

		// MustInclude
		if f.MustInclude != "" {
			if _, ok = msg.Message[f.MustInclude]; !ok {
				utils.Logger.Warn("dicard since of missing key", zap.String("key", f.MustInclude))
				f.Cf.DiscardMsg(msg)
				continue
			}
		}

		// parse json
		if f.ParseJsonKey != "" {
			switch msg.Message[f.ParseJsonKey].(type) {
			case []byte:
				if err = json.Unmarshal(msg.Message[f.ParseJsonKey].([]byte), &msg.Message); err != nil {
					utils.Logger.Warn("json unmarshal connector args got error",
						zap.Error(err),
						zap.ByteString("args", msg.Message[f.ParseJsonKey].([]byte)))
				}
			case nil:
			default:
				utils.Logger.Warn("unknown args type")
			}
			delete(msg.Message, f.ParseJsonKey)
		}

		// flatten messages
		libs.FlattenMap(msg.Message)

		// add
		if _, ok = f.Add[msg.Tag]; ok {
			for k, v = range f.Add[msg.Tag] {
				msg.Message[k] = v
			}
		}

		f.OutChan <- msg
	}
}

type ParserFactCfg struct {
	Name                      string
	Tags                      []string
	Env, MsgKey               string
	Regexp                    *regexp.Regexp
	MsgPool                   *sync.Pool
	IsRemoveOrigLog           bool
	Add                       map[string]map[string]string
	ParseJsonKey, MustInclude string
}

type ParserFact struct {
	*BaseTagFilterFactory
	*ParserFactCfg
	tagsset map[string]struct{}
}

func NewParserFact(cfg *ParserFactCfg) *ParserFact {
	utils.Logger.Info("create new connectorfactory")
	cf := &ParserFact{
		BaseTagFilterFactory: &BaseTagFilterFactory{},
		ParserFactCfg:        cfg,
	}

	cf.tagsset = map[string]struct{}{}
	for _, tag := range cf.Tags {
		utils.Logger.Info("Parser factory add tag", zap.String("tag", tag+"."+cf.Env))
		cf.tagsset[tag+"."+cf.Env] = struct{}{}
	}

	return cf
}

func (cf *ParserFact) GetName() string {
	return cf.Name
}

func (cf *ParserFact) IsTagSupported(tag string) (ok bool) {
	_, ok = cf.tagsset[tag]
	return ok
}

func (cf *ParserFact) Spawn(tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn Parser tagfilter", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, cf.defaultInternalChanSize)
	f := NewParser(&ParserCfg{
		Cf:              cf,
		Tag:             tag,
		InChan:          inChan,
		OutChan:         outChan,
		MsgKey:          cf.MsgKey,
		MsgPool:         cf.MsgPool,
		Regexp:          cf.Regexp,
		IsRemoveOrigLog: cf.IsRemoveOrigLog,
		Add:             cf.Add,
		ParseJsonKey:    cf.ParseJsonKey,
		MustInclude:     cf.MustInclude,
	})
	go f.Run()
	return inChan
}
