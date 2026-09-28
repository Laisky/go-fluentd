package recvs

import (
	"context"
	"time"

	"gofluentd/library"
	"gofluentd/library/log"

	"github.com/IBM/sarama"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

const (
	defaultKafkaReconnectInterval = 1 * time.Hour
	kafkaRetryInterval            = 100 * time.Millisecond
)

func GetKafkaRewriteTag(rewriteTag, env string) string {
	if rewriteTag == "" {
		return ""
	}
	return rewriteTag + "." + env
}

type KafkaCommitCfg struct {
	library.AddCfg
	IntervalNum      int
	IntervalDuration time.Duration
}

// KafkaCfg configures Kafka input. JSONTagKey selects the input tag before
// RewriteTag is applied; TagKey retains that original tag in the payload.
// ReconnectInterval bounds the lifetime of each consumer group client.
type KafkaCfg struct {
	KafkaCommitCfg
	Topics, Brokers                  []string
	Group, Tag, MsgKey, TagKey, Name string
	NConsumer                        int
	IsJSONFormat                     bool
	JSONTagKey                       string
	RewriteTag                       string
	ReconnectInterval                time.Duration
}

// kafkaConsumerGroup is the portion of Sarama's group API owned by this input.
type kafkaConsumerGroup interface {
	Consume(context.Context, []string, sarama.ConsumerGroupHandler) error
	Errors() <-chan error
	Close() error
}

type KafkaRecv struct {
	BaseRecv
	*KafkaCfg
	newConsumerGroup func([]string, string, *sarama.Config) (kafkaConsumerGroup, error)
}

func NewKafkaRecv(cfg *KafkaCfg) *KafkaRecv {
	r := &KafkaRecv{
		KafkaCfg: cfg,
		newConsumerGroup: func(brokers []string, group string, cfg *sarama.Config) (kafkaConsumerGroup, error) {
			return sarama.NewConsumerGroup(brokers, group, cfg)
		},
	}
	if err := r.valid(); err != nil {
		log.Logger.Panic("new kafka recv", zap.Error(err))
	}
	log.Logger.Info("new kafka recv", zap.Strings("topics", cfg.Topics),
		zap.Strings("brokers", cfg.Brokers), zap.Int("nconsumer", cfg.NConsumer),
		zap.Int("interval_num", cfg.IntervalNum), zap.Duration("interval_sec", cfg.IntervalDuration))
	return r
}

func (r *KafkaRecv) valid() error {
	if !r.IsJSONFormat && r.MsgKey == "" {
		r.MsgKey = "log"
	}
	if r.ReconnectInterval <= 0 {
		r.ReconnectInterval = defaultKafkaReconnectInterval
	}
	if r.NConsumer <= 0 {
		r.NConsumer = 1
	}
	if r.IntervalNum <= 0 {
		r.IntervalNum = 1000
	}
	if r.IntervalDuration <= 0 {
		r.IntervalDuration = 3 * time.Second
	}
	return nil
}

func (r *KafkaRecv) GetName() string { return r.Name }

func (r *KafkaRecv) consumerConfig() *sarama.Config {
	cfg := sarama.NewConfig()
	cfg.Net.KeepAlive = 30 * time.Second
	cfg.Consumer.Return.Errors = true
	// Keep the legacy new-group policy: start at the end when no committed
	// offset exists. Never silently replay a whole topic during an upgrade.
	cfg.Consumer.Offsets.Initial = sarama.OffsetNewest
	cfg.Consumer.Offsets.AutoCommit.Enable = true
	cfg.Consumer.Offsets.AutoCommit.Interval = r.IntervalDuration
	// Eager range assignment cancels the session on revocation, including
	// claims currently blocked by downstream backpressure.
	cfg.Consumer.Group.Rebalance.GroupStrategies = []sarama.BalanceStrategy{sarama.NewBalanceStrategyRange()}
	return cfg
}

func (r *KafkaRecv) Run(ctx context.Context) {
	for i := 0; i < r.NConsumer; i++ {
		go r.runConsumer(ctx)
	}
}

func kafkaRetry(ctx context.Context) bool {
	timer := time.NewTimer(kafkaRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *KafkaRecv) runConsumer(ctx context.Context) {
	for ctx.Err() == nil {
		cycleCtx, cancel := context.WithTimeout(ctx, r.ReconnectInterval)
		group, err := r.newConsumerGroup(r.Brokers, r.Group, r.consumerConfig())
		if err == nil {
			r.consumeGroup(cycleCtx, group)
		} else {
			log.Logger.Error("connect Kafka consumer", zap.Error(err))
		}
		cancel()
		if !kafkaRetry(ctx) {
			return
		}
	}
}

func (r *KafkaRecv) consumeGroup(ctx context.Context, group kafkaConsumerGroup) {
	// Keep draining errors until Close finishes: stopping the drain first can
	// deadlock a client that reports its final errors while shutting down.
	stopErrors, errorsDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(errorsDone)
		for {
			select {
			case err, ok := <-group.Errors():
				if !ok {
					return
				}
				log.Logger.Warn("Kafka consumer error", zap.Error(err))
			case <-stopErrors:
				return
			}
		}
	}()
	defer func() {
		if err := group.Close(); err != nil {
			log.Logger.Warn("close Kafka consumer", zap.Error(err))
		}
		close(stopErrors)
		<-errorsDone
	}()
	handler := &kafkaGroupHandler{recv: r, dry: utils.Settings.GetBool("dry")}
	for ctx.Err() == nil {
		// Consume returns after an eager rebalance. Re-enter it on the same
		// client; client recreation is reserved for the configured lifetime.
		if err := group.Consume(ctx, r.Topics, handler); err != nil && ctx.Err() == nil {
			log.Logger.Warn("consume Kafka group", zap.Error(err))
			if !kafkaRetry(ctx) {
				return
			}
		}
	}
}

type kafkaGroupHandler struct {
	recv *KafkaRecv
	dry  bool
}

func (*kafkaGroupHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (*kafkaGroupHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *kafkaGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	count := 0 // claim-local: never mix offsets from different partitions
	ctx := session.Context()
	for {
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case record, ok := <-claim.Messages():
			if !ok {
				return nil
			}
			if !h.recv.forwardKafkaRecord(ctx, record) {
				return nil
			}
			if !h.dry {
				session.MarkMessage(record, "")
				count++
				if count >= h.recv.IntervalNum {
					session.Commit()
					count = 0
				}
			}
		}
	}
}

// forwardKafkaRecord preserves the existing admission boundary: offsets become
// eligible only after handoff to the acceptor, not after end-to-end delivery.
// Malformed records retain the existing discard-and-advance policy.
func (r *KafkaRecv) forwardKafkaRecord(ctx context.Context, record *sarama.ConsumerMessage) bool {
	if ctx.Err() != nil {
		return false
	}
	msg, err := r.parse2Msg(record)
	if err != nil {
		log.Logger.Error("parse Kafka message", zap.String("name", r.Name), zap.Error(err))
		return true
	}
	select {
	case <-ctx.Done():
		r.msgPool.Put(msg)
		return false
	case r.syncOutChan <- msg:
		return true
	}
}

// parse2Msg converts a Kafka record while retaining original-tag metadata.
func (r *KafkaRecv) parse2Msg(kmsg *sarama.ConsumerMessage) (msg *library.FluentMsg, err error) {
	msg = r.newMsg()
	msg.ID = r.counter.Count()
	msg.Tag = r.Tag
	msg.Message = map[string]interface{}{}
	if r.IsJSONFormat {
		if err = json.Unmarshal(kmsg.Value, &msg.Message); err != nil {
			r.msgPool.Put(msg)
			return nil, errors.Wrap(err, "unmarshal Kafka message")
		}
		if msg.Message == nil {
			r.msgPool.Put(msg)
			return nil, errors.New("Kafka JSON must be an object")
		}
		if r.JSONTagKey != "" {
			switch tag := msg.Message[r.JSONTagKey].(type) {
			case []byte:
				msg.Tag = string(tag)
			case string:
				msg.Tag = tag
			default:
				r.msgPool.Put(msg)
				return nil, errors.New("unknown JSONTagKey format")
			}
		}
	} else {
		msg.Message[r.MsgKey] = kmsg.Value
	}
	if r.TagKey != "" {
		msg.Message[r.TagKey] = msg.Tag
	}
	if r.RewriteTag != "" {
		msg.Tag = r.RewriteTag
	}
	library.ProcessAdd(r.AddCfg, msg)
	return msg, nil
}
