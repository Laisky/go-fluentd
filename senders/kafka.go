package senders

import (
	"context"
	"fmt"
	"time"

	"gofluentd/library"

	"github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/Shopify/sarama"
)

func NewKafkaProducer(brokers []string) (p sarama.SyncProducer, err error) {
	cfg := sarama.NewConfig()
	cfg.Producer.MaxMessageBytes = 1048576
	cfg.Producer.RequiredAcks = sarama.WaitForLocal
	cfg.Producer.Partitioner = sarama.NewRandomPartitioner
	cfg.Producer.Retry.Max = 3
	cfg.Producer.Return.Successes = true
	cfg.Producer.Timeout = 3 * time.Second
	return sarama.NewSyncProducer(brokers, cfg)
}

type KafkaSenderCfg struct {
	Name, TagKey                 string
	Brokers                      []string
	Topic                        string
	Tags                         []string
	InChanSize, NFork, BatchSize int
	MaxWait                      time.Duration
	IsDiscardWhenBlocked         bool
}

type KafkaSender struct {
	*BaseSender
	*KafkaSenderCfg
}

func NewKafkaSender(cfg *KafkaSenderCfg) *KafkaSender {
	library.Logger.Info("new kafka sender",
		zap.Strings("brokers", cfg.Brokers))

	if len(cfg.Brokers) == 0 {
		panic(fmt.Errorf("brokers shoule not be empty"))
	}

	s := &KafkaSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		KafkaSenderCfg: cfg,
	}
	s.SetSupportedTags(cfg.Tags)
	return s
}

func (s *KafkaSender) GetName() string {
	return s.Name
}

func (s *KafkaSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	library.Logger.Info("SpawnForTag")
	inChan := make(chan *library.FluentMsg, s.InChanSize)

	for i := 0; i < s.NFork; i++ {
		go func(i int) {
			defer library.Logger.Info("kafka sender exit",
				zap.String("name", s.GetName()),
				zap.Int("i", i))
			var (
				jb                []byte
				nRetry            int
				maxRetry          = 3
				msgBatch          = make([]*library.FluentMsg, s.BatchSize)
				kmsgBatchDelivery = make([]*sarama.ProducerMessage, s.BatchSize)
				msgBatchDelivery  []*library.FluentMsg
				iBatch            = 0
				lastT             = time.Unix(0, 0)
				err               error
				j                 int
				msg               *library.FluentMsg
				ok                bool
				ticker            = time.NewTicker(s.MaxWait)
			)
			defer ticker.Stop()

			for j = 0; j < s.BatchSize; j++ {
				kmsgBatchDelivery[j] = &sarama.ProducerMessage{Topic: s.Topic}
			}

		RECONNECT:
			producer, err := NewKafkaProducer(s.Brokers)
			if err != nil {
				library.Logger.Error("connect to kakfa broker got error", zap.Error(err))
				goto RECONNECT
			}
			library.Logger.Info("connect to kafka brokers",
				zap.Strings("brokers", s.Brokers))

			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok = <-inChan:
					if !ok {
						library.Logger.Info("inChan closed")
						return
					}
					msgBatch[iBatch] = msg
					iBatch++
				case <-ticker.C:
					if iBatch == 0 {
						continue
					}
					msg = msgBatch[iBatch-1]
				}

				if iBatch < s.BatchSize &&
					utils.Clock.GetUTCNow().Sub(lastT) < s.MaxWait {
					continue
				}

				lastT = utils.Clock.GetUTCNow()
				msgBatchDelivery = msgBatch[:iBatch]
				iBatch = 0
				nRetry = 0
				for j, msg = range msgBatchDelivery {
					if jb, err = utils.JSON.Marshal(&msg.Message); err != nil {
						library.Logger.Error("marashal msg got error",
							zap.Error(err),
							zap.String("msg", fmt.Sprint(msg)))
						// TODO(potential bug): should remove element in msgBatchDelivery
						continue
					}

					if utils.Settings.GetBool("dry") {
						library.Logger.Info("send message to backend",
							zap.ByteString("msg", jb))
						s.successedChan <- msg
						continue
					}

					kmsgBatchDelivery[j].Value = sarama.ByteEncoder(jb)
				}

				if utils.Settings.GetBool("dry") {
					continue
				}

			SEND_MSG:
				if err = producer.SendMessages(kmsgBatchDelivery[:len(msgBatchDelivery)]); err != nil {
					nRetry++
					if nRetry > maxRetry {
						library.Logger.Error("try send kafka message got error", zap.Error(err))

						library.Logger.Error("discard msg since of sender err",
							zap.String("tag", msg.Tag),
							zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.failedChan <- msg
						}

						if err = producer.Close(); err != nil {
							library.Logger.Error("try to close connection got error", zap.Error(err))
						}
						library.Logger.Info("connection closed, try to reconnect...")
						goto RECONNECT
					}

					goto SEND_MSG
				}
				library.Logger.Debug("success sent messages to brokers",
					zap.Int("batch", len(msgBatchDelivery)),
					zap.String("topic", s.Topic),
					zap.Strings("brokers", s.Brokers),
					zap.String("tag", msg.Tag))
				for _, msg = range msgBatchDelivery {
					s.successedChan <- msg
				}
			}
		}(i)
	}

	return inChan
}
