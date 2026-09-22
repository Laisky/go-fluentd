package senders

import (
	"context"
	"fmt"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

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
	log.Logger.Info("new kafka sender",
		zap.Strings("brokers", cfg.Brokers))

	if len(cfg.Brokers) == 0 {
		panic(fmt.Errorf("brokers shoule not be empty"))
	}

	if cfg.NFork <= 0 {
		cfg.NFork = 1
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 5 * time.Second
	}
	if cfg.InChanSize < 0 {
		cfg.InChanSize = 0
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
	in := make(chan *library.FluentMsg, s.InChanSize)
	for i := 0; i < s.NFork; i++ {
		go func() {
			var producer sarama.SyncProducer
			defer func() {
				if producer != nil {
					if err := producer.Close(); err != nil {
						log.Logger.Warn("close Kafka producer", zap.Error(err))
					}
				}
			}()
			s.runBatches(ctx, in, s.BatchSize, s.MaxWait, func(ctx context.Context, msgs []*library.FluentMsg) error {
				// Prepare every record before issuing any request. Reusing Sarama messages
				// after a marshal error can resend a previous record as the current one.
				encoded := make([]*sarama.ProducerMessage, len(msgs))
				for i, msg := range msgs {
					b, err := utils.JSON.Marshal(msg.Message)
					if err != nil {
						return fmt.Errorf("encode Kafka record %d: %w", i, err)
					}
					encoded[i] = &sarama.ProducerMessage{Topic: s.Topic, Value: sarama.ByteEncoder(b)}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if producer == nil {
					var err error
					producer, err = NewKafkaProducer(s.Brokers)
					if err != nil {
						return err
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := producer.SendMessages(encoded); err != nil {
					producer.Close()
					producer = nil
					return err
				}
				return nil
			})
		}()
	}
	return in
}
