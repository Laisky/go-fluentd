package recvs

import (
	"encoding/json"
	"sync"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/kafka"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"github.com/Laisky/go-fluentd/libs"
)

func GetKafkaRewriteTag(rewriteTag, env string) string {
	if rewriteTag == "" {
		return ""
	}

	return rewriteTag + "." + env
}

type KafkaCommitCfg struct {
	IntervalNum      int
	IntervalDuration time.Duration
}

type KafkaCfg struct {
	*KafkaCommitCfg
	Topics, Brokers                  []string
	Group, Tag, MsgKey, TagKey, Name string
	NConsumer                        int
	KMsgPool                         *sync.Pool
	Meta                             map[string]interface{}
	IsJsonFormat                     bool   // is unmarshal kafka msg
	JsonTagKey                       string // load tag from kafka message(only work when IsJsonFormat is true)
	RewriteTag                       string
}

type KafkaRecv struct {
	*BaseRecv
	*KafkaCfg
}

func NewKafkaRecv(cfg *KafkaCfg) *KafkaRecv {
	utils.Logger.Info("create KafkaRecv",
		zap.Strings("topics", cfg.Topics),
		zap.Strings("brokers", cfg.Brokers),
		zap.Bool("IsJsonFormat", cfg.IsJsonFormat),
		zap.String("TagKey", cfg.TagKey),
		zap.String("JsonTagKey", cfg.JsonTagKey))
	return &KafkaRecv{
		BaseRecv: &BaseRecv{},
		KafkaCfg: cfg,
	}
}

func (r *KafkaRecv) GetName() string {
	return r.Name
}

func (r *KafkaRecv) Run() {
	utils.Logger.Info("run KafkaRecv")
	for i := 0; i < r.NConsumer; i++ {
		go func(i int) {
			for {
				cli, err := kafka.NewKafkaCliWithGroupId(&kafka.KafkaCliCfg{
					Brokers:          r.Brokers,
					Topics:           r.Topics,
					Groupid:          r.Group,
					KMsgPool:         r.KMsgPool,
					IntervalNum:      r.IntervalNum,
					IntervalDuration: r.IntervalDuration,
				})
				if err != nil {
					utils.Logger.Error("try to connect to kafka got error", zap.Error(err))
					continue
				}
				utils.Logger.Info("connect to kafka brokers",
					zap.Strings("brokers", r.Brokers),
					zap.Strings("topics", r.Topics),
					zap.Int("intervalnum", r.IntervalNum),
					zap.Int("nconsumer", r.NConsumer),
					zap.Duration("intervalduration", r.IntervalDuration),
					zap.String("group", r.Group))

				var (
					kmsg *kafka.KafkaMsg
					msg  *libs.FluentMsg
				)
				for kmsg = range cli.Messages() { // receive new kmsg, and convert to fluent msg
					utils.Logger.Debug("got new message from kafka",
						zap.Int("n", i),
						zap.ByteString("msg", kmsg.Message),
						zap.String("name", r.GetName()))
					if msg, err = r.parse2Msg(kmsg); err != nil {
						utils.Logger.Error("try to parse kafka message got error",
							zap.String("name", r.GetName()),
							zap.Error(err),
							zap.ByteString("log", kmsg.Message))
						cli.CommitWithMsg(kmsg)
						continue
					}

					r.syncOutChan <- msg // blockable
					cli.CommitWithMsg(kmsg)
				}

				cli.Close()
			}
		}(i)
	}
}

// parse2Msg parse kafkamsg to fluentdmsg
func (r *KafkaRecv) parse2Msg(kmsg *kafka.KafkaMsg) (msg *libs.FluentMsg, err error) {
	msg = r.msgPool.Get().(*libs.FluentMsg)
	msg.Id = r.counter.Count()
	msg.Tag = r.Tag

	// remove old messages log
	msg.Message = map[string]interface{}{}

	for metaK, metaV := range r.Meta {
		msg.Message[metaK] = []byte(metaV.(string))
	}

	if r.IsJsonFormat {
		if err = json.Unmarshal(kmsg.Message, &msg.Message); err != nil {
			r.msgPool.Put(msg)
			return nil, errors.Wrap(err, "try to unmarshal kmsg got error")
		}

		if r.JsonTagKey != "" { // load msg.Tag from json
			switch msg.Message[r.JsonTagKey].(type) {
			case []byte:
				msg.Tag = string(msg.Message[r.JsonTagKey].([]byte))
			case string:
				msg.Tag = msg.Message[r.JsonTagKey].(string)
			default:
				utils.Logger.Error("unknown tagkey format", zap.String("tagkey", r.JsonTagKey))
				r.msgPool.Put(msg)
				return nil, errors.New("unknown tagkey format")
			}
		}
	} else {
		msg.Message[r.MsgKey] = kmsg.Message
	}

	msg.Message[r.TagKey] = msg.Tag
	utils.Logger.Debug("parse2Msg got new msg",
		zap.String("tag", msg.Tag),
		zap.String("rewrite_tag", r.RewriteTag),
		zap.ByteString("msg", kmsg.Message))
	if r.RewriteTag != "" {
		msg.Tag = r.RewriteTag
	}
	return msg, nil
}
