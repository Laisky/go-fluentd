package recvs

import (
	"context"
	"sync"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-kafka"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

const (
	defaultKafkaReconnectInterval = 1 * time.Hour
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

func NewKafkaCfg() *KafkaCfg {
	return &KafkaCfg{
		ReconnectInterval: defaultKafkaReconnectInterval,
	}
}

/*KafkaCfg kafka client configuration

Args:
	IsJSONFormat: unmarshal json into `msg.Message`
	MsgKey: put kafka msg body into `msg.Message[MsgKey]`
	TagKey: set tag into `msg.Message[TagKey]`
	Name: name of this recv plugin
	KMsgPool: sync.Pool for `*utils.kafka.KafkaMsg`
	Meta: add new field and value into `msg.Message`
	JSONTagKey: load tag from kafka message(only work when IsJSONFormat is true)
	RewriteTag: rewrite `msg.Tag`, `msg.Message["tag"]` will keep origin value
	ReconnectInterval: restart consumer periodically
*/
type KafkaCfg struct {
	*KafkaCommitCfg
	Topics, Brokers                  []string
	Group, Tag, MsgKey, TagKey, Name string
	NConsumer                        int
	KMsgPool                         *sync.Pool
	Meta                             map[string]interface{}
	IsJSONFormat                     bool
	JSONTagKey                       string
	RewriteTag                       string
	ReconnectInterval                time.Duration
}

type KafkaRecv struct {
	*BaseRecv
	*KafkaCfg
}

func NewKafkaRecv(cfg *KafkaCfg) *KafkaRecv {
	if cfg.MsgKey == "" && !cfg.IsJSONFormat {
		utils.Logger.Panic("at least set one of MsgKey and IsJSONFormat")
	}
	if cfg.ReconnectInterval < defaultKafkaReconnectInterval {
		utils.Logger.Warn("ReconnectInterval too small", zap.Duration("ReconnectInterval", cfg.ReconnectInterval))
	}

	utils.Logger.Info("create KafkaRecv",
		zap.Strings("topics", cfg.Topics),
		zap.Strings("brokers", cfg.Brokers),
		zap.Bool("IsJSONFormat", cfg.IsJSONFormat),
		zap.String("TagKey", cfg.TagKey),
		zap.String("JSONTagKey", cfg.JSONTagKey))

	return &KafkaRecv{
		BaseRecv: &BaseRecv{},
		KafkaCfg: cfg,
	}
}

func (r *KafkaRecv) GetName() string {
	return r.Name
}

func (r *KafkaRecv) Run(ctx context.Context) {
	utils.Logger.Info("run KafkaRecv")
	for i := 0; i < r.NConsumer; i++ {
		go func(i int) {
			defer utils.Logger.Info("kafka reciver exit", zap.Int("n", i))
			var (
				ok           bool
				kmsg         *kafka.KafkaMsg
				msg          *libs.FluentMsg
				ctx2Consumer context.Context
				cancel       func()
			)

			for {
				select {
				case <-ctx.Done():
					if cancel != nil {
						cancel()
					}
					return
				default:
				}

				ctx2Consumer, cancel = context.WithTimeout(ctx, r.ReconnectInterval)
				cli, err := kafka.NewKafkaCliWithGroupID(
					ctx2Consumer,
					&kafka.KafkaCliCfg{
						Brokers:  r.Brokers,
						Topics:   r.Topics,
						Groupid:  r.Group,
						KMsgPool: r.KMsgPool,
					},
					kafka.WithCommitFilterCheckInterval(r.IntervalDuration),
					kafka.WithCommitFilterCheckNum(r.IntervalNum),
				)
				if err != nil {
					utils.Logger.Error("try to connect to kafka got error", zap.Error(err))
					cancel()
					continue
				}
				utils.Logger.Info("connect to kafka brokers",
					zap.Strings("brokers", r.Brokers),
					zap.Strings("topics", r.Topics),
					zap.Int("intervalnum", r.IntervalNum),
					zap.Int("nconsumer", r.NConsumer),
					zap.Duration("intervalduration", r.IntervalDuration),
					zap.String("group", r.Group))

				msgChan := cli.Messages(ctx)
			CONSUMER_LOOP:
				for { // receive new kmsg, and convert to fluent msg
					select {
					case <-ctx2Consumer.Done():
						break CONSUMER_LOOP
					case kmsg, ok = <-msgChan:
						if !ok {
							utils.Logger.Info("consumer break")
							cancel()
							break CONSUMER_LOOP
						}
					}

					utils.Logger.Debug("got new message from kafka",
						zap.Int("n", i),
						zap.Int32("partition", kmsg.Partition),
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
		if metaV.(string) == RandomValOperator {
			msg.Message[metaK] = utils.RandomStringWithLength(10)
			continue
		}
		msg.Message[metaK] = []byte(metaV.(string))
	}

	if r.IsJSONFormat {
		if err = json.Unmarshal(kmsg.Message, &msg.Message); err != nil {
			r.msgPool.Put(msg)
			return nil, errors.Wrap(err, "try to unmarshal kmsg got error")
		}

		if r.JSONTagKey != "" { // load msg.Tag from json
			switch msg.Message[r.JSONTagKey].(type) {
			case []byte:
				msg.Tag = string(msg.Message[r.JSONTagKey].([]byte))
			case string:
				msg.Tag = msg.Message[r.JSONTagKey].(string)
			default:
				utils.Logger.Error("discard log since unknown JSONTagKey format", zap.String("tagkey", r.JSONTagKey))
				r.msgPool.Put(msg)
				return nil, errors.New("unknown JSONTagKey format")
			}
		}
	} else {
		msg.Message[r.MsgKey] = kmsg.Message
	}

	msg.Message[r.TagKey] = msg.Tag
	// utils.Logger.Debug("parse2Msg got new msg",
	// 	zap.String("tag", msg.Tag),
	// 	zap.String("rewrite_tag", r.RewriteTag),
	// 	zap.ByteString("msg", kmsg.Message))
	if r.RewriteTag != "" {
		msg.Tag = r.RewriteTag
	}
	return msg, nil
}
