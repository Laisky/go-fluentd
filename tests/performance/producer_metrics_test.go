package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gofluentd/internal/controller"
	"gofluentd/internal/monitor"
	"gofluentd/internal/senders"
	"gofluentd/library"
)

type heldSender struct {
	senders.BaseSender
	name   string
	in     chan *library.FluentMsg
	result chan<- *library.FluentMsg
	failed chan<- *library.FluentMsg
}

func (s *heldSender) GetName() string                                 { return s.name }
func (s *heldSender) Spawn(context.Context) chan<- *library.FluentMsg { return s.in }
func (s *heldSender) SetSuccessedChan(out chan<- *library.FluentMsg)  { s.result = out }
func (s *heldSender) SetFailedChan(out chan<- *library.FluentMsg)     { s.failed = out }

func heldProducer(tb testing.TB, n int) (*gin.Engine, []*library.FluentMsg, *heldSender, chan *library.FluentMsg) {
	tb.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)
	first := &heldSender{name: "ready", in: make(chan *library.FluentMsg, 1)}
	second := &heldSender{name: "pending", in: make(chan *library.FluentMsg, 1)}
	first.SetSupportedTags([]string{"logs"})
	second.SetSupportedTags([]string{"logs"})
	in, commit := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, n+1)
	p, err := controller.NewProducer(&controller.ProducerCfg{NFork: 1, InChan: in, CommitChan: commit, DiscardChanSize: n + 1, MsgPool: &sync.Pool{}}, first, second)
	if err != nil {
		tb.Fatal(err)
	}
	p.Run(ctx)
	messages := make([]*library.FluentMsg, n)
	for i := range messages {
		m := message(int64(i))
		messages[i] = m
		in <- m
		if <-first.in != m || <-second.in != m {
			tb.Fatal("fanout mismatch")
		}
		first.result <- m
	}
	engine := gin.New()
	monitor.BindHTTP(engine)
	deadline := time.Now().Add(10 * time.Second)
	for pendingGauge(tb, engine) != int64(n) {
		if time.Now().After(deadline) {
			tb.Fatal("pending gauge did not settle")
		}
		time.Sleep(time.Millisecond)
	}
	return engine, messages, second, commit
}
func pendingGauge(tb testing.TB, e *gin.Engine) int64 {
	tb.Helper()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		tb.Fatal(err)
	}
	var producer struct {
		Pending int64 `json:"waitToDiscardMsgNum"`
	}
	if err := json.Unmarshal(snapshot["producer"], &producer); err != nil {
		tb.Fatal(err)
	}
	return producer.Pending
}
func TestPerformanceContractPendingGaugeTracksFanout(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			e, msgs, second, commit := heldProducer(t, 128)
			if len(commit) != 0 {
				t.Fatal("incomplete fanout was committed")
			}
			for _, m := range msgs {
				if failed {
					second.failed <- m
				} else {
					second.result <- m
				}
			}
			deadline := time.Now().Add(time.Second)
			for pendingGauge(t, e) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("terminal fanout still counted as pending")
				}
				time.Sleep(time.Millisecond)
			}
			if failed {
				if len(commit) != 0 {
					t.Fatal("failed fanout was committed")
				}
			} else {
				for range msgs {
					select {
					case <-commit:
					case <-time.After(time.Second):
						t.Fatal("fanout did not finish")
					}
				}
			}
		})
	}
}
func BenchmarkPerfPendingMonitor(b *testing.B) {
	for _, n := range []int{0, 1000, 100000} {
		b.Run(fmt.Sprintf("pending=%d", n), func(b *testing.B) {
			e, _, _, _ := heldProducer(b, n)
			req := httptest.NewRequest("GET", "/monitor", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				e.ServeHTTP(w, req)
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
			b.ReportMetric(1, "scrapes/op")
			b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "scrapes/s")
		})
	}
}
