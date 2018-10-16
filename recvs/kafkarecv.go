package recvs

import (
	"sync"
	"time"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/kafka"
	"github.com/pkg/errors"
)

type KafkaCommitCfg struct {
	IntervalNum      int
	IntervalDuration time.Duration
}

type KafkaCfg struct {
	*KafkaCommitCfg
	Topics, Brokers    []string
	Group, Tag, MsgKey string
	NConsumer          int
	KMsgPool           *sync.Pool
	Meta               map[string]interface{}
}

type KafkaRecv struct {
	*BaseRecv
	*KafkaCfg
}

func NewKafkaRecv(cfg *KafkaCfg) *KafkaRecv {
	utils.Logger.Info("create KafkaRecv")
	return &KafkaRecv{
		KafkaCfg: cfg,
		BaseRecv: &BaseRecv{},
	}
}

func (r *KafkaRecv) Run() {
	utils.Logger.Info("run KafkaRecv")

	for i := 0; i < r.NConsumer; i++ {
		go func() {
			cli, err := kafka.NewKafkaCliWithGroupId(&kafka.KafkaCliCfg{
				Brokers:          r.Brokers,
				Topics:           r.Topics,
				Groupid:          r.Group,
				KMsgPool:         r.KMsgPool,
				IntervalNum:      r.IntervalNum,
				IntervalDuration: r.IntervalDuration,
			})
			if err != nil {
				panic(errors.Wrap(err, "try to connect to kafka got error"))
			}

			var (
				kmsg  *kafka.KafkaMsg
				msg   *libs.FluentMsg
				metaK string
				metaV interface{}
			)
			for {
				for kmsg = range cli.Messages() { // receive new kmsg, and convert to fluent msg
					msg = r.msgPool.Get().(*libs.FluentMsg)
					msg.Id = r.counter.Count()
					msg.Tag = r.Tag
					if msg.Message == nil {
						msg.Message = map[string]interface{}{}
					}
					for metaK, metaV = range r.Meta {
						msg.Message[metaK] = []byte(metaV.(string))
					}
					msg.Message[r.MsgKey] = kmsg.Message
					r.outChan <- msg
					cli.CommitWithMsg(kmsg)
				}
			}
		}()
	}
}
