package tagFilters

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
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
	Cf              *ParserFact
	Tag, MsgKey     string
	Regexp          *regexp.Regexp
	OutChan         chan<- *libs.FluentMsg
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
	Add             map[string]map[string]string
	ParseJsonKey,
	MustInclude,
	TimeKey,
	TimeFormat,
	NewTimeKey,
	AppendTimeZone,
	NewTimeFormat string
	ReservedTimeKey bool
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

func (f *Parser) Run(inChan <-chan *libs.FluentMsg) {
	var (
		err error
		ok  bool
		msg *libs.FluentMsg
		// vi   interface{}
		k, v string
		t    time.Time
	)
	for msg = range inChan {
		if !f.Cf.IsTagSupported(msg.Tag) {
			f.OutChan <- msg
		}

		if f.MsgKey != "" {
			switch msg.Message[f.MsgKey].(type) {
			case []byte:
			case string:
				msg.Message[f.MsgKey] = []byte(msg.Message[f.MsgKey].(string))
			default:
				utils.Logger.Warn("msg key not exists or unknown type",
					zap.String("tag", msg.Tag),
					zap.String("msg", fmt.Sprint(msg.Message)),
					zap.String("msg_key", f.MsgKey))
				f.OutChan <- msg
				continue
			}

			// parse log string
			if f.Regexp != nil {
				if err = libs.RegexNamedSubMatch(f.Regexp, msg.Message[f.MsgKey].([]byte), msg.Message); err != nil {
					utils.Logger.Warn("message format not matched",
						zap.String("tag", msg.Tag),
						zap.ByteString("log", msg.Message[f.MsgKey].([]byte)))
					f.Cf.DiscardMsg(msg)
					continue
				}
			}

			// remove origin log
			if f.IsRemoveOrigLog {
				delete(msg.Message, f.MsgKey)
			}
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
		ok = false
		if f.ParseJsonKey != "" {
			switch log := msg.Message[f.ParseJsonKey].(type) {
			case string:
				if err = json.UnmarshalFromString(log, &msg.Message); err != nil {
					utils.Logger.Warn("json unmarshal JSON args got error",
						zap.String("tag", msg.Tag),
						zap.Error(err),
						zap.Int64s("ext-ids", msg.ExtIds),
						zap.Int64("id", msg.Id),
						zap.String("args", log))
				} else {
					ok = true
				}
			case []byte:
				if err = json.Unmarshal(log, &msg.Message); err != nil {
					utils.Logger.Warn("json unmarshal JSON args got error",
						zap.String("tag", msg.Tag),
						zap.Error(err),
						zap.Int64s("ext-ids", msg.ExtIds),
						zap.Int64("id", msg.Id),
						zap.ByteString("args", log))
				} else {
					ok = true
				}
			case nil:
				utils.Logger.Warn("json key does not exists", zap.String("tag", msg.Tag))
			default:
				utils.Logger.Warn("unknown args type", zap.String("tag", msg.Tag))
			}

			if ok { // if failed to parse json, reserve origin args
				delete(msg.Message, f.ParseJsonKey)
			}
		}

		// flatten messages
		libs.FlattenMap(msg.Message, "__") // do not use `.` as delimiter!

		// // trim
		// for k, vi = range msg.Message {
		// 	switch log := vi.(type) {
		// 	case string:
		// 		msg.Message[k] = strings.TrimSpace(log)
		// 	case []byte:
		// 		msg.Message[k] = bytes.TrimSpace(log)
		// 	}
		// }

		// add
		if _, ok = f.Add[msg.Tag]; ok {
			for k, v = range f.Add[msg.Tag] {
				msg.Message[k] = v
			}
		}

		// parse time
		if f.TimeKey != "" {
			switch ts := msg.Message[f.TimeKey].(type) {
			case []byte:
				if f.AppendTimeZone != "" {
					v = string(ts) + " " + f.AppendTimeZone
				} else {
					v = string(ts)
				}
			case string:
				if f.AppendTimeZone != "" {
					v = ts + " " + f.AppendTimeZone
				} else {
					v = ts
				}
			default:
				utils.Logger.Warn("unknown time format",
					zap.Error(err),
					zap.String("ts", fmt.Sprint(msg.Message[f.TimeKey])),
					zap.String("tag", msg.Tag),
					zap.String("time_key", f.TimeKey),
					zap.String("time_format", f.TimeFormat),
					zap.String("append_time_zone", f.AppendTimeZone))
				f.Cf.DiscardMsg(msg)
				continue
			}

			v = strings.Replace(v, ",", ".", -1)
			if t, err = time.Parse(f.TimeFormat, v); err != nil {
				utils.Logger.Warn("parse time got error",
					zap.Error(err),
					zap.String("ts", v),
					zap.String("tag", msg.Tag),
					zap.String("time_key", f.TimeKey),
					zap.String("time_format", f.TimeFormat),
					zap.String("append_time_zone", f.AppendTimeZone))
				f.Cf.DiscardMsg(msg)
				continue
			}

			if !f.ReservedTimeKey {
				delete(msg.Message, f.TimeKey)
			}

			msg.Message[f.NewTimeKey] = t.UTC().Format(f.NewTimeFormat)
		}

		f.OutChan <- msg
	}
}

type ParserFactCfg struct {
	NFork                                                          int
	Name, LBKey                                                    string
	Tags                                                           []string
	Env, MsgKey                                                    string
	Regexp                                                         *regexp.Regexp
	MsgPool                                                        *sync.Pool
	IsRemoveOrigLog                                                bool
	Add                                                            map[string]map[string]string
	ParseJsonKey, MustInclude                                      string
	TimeKey, TimeFormat, NewTimeKey, AppendTimeZone, NewTimeFormat string
	ReservedTimeKey                                                bool
}

type ParserFact struct {
	*BaseTagFilterFactory
	*ParserFactCfg
	tagsset map[string]struct{}
}

func NewParserFact(cfg *ParserFactCfg) *ParserFact {
	utils.Logger.Info("create new connectorfactory")

	if cfg.NFork < 1 {
		utils.Logger.Panic("nfork should bigger than 1")
	}

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
	return cf.Name + "-parser"
}

func (cf *ParserFact) IsTagSupported(tag string) (ok bool) {
	_, ok = cf.tagsset[tag]
	return ok
}

func (cf *ParserFact) Spawn(tag string, outChan chan<- *libs.FluentMsg) chan<- *libs.FluentMsg {
	utils.Logger.Info("spawn parser tagfilter", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, cf.defaultInternalChanSize)

	inchans := []chan *libs.FluentMsg{}
	for i := 0; i < cf.NFork; i++ {
		f := NewParser(&ParserCfg{
			Cf:              cf,
			Tag:             tag,
			OutChan:         outChan,
			MsgKey:          cf.MsgKey,
			MsgPool:         cf.MsgPool,
			Regexp:          cf.Regexp,
			IsRemoveOrigLog: cf.IsRemoveOrigLog,
			Add:             cf.Add,
			ParseJsonKey:    cf.ParseJsonKey,
			MustInclude:     cf.MustInclude,
			TimeKey:         cf.TimeKey,
			TimeFormat:      cf.TimeFormat,
			NewTimeKey:      cf.NewTimeKey,
			AppendTimeZone:  cf.AppendTimeZone,
			NewTimeFormat:   cf.NewTimeFormat,
			ReservedTimeKey: cf.ReservedTimeKey,
		})
		eachInchan := make(chan *libs.FluentMsg, cf.defaultInternalChanSize)
		go f.Run(eachInchan)
		inchans = append(inchans, eachInchan)
	}

	go cf.runLB(cf.LBKey, inChan, inchans)
	return inChan
}
