package tagfilters

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/Laisky/zap"
)

func (cf *ParserFact) StartNewParser(ctx context.Context, outChan chan<- *library.FluentMsg, inChan <-chan *library.FluentMsg) {
	defer log.Logger.Info("parser runner exit")
	var (
		err error
		ok  bool
		msg *library.FluentMsg
		v   string
		t   time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-inChan:
			if !ok {
				log.Logger.Info("inChan closed")
				return
			}
		}

		if !cf.IsTagSupported(msg.Tag) {
			select {
			case outChan <- msg:
			case <-ctx.Done():
				return
			}
			continue
		}

		if cf.MsgKey != "" {
			switch msg.Message[cf.MsgKey].(type) {
			case []byte:
			case string:
				msg.Message[cf.MsgKey] = []byte(msg.Message[cf.MsgKey].(string))
			default:
				log.Logger.Warn("unknon msg_key",
					zap.String("tag", msg.Tag),
					zap.String("msg", fmt.Sprint(msg.Message)),
					zap.String("msg_key", cf.MsgKey))
				select {
				case outChan <- msg:
				case <-ctx.Done():
					return
				}
				continue
			}

			// parse log string
			if cf.Regexp != nil {
				if err = library.RegexNamedSubMatch(cf.Regexp, msg.Message[cf.MsgKey].([]byte), msg.Message); err != nil {
					log.Logger.Warn("discard message since format not matched",
						zap.String("tag", msg.Tag),
						zap.ByteString("log", msg.Message[cf.MsgKey].([]byte)))
					select {
					case cf.waitCommitChan <- msg:
					case <-ctx.Done():
						return
					}
					continue
				}
			}

			// remove origin log
			if cf.IsRemoveOrigLog {
				delete(msg.Message, cf.MsgKey)
			}
		}

		// Decode into a temporary map. Invalid or null JSON must not erase
		// or partially overwrite fields in the original record.
		if cf.ParseJSONKey != "" {
			var payload []byte
			switch value := msg.Message[cf.ParseJSONKey].(type) {
			case string:
				payload = []byte(value)
			case []byte:
				payload = value
			}
			var parsed map[string]interface{}
			if len(payload) > 0 && json.Unmarshal(payload, &parsed) == nil && parsed != nil {
				for key, value := range parsed {
					msg.Message[key] = value
				}
				delete(msg.Message, cf.ParseJSONKey)
			}
		}
		// flatten messages
		library.FlattenMap(msg.Message, "__") // do not use `.` as delimiter!

		// MustInclude
		if cf.MustInclude != "" {
			if _, ok = msg.Message[cf.MustInclude]; !ok {
				log.Logger.Warn("dicard since of missing key", zap.String("key", cf.MustInclude))
				select {
				case cf.waitCommitChan <- msg:
				case <-ctx.Done():
					return
				}
				continue
			}
		}

		// parse time
		if cf.TimeKey != "" {
			switch ts := msg.Message[cf.TimeKey].(type) {
			case []byte:
				if cf.AppendTimeZone != "" {
					v = string(ts) + " " + cf.AppendTimeZone
				} else {
					v = string(ts)
				}
			case string:
				if cf.AppendTimeZone != "" {
					v = ts + " " + cf.AppendTimeZone
				} else {
					v = ts
				}
			default:
				log.Logger.Warn("discard since unknown time format",
					zap.Error(err),
					zap.String("ts", fmt.Sprint(msg.Message[cf.TimeKey])),
					zap.String("tag", msg.Tag),
					zap.String("time_key", cf.TimeKey),
					zap.String("time_format", cf.TimeFormat),
					zap.String("append_time_zone", cf.AppendTimeZone))
				select {
				case cf.waitCommitChan <- msg:
				case <-ctx.Done():
					return
				}
				continue
			}

			v = strings.Replace(v, ",", ".", -1)
			if t, err = time.Parse(cf.TimeFormat, v); err != nil {
				log.Logger.Warn("discard since parse time got error",
					zap.Error(err),
					zap.String("ts", v),
					zap.String("tag", msg.Tag),
					zap.String("time_key", cf.TimeKey),
					zap.String("time_format", cf.TimeFormat),
					zap.String("append_time_zone", cf.AppendTimeZone))
				select {
				case cf.waitCommitChan <- msg:
				case <-ctx.Done():
					return
				}
				continue
			}

			if !cf.ReservedTimeKey {
				delete(msg.Message, cf.TimeKey)
			}

			msg.Message[cf.NewTimeKey] = t.UTC().Format(cf.NewTimeFormat)

		}

		library.ProcessAdd(cf.AddCfg, msg)
		select {
		case outChan <- msg:
		case <-ctx.Done():
			return
		}
	}
}

type ParserFactCfg struct {
	NFork           int
	Name, LBKey     string
	Tags            []string
	MsgKey          string
	Regexp          *regexp.Regexp
	MsgPool         *sync.Pool
	IsRemoveOrigLog bool
	AddCfg          library.AddCfg
	ParseJSONKey,
	MustInclude string
	TimeKey,
	TimeFormat,
	NewTimeKey,
	AppendTimeZone,
	NewTimeFormat string
	ReservedTimeKey bool
}

type ParserFact struct {
	*BaseTagFilterFactory
	*ParserFactCfg
	tagsset map[string]struct{}
}

func NewParserFact(cfg *ParserFactCfg) *ParserFact {
	cf := &ParserFact{
		BaseTagFilterFactory: &BaseTagFilterFactory{},
		ParserFactCfg:        cfg,
		tagsset:              map[string]struct{}{},
	}
	if err := cf.valid(); err != nil {
		log.Logger.Panic("new parser", zap.Error(err))
	}

	for _, tag := range cf.Tags {
		cf.tagsset[tag] = struct{}{}
	}

	log.Logger.Info("new parser",
		zap.Int("n_fork", cf.NFork),
		zap.Strings("tags", cf.Tags),
		zap.String("msg_key", cf.MsgKey),
		zap.String("time_key", cf.TimeKey),
		zap.String("new_time_format", cf.NewTimeFormat),
		zap.String("new_time_key", cf.NewTimeKey),
		zap.String("msg_key", cf.MsgKey),
	)
	return cf
}

func (cf *ParserFact) valid() error {
	if cf.NFork < 1 {
		cf.NFork = 4
		log.Logger.Info("reset n_fork", zap.Int("n_fork", cf.NFork))
	}

	if cf.NewTimeFormat == "" {
		cf.NewTimeFormat = "2006-01-02T15:04:05.000000Z"
		log.Logger.Info("reset new_time_format", zap.String("new_time_format", cf.NewTimeFormat))
	}

	if cf.NewTimeKey == "" {
		cf.NewTimeKey = "@timestamp"
		log.Logger.Info("reset new_time_key", zap.String("new_time_key", cf.NewTimeKey))
	}

	return nil
}

func (cf *ParserFact) GetName() string {
	return cf.Name + "-parser"
}

func (cf *ParserFact) IsTagSupported(tag string) (ok bool) {
	_, ok = cf.tagsset[tag]
	return ok
}

func (cf *ParserFact) Spawn(ctx context.Context, tag string, outChan chan<- *library.FluentMsg) chan<- *library.FluentMsg {
	log.Logger.Info("spawn parser tagfilter", zap.String("tag", tag))
	inChan := make(chan *library.FluentMsg, cf.defaultInternalChanSize)

	inchans := []chan *library.FluentMsg{}
	for i := 0; i < cf.NFork; i++ {
		eachInchan := make(chan *library.FluentMsg, cf.defaultInternalChanSize)
		go cf.StartNewParser(ctx, outChan, eachInchan)
		inchans = append(inchans, eachInchan)
	}

	go cf.runLB(ctx, cf.LBKey, inChan, inchans)
	return inChan
}
