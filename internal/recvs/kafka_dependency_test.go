package recvs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
)

type upgradeKafkaSession struct {
	sarama.ConsumerGroupSession
	ctx     context.Context
	marked  []*sarama.ConsumerMessage
	commits int
}

func (s *upgradeKafkaSession) Context() context.Context { return s.ctx }
func (s *upgradeKafkaSession) MarkMessage(m *sarama.ConsumerMessage, _ string) {
	s.marked = append(s.marked, m)
}
func (s *upgradeKafkaSession) Commit() { s.commits++ }

type upgradeKafkaClaim struct {
	sarama.ConsumerGroupClaim
	messages chan *sarama.ConsumerMessage
}

func (c *upgradeKafkaClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }
func upgradeKafkaRecv() *KafkaRecv {
	r := NewKafkaRecv(&KafkaCfg{Tag: "logs", Topics: []string{"logs"}, Brokers: []string{"unused"}, Group: "test", KafkaCommitCfg: KafkaCommitCfg{IntervalNum: 2}})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(recvPool())
	return r
}
func TestRegressionKafkaUpgradeAdmissionAndOffsets(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			r := upgradeKafkaRecv()
			r.IsJSONFormat = true
			out := make(chan *library.FluentMsg, 3)
			r.SetSyncOutChan(out)
			s := &upgradeKafkaSession{ctx: context.Background()}
			claim := &upgradeKafkaClaim{messages: make(chan *sarama.ConsumerMessage, 4)}
			for i, body := range []string{`{"n":1}`, `null`, `{"n":3}`} {
				claim.messages <- &sarama.ConsumerMessage{Topic: "logs", Partition: 7, Offset: int64(100 + i), Value: []byte(body)}
			}
			close(claim.messages)
			h := &kafkaGroupHandler{recv: r, dry: dry}
			if err := h.Setup(s); err != nil {
				t.Fatal(err)
			}
			if err := h.ConsumeClaim(s, claim); err != nil {
				t.Fatal(err)
			}
			if err := h.Cleanup(s); err != nil {
				t.Fatal(err)
			}
			if len(out) != 2 {
				t.Fatalf("delivered %d records, want 2 valid JSON objects", len(out))
			}
			wantMarks, wantCommits := 3, 1
			if dry {
				wantMarks, wantCommits = 0, 0
			}
			if len(s.marked) != wantMarks || s.commits != wantCommits {
				t.Fatalf("marks=%d commits=%d, want %d/%d", len(s.marked), s.commits, wantMarks, wantCommits)
			}
			for i, m := range s.marked {
				if m.Offset != int64(100+i) || m.Partition != 7 || m.Topic != "logs" {
					t.Fatalf("offset ownership changed: %+v", m)
				}
			}
		})
	}
}

type upgradeKafkaCounter struct {
	reached chan struct{}
	once    sync.Once
}

func (c *upgradeKafkaCounter) Count() int64         { c.once.Do(func() { close(c.reached) }); return 1 }
func (c *upgradeKafkaCounter) CountN(n int64) int64 { return c.Count() }
func TestRegressionKafkaUpgradeBlockedClaimCancellation(t *testing.T) {
	r := upgradeKafkaRecv()
	reached := make(chan struct{})
	r.SetCounter(&upgradeKafkaCounter{reached: reached})
	r.SetSyncOutChan(make(chan *library.FluentMsg))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &upgradeKafkaSession{ctx: ctx}
	claim := &upgradeKafkaClaim{messages: make(chan *sarama.ConsumerMessage, 1)}
	claim.messages <- &sarama.ConsumerMessage{Value: []byte("blocked"), Offset: 42}
	done := make(chan error, 1)
	go func() { done <- (&kafkaGroupHandler{recv: r}).ConsumeClaim(s, claim) }()
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("claim never reached delivery")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("rebalance/cancellation stuck behind downstream backpressure")
	}
	if len(s.marked) != 0 || s.commits != 0 {
		t.Fatal("undelivered record was marked/committed")
	}
}

type upgradeKafkaGroup struct {
	errs    chan error
	consume func(context.Context) error
	closed  int
}

func (g *upgradeKafkaGroup) Errors() <-chan error { return g.errs }
func (g *upgradeKafkaGroup) Consume(ctx context.Context, _ []string, _ sarama.ConsumerGroupHandler) error {
	return g.consume(ctx)
}
func (g *upgradeKafkaGroup) Close() error {
	g.closed++
	g.errs <- errors.New("final close diagnostic")
	close(g.errs)
	return nil
}
func TestRegressionKafkaUpgradeConsumerLifecycle(t *testing.T) {
	r := upgradeKafkaRecv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	group := &upgradeKafkaGroup{errs: make(chan error)}
	group.consume = func(context.Context) error {
		calls++
		switch calls {
		case 1:
			return nil
		case 2:
			return errors.New("retryable broker error")
		default:
			cancel()
			return nil
		}
	}
	done := make(chan struct{})
	go func() { r.consumeGroup(ctx, group); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer failed to rejoin/retry/close")
	}
	if calls != 3 || group.closed != 1 {
		t.Fatalf("consume=%d close=%d", calls, group.closed)
	}
}
func TestRegressionKafkaUpgradeConnectRetryAndDefaults(t *testing.T) {
	r := upgradeKafkaRecv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	group := &upgradeKafkaGroup{errs: make(chan error), consume: func(context.Context) error { cancel(); return nil }}
	r.newConsumerGroup = func(b []string, g string, cfg *sarama.Config) (kafkaConsumerGroup, error) {
		calls++
		if len(b) != 1 || b[0] != "unused" || g != "test" {
			t.Error("connection configuration changed")
		}
		if cfg.Consumer.Offsets.Initial != sarama.OffsetNewest || !cfg.Consumer.Return.Errors || cfg.Consumer.Offsets.AutoCommit.Interval != 3*time.Second {
			t.Error("offset/error/commit defaults changed")
		}
		if err := cfg.Validate(); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			return nil, errors.New("unavailable")
		}
		return group, nil
	}
	r.runConsumer(ctx)
	if calls != 2 || group.closed != 1 {
		t.Fatalf("connect=%d close=%d", calls, group.closed)
	}
	r.runConsumer(ctx)
	if calls != 2 {
		t.Fatal("canceled worker connected again")
	}
}
func TestRegressionKafkaUpgradeConcurrentPartitionOwnership(t *testing.T) {
	r := upgradeKafkaRecv()
	out := make(chan *library.FluentMsg, 64)
	r.SetSyncOutChan(out)
	handler := &kafkaGroupHandler{recv: r}
	var wg sync.WaitGroup
	sessions := make([]*upgradeKafkaSession, 4)
	for part := range sessions {
		s := &upgradeKafkaSession{ctx: context.Background()}
		sessions[part] = s
		claim := &upgradeKafkaClaim{messages: make(chan *sarama.ConsumerMessage, 16)}
		for i := 0; i < 16; i++ {
			claim.messages <- &sarama.ConsumerMessage{Topic: "logs", Partition: int32(part), Offset: int64(i), Value: []byte(fmt.Sprintf("%d/%d", part, i))}
		}
		close(claim.messages)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handler.ConsumeClaim(s, claim); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(out)
	ids := map[int64]bool{}
	payloads := map[string]bool{}
	for m := range out {
		if ids[m.ID] {
			t.Fatal("duplicate internal ID")
		}
		ids[m.ID] = true
		payloads[string(m.Message["log"].([]byte))] = true
	}
	if len(ids) != 64 || len(payloads) != 64 {
		t.Fatalf("lost deliveries: ids=%d bodies=%d", len(ids), len(payloads))
	}
	for part, s := range sessions {
		if len(s.marked) != 16 || s.commits != 8 {
			t.Fatal("lost marks or wrong count-based commits")
		}
		for i, m := range s.marked {
			if m.Partition != int32(part) || m.Offset != int64(i) {
				t.Fatal("cross-partition offset mixup")
			}
		}
	}
}
