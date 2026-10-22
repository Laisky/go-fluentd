package recvs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"gofluentd/internal/senders"
	"gofluentd/library"
)

// durableBrokerConsumer owns one actual Sarama group client and its synthetic receipt boundary.
type durableBrokerConsumer struct {
	receiver *KafkaRecv
	out      chan *library.FluentMsg
	ready    chan int32
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// startDurableBrokerConsumer starts the real production consumer adapter in strict mode.
func startDurableBrokerConsumer(t *testing.T, parent context.Context, brokers []string, topic, groupID string) *durableBrokerConsumer {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	c := &durableBrokerConsumer{out: make(chan *library.FluentMsg, 4), ready: make(chan int32, 32), cancel: cancel, done: make(chan struct{})}
	r := upgradeKafkaRecv()
	r.Brokers, r.Topics, r.Group = brokers, []string{topic}, groupID
	r.IsJSONFormat = true
	r.IntervalNum = 1
	r.IntervalDuration = 25 * time.Millisecond
	r.SetDurableAdmission(true)
	r.SetSyncOutChan(c.out)
	c.receiver = r
	r.newConsumerGroup = func(b []string, g string, cfg *sarama.Config) (kafkaConsumerGroup, error) {
		// Bound network waits while retaining the real eager group ownership/offset implementation.
		cfg.Net.DialTimeout = 3 * time.Second
		cfg.Net.ReadTimeout = 5 * time.Second
		cfg.Net.WriteTimeout = 5 * time.Second
		client, err := sarama.NewConsumerGroup(b, g, cfg)
		if err != nil {
			return nil, err
		}
		return &brokerUpgradeGroup{ConsumerGroup: client, ready: c.ready, closed: make(chan struct{})}, nil
	}
	go func() { defer close(c.done); r.runConsumer(ctx) }()
	t.Cleanup(func() { c.stop(t) })
	return c
}

// stop cancels ownership and joins the actual consumer before any late receipt is supplied.
func (c *durableBrokerConsumer) stop(t *testing.T) {
	t.Helper()
	c.stopOnce.Do(func() {
		c.cancel()
		select {
		case <-c.done:
		case <-time.After(15 * time.Second):
			t.Error("real durable Kafka consumer did not stop")
		}
	})
}

// waitReady waits until the broker has assigned a resolved partition start position.
func (c *durableBrokerConsumer) waitReady(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-c.ready:
	case <-ctx.Done():
		t.Fatal("real durable Kafka claim readiness deadline")
	}
}

// takeDurableBrokerRecord reads from the current owner, including either owner after rebalance.
func takeDurableBrokerRecord(t *testing.T, ctx context.Context, consumers ...*durableBrokerConsumer) *library.FluentMsg {
	t.Helper()
	if len(consumers) == 1 {
		select {
		case m := <-consumers[0].out:
			return m
		case <-ctx.Done():
			t.Fatal("real durable Kafka record deadline")
		}
	}
	if len(consumers) == 2 {
		select {
		case m := <-consumers[0].out:
			return m
		case m := <-consumers[1].out:
			return m
		case <-ctx.Done():
			t.Fatal("real durable Kafka rebalance record deadline")
		}
	}
	t.Fatal("invalid real broker fixture receiver count")
	return nil
}

// durableBrokerPosition reads the actual broker's next committed offset, with -1 meaning unset.
func durableBrokerPosition(t *testing.T, admin sarama.ClusterAdmin, groupID, topic string) int64 {
	t.Helper()
	offsets, err := admin.ListConsumerGroupOffsets(groupID, map[string][]int32{topic: {0}})
	durableCheck.NoError(t, err)
	block := offsets.GetBlock(topic, 0)
	if block == nil {
		return -1
	}
	if block.Err != sarama.ErrNoError {
		t.Fatalf("broker offset fetch failed: %v", block.Err)
	}
	return block.Offset
}

// holdDurableBrokerFrontier proves no periodic auto-commit can advance a blocked record.
func holdDurableBrokerFrontier(t *testing.T, ctx context.Context, admin sarama.ClusterAdmin, groupID, topic string, maximum int64, consumers ...*durableBrokerConsumer) {
	t.Helper()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if got := durableBrokerPosition(t, admin, groupID, topic); got > maximum {
			t.Fatalf("broker advanced blocked frontier to %d, maximum %d", got, maximum)
		}
		for _, c := range consumers {
			select {
			case <-c.out:
				t.Fatal("later broker record overtook blocked admission")
			default:
			}
		}
		select {
		case <-timer.C:
			return
		case <-ctx.Done():
			t.Fatal("broker frontier hold deadline")
		case <-tick.C:
		}
	}
}

// waitDurableBrokerPosition waits for the exact accepted-record frontier.
func waitDurableBrokerPosition(t *testing.T, ctx context.Context, admin sarama.ClusterAdmin, groupID, topic string, want int64) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		got := durableBrokerPosition(t, admin, groupID, topic)
		if got == want {
			return
		}
		if got > want {
			t.Fatalf("broker committed past expected frontier %d to %d", want, got)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("broker frontier %d did not reach %d", got, want)
		case <-tick.C:
		}
	}
}

// checkDurableBrokerRecord verifies exact source positions and original synthetic broker bytes.
func checkDurableBrokerRecord(t *testing.T, msg *library.FluentMsg, topic string, offset int64, id int) {
	t.Helper()
	durableCheck.NotNil(t, msg.DurableAck)
	durableCheck.Equal(t, topic, msg.SourceMetadata["topic"])
	durableCheck.Equal(t, int32(0), msg.SourceMetadata["partition"])
	durableCheck.Equal(t, offset, msg.SourceMetadata["offset"])
	durableCheck.Equal(t, []byte(fmt.Sprintf("{\"id\":\"synthetic-%d\"}", id)), msg.SourceMetadata["value"])
}

// TestDurableKafkaRealBroker tests actual broker commits, replay and rebalance.
// Only a disposable unauthenticated broker is supplied by the opt-in test environment.
// Journal receipts are controlled to isolate the broker ACK boundary; no sink delivery is claimed.
func TestDurableKafkaRealBroker(t *testing.T) {
	address := os.Getenv("FLUENTD_KAFKA_TEST_BROKERS")
	if address == "" {
		t.Skip("set FLUENTD_KAFKA_TEST_BROKERS to run durable real-broker contract")
	}
	brokers := strings.Split(address, ",")
	cfg := sarama.NewConfig()
	cfg.Net.DialTimeout = 3 * time.Second
	cfg.Net.ReadTimeout = 5 * time.Second
	cfg.Net.WriteTimeout = 5 * time.Second
	admin, err := sarama.NewClusterAdmin(brokers, cfg)
	durableCheck.NoError(t, err)
	defer admin.Close()
	producer, err := senders.NewKafkaProducer(brokers)
	durableCheck.NoError(t, err)
	defer producer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	for _, scenario := range []string{"receipts-gap-restart", "first-unaccepted-restart", "rebalance-late-owner"} {
		t.Run(scenario, func(t *testing.T) {
			topic := fmt.Sprintf("fluentd-durable-%d", time.Now().UnixNano())
			groupID := topic + "-group"
			durableCheck.NoError(t, admin.CreateTopic(topic, &sarama.TopicDetail{NumPartitions: 1, ReplicationFactor: 1}, false))
			defer func() {
				if err := admin.DeleteTopic(topic); err != nil {
					t.Errorf("delete synthetic topic: %v", err)
				}
			}()
			send := func(n int) []int64 {
				messages := make([]*sarama.ProducerMessage, n)
				for i := range messages {
					messages[i] = &sarama.ProducerMessage{Topic: topic, Key: sarama.StringEncoder("synthetic-key"), Value: sarama.StringEncoder(fmt.Sprintf("{\"id\":\"synthetic-%d\"}", i))}
				}
				durableCheck.NoError(t, producer.SendMessages(messages))
				positions := make([]int64, n)
				for i, m := range messages {
					durableCheck.Equal(t, int32(0), m.Partition)
					positions[i] = m.Offset
				}
				return positions
			}
			if scenario == "first-unaccepted-restart" {
				// Nothing is committed or seeded, and the fresh strict group must replay retained input.
				positions := send(2)
				first := startDurableBrokerConsumer(t, ctx, brokers, topic, groupID)
				first.waitReady(t, ctx)
				blocked := takeDurableBrokerRecord(t, ctx, first)
				checkDurableBrokerRecord(t, blocked, topic, positions[0], 0)
				holdDurableBrokerFrontier(t, ctx, admin, groupID, topic, -1, first)
				first.stop(t)
				blocked.CompleteAcceptance(nil)
				durableCheck.Equal(t, int64(-1), durableBrokerPosition(t, admin, groupID, topic))
				restarted := startDurableBrokerConsumer(t, ctx, brokers, topic, groupID)
				restarted.waitReady(t, ctx)
				replay := takeDurableBrokerRecord(t, ctx, restarted)
				checkDurableBrokerRecord(t, replay, topic, positions[0], 0)
				replay.CompleteAcceptance(nil)
				second := takeDurableBrokerRecord(t, ctx, restarted)
				checkDurableBrokerRecord(t, second, topic, positions[1], 1)
				second.CompleteAcceptance(nil)
				waitDurableBrokerPosition(t, ctx, admin, groupID, topic, positions[1]+1)
				restarted.stop(t)
				return
			}
			first := startDurableBrokerConsumer(t, ctx, brokers, topic, groupID)
			first.waitReady(t, ctx)
			n := 3
			if scenario == "rebalance-late-owner" {
				n = 2
			}
			positions := send(n)
			blocked := takeDurableBrokerRecord(t, ctx, first)
			checkDurableBrokerRecord(t, blocked, topic, positions[0], 0)
			holdDurableBrokerFrontier(t, ctx, admin, groupID, topic, -1, first)
			if scenario == "rebalance-late-owner" {
				secondOwner := startDurableBrokerConsumer(t, ctx, brokers, topic, groupID)
				// Eager rebalance cancels the old blocked claim; either member may own the new generation.
				replay := takeDurableBrokerRecord(t, ctx, first, secondOwner)
				checkDurableBrokerRecord(t, replay, topic, positions[0], 0)
				blocked.CompleteAcceptance(nil)
				holdDurableBrokerFrontier(t, ctx, admin, groupID, topic, -1, first, secondOwner)
				replay.CompleteAcceptance(nil)
				second := takeDurableBrokerRecord(t, ctx, first, secondOwner)
				checkDurableBrokerRecord(t, second, topic, positions[1], 1)
				second.CompleteAcceptance(nil)
				waitDurableBrokerPosition(t, ctx, admin, groupID, topic, positions[1]+1)
				first.stop(t)
				secondOwner.stop(t)
				return
			}
			// A refusal retries the exact current broker position, with no later delivery or commit.
			blocked.CompleteAcceptance(errors.New("synthetic aggregate budget full"))
			retry := takeDurableBrokerRecord(t, ctx, first)
			checkDurableBrokerRecord(t, retry, topic, positions[0], 0)
			holdDurableBrokerFrontier(t, ctx, admin, groupID, topic, -1, first)
			retry.CompleteAcceptance(nil)
			next := takeDurableBrokerRecord(t, ctx, first)
			checkDurableBrokerRecord(t, next, topic, positions[1], 1)
			waitDurableBrokerPosition(t, ctx, admin, groupID, topic, positions[0]+1)
			holdDurableBrokerFrontier(t, ctx, admin, groupID, topic, positions[0]+1, first)
			first.stop(t)
			next.CompleteAcceptance(nil)
			durableCheck.Equal(t, positions[0]+1, durableBrokerPosition(t, admin, groupID, topic))
			restarted := startDurableBrokerConsumer(t, ctx, brokers, topic, groupID)
			restarted.waitReady(t, ctx)
			replay := takeDurableBrokerRecord(t, ctx, restarted)
			checkDurableBrokerRecord(t, replay, topic, positions[1], 1)
			replay.CompleteAcceptance(nil)
			final := takeDurableBrokerRecord(t, ctx, restarted)
			checkDurableBrokerRecord(t, final, topic, positions[2], 2)
			final.CompleteAcceptance(nil)
			waitDurableBrokerPosition(t, ctx, admin, groupID, topic, positions[2]+1)
			restarted.stop(t)
		})
	}
}

// TestDurableKafkaInitialPositionPolicy preserves legacy new-group semantics while strict mode replays.
func TestDurableKafkaInitialPositionPolicy(t *testing.T) {
	r := upgradeKafkaRecv()
	durableCheck.Equal(t, sarama.OffsetNewest, r.consumerConfig().Consumer.Offsets.Initial)
	r.SetDurableAdmission(true)
	durableCheck.Equal(t, sarama.OffsetOldest, r.consumerConfig().Consumer.Offsets.Initial)
}
