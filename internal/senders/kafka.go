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
	cfg.Net.DialTimeout = 3 * time.Second
	cfg.Net.ReadTimeout = 3 * time.Second
	cfg.Net.WriteTimeout = 3 * time.Second
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
	newProducer func([]string) (sarama.SyncProducer, error)
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
	s := &KafkaSender{
		BaseSender: &BaseSender{
			IsDiscardWhenBlocked: cfg.IsDiscardWhenBlocked,
		},
		KafkaSenderCfg: cfg,
		newProducer:    NewKafkaProducer,
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
			closeProducer := func() {
				if producer != nil {
					if err := producer.Close(); err != nil {
						log.Logger.Warn("close Kafka producer", zap.Error(err))
					}
					producer = nil
				}
			}
			defer closeProducer()
			connect := func() bool {
				for producer == nil {
					if ctx.Err() != nil {
						return false
					}
					var err error
					producer, err = s.newProducer(s.Brokers)
					if err == nil {
						return true
					}
					log.Logger.Warn("connect Kafka producer", zap.Error(err))
					timer := time.NewTimer(100 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return false
					case <-timer.C:
					}
				}
				return true
			}
			runBatchWorker(ctx, in, s.BatchSize, s.MaxWait, func(msgs []*library.FluentMsg) bool {
				if ctx.Err() != nil {
					return false
				}
				records := make([]*sarama.ProducerMessage, 0, len(msgs))
				for _, msg := range msgs {
					if msg == nil {
						return s.reportBatch(ctx, msgs, false)
					}
					payload, err := utils.JSON.Marshal(msg.Message)
					if err != nil {
						log.Logger.Warn("encode Kafka batch", zap.Error(err))
						return s.reportBatch(ctx, msgs, false)
					}
					records = append(records, &sarama.ProducerMessage{Topic: s.Topic, Value: sarama.ByteEncoder(payload)})
				}
				if utils.Settings.GetBool("dry") {
					return s.reportBatch(ctx, msgs, true)
				}
				if !connect() {
					return false
				}
				var err error
				for attempt := 0; attempt < 4; attempt++ {
					if ctx.Err() != nil {
						return false
					}
					err = producer.SendMessages(records)
					if err == nil {
						break
					}
				}
				if err != nil {
					closeProducer()
				}
				return s.reportBatch(ctx, msgs, err == nil)
			})
		}()
	}
	return in
}
