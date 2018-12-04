package senders

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-utils"
	"github.com/Shopify/sarama"
	"go.uber.org/zap"
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
	Name                                        string
	Brokers                                     []string
	Topic                                       string
	Tags                                        []string
	InChanSize, RetryChanSize, NFork, BatchSize int
	MaxWait                                     time.Duration
}

type KafkaSender struct {
	*BaseSender
	*KafkaSenderCfg
}

func NewKafkaSender(cfg *KafkaSenderCfg) *KafkaSender {
	utils.Logger.Info("new kafka sender",
		zap.Strings("brokers", cfg.Brokers))

	if len(cfg.Brokers) == 0 {
		panic(fmt.Errorf("brokers shoule not be empty"))
	}

	s := &KafkaSender{
		BaseSender:     &BaseSender{},
		KafkaSenderCfg: cfg,
	}
	s.SetSupportedTags(cfg.Tags)
	return s
}

func (s *KafkaSender) GetName() string {
	return s.Name
}

func (s *KafkaSender) Spawn(tag string) chan<- *libs.FluentMsg {
	utils.Logger.Info("SpawnForTag", zap.String("tag", tag))
	inChan := make(chan *libs.FluentMsg, s.InChanSize)

	for i := 0; i < s.NFork; i++ {
		go func() {
			var (
				jb                []byte
				nRetry            = 0
				maxRetry          = 3
				msgBatch          = make([]*libs.FluentMsg, s.BatchSize)
				kmsgBatchDelivery = make([]*sarama.ProducerMessage, s.BatchSize)
				msgBatchDelivery  []*libs.FluentMsg
				iBatch            = 0
				lastT             = time.Unix(0, 0)
				err               error
				j                 int
			)

			for j = 0; j < s.BatchSize; j++ {
				kmsgBatchDelivery[j] = &sarama.ProducerMessage{Topic: s.Topic}
			}

		RECONNECT:
			producer, err := NewKafkaProducer(s.Brokers)
			if err != nil {
				utils.Logger.Error("connect to kakfa broker got error", zap.Error(err))
				goto RECONNECT
			}
			utils.Logger.Info("connect to kafka brokers",
				zap.Strings("brokers", s.Brokers),
				zap.String("tag", tag))

			for msg := range inChan {
				msgBatch[iBatch] = msg
				iBatch++
				if iBatch < s.BatchSize &&
					time.Now().Sub(lastT) < s.MaxWait {
					continue
				}
				lastT = time.Now()
				msgBatchDelivery = msgBatch[:iBatch]
				iBatch = 0
				nRetry = 0
				for j, msg = range msgBatchDelivery {
					if jb, err = json.Marshal(&msg.Message); err != nil {
						utils.Logger.Error("marashal msg got error",
							zap.Error(err),
							zap.String("msg", fmt.Sprintf("%+v", msg)))
						continue
					}
					kmsgBatchDelivery[j].Value = sarama.ByteEncoder(jb)
				}

				if utils.Settings.GetBool("dry") {
					utils.Logger.Info("send message to backend",
						zap.String("tag", tag),
						zap.String("log", fmt.Sprint(msgBatch[0].Message)))
					for _, msg = range msgBatchDelivery {
						s.discardChan <- msg
					}
					continue
				}
			SEND_MSG:
				if err = producer.SendMessages(kmsgBatchDelivery[:len(msgBatchDelivery)]); err != nil {
					nRetry++
					if nRetry > maxRetry {
						utils.Logger.Error("try send kafka message got error", zap.Error(err))

						utils.Logger.Error("discard msg since of sender err",
							zap.String("tag", msg.Tag),
							zap.Int("num", len(msgBatchDelivery)))
						for _, msg = range msgBatchDelivery {
							s.discardWithoutCommitChan <- msg
						}

						if err = producer.Close(); err != nil {
							utils.Logger.Error("try to close connection got error", zap.Error(err))
						}
						utils.Logger.Info("connection closed, try to reconnect...")
						goto RECONNECT
					}

					goto SEND_MSG
				}
				utils.Logger.Debug("success sent messages to brokers",
					zap.String("topic", s.Topic),
					zap.Strings("brokers", s.Brokers),
					zap.String("tag", tag))
				for _, msg = range msgBatchDelivery {
					s.discardChan <- msg
				}
			}
		}()
	}

	return inChan
}
