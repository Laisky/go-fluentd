package senders

import (
	"bytes"
	"compress/gzip"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"github.com/Shopify/sarama"
	"gofluentd/library"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type componentTransport func(*http.Request) (*http.Response, error)

func (f componentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func senderTake(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("sender result timed out")
		return nil
	}
}
func senderChannels(s SenderItf) (chan *library.FluentMsg, chan *library.FluentMsg) {
	good, bad := make(chan *library.FluentMsg, 16), make(chan *library.FluentMsg, 16)
	s.SetSuccessedChan(good)
	s.SetFailedChan(bad)
	return good, bad
}
func componentHTTPSender(t *testing.T, kind string, transport http.RoundTripper, wait time.Duration) (SenderItf, chan *library.FluentMsg, chan *library.FluentMsg) {
	t.Helper()
	var s SenderItf
	if kind == "http" {
		v := NewHTTPSender(&HTTPSenderCfg{Name: "http", Addr: "http://example.invalid", Tags: []string{"logs"}, NFork: 1, InChanSize: 8, BatchSize: 3, MaxWait: wait})
		v.httpClient = &http.Client{Transport: transport}
		s = v
	} else {
		v := NewElasticSearchSender(&ElasticSearchSenderCfg{Name: "es", Addr: "http://example.invalid", Tags: []string{"logs"}, TagIndexMap: map[string]string{"logs": "logs"}, NFork: 1, InChanSize: 8, BatchSize: 3, MaxWait: wait})
		v.httpClient = &http.Client{Transport: transport}
		s = v
	}
	good, bad := senderChannels(s)
	return s, good, bad
}
func componentHTTPResponse(status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"errors":false}`))}
}
func TestComponentHTTPAndESClosedInputFlushesPending(t *testing.T) {
	for _, kind := range []string{"http", "es"} {
		t.Run(kind, func(t *testing.T) {
			transport := componentTransport(func(r *http.Request) (*http.Response, error) { return componentHTTPResponse(200), nil })
			s, good, bad := componentHTTPSender(t, kind, transport, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.Spawn(ctx)
			first := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{"n": 1}}
			in <- first
			if senderTake(t, good) != first {
				t.Fatal("normal first batch failed")
			}
			last := &library.FluentMsg{Tag: "logs", ID: 2, Message: map[string]interface{}{"n": 2}}
			in <- last
			close(in)
			if senderTake(t, good) != last || len(bad) != 0 {
				t.Fatal("pending batch dropped on input close")
			}
		})
	}
}
func TestComponentHTTPAndESRetriesTimeoutAndCompletePayload(t *testing.T) {
	for _, kind := range []string{"http", "es"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", kind, fail), func(t *testing.T) {
				var mu sync.Mutex
				calls := 0
				var payloads [][]byte
				transport := componentTransport(func(r *http.Request) (*http.Response, error) {
					reader, err := gzip.NewReader(r.Body)
					if err != nil {
						t.Error(err)
						return nil, err
					}
					body, err := io.ReadAll(reader)
					reader.Close()
					if err != nil {
						t.Error(err)
						return nil, err
					}
					mu.Lock()
					calls++
					n := calls
					payloads = append(payloads, append([]byte(nil), body...))
					mu.Unlock()
					if fail || n == 1 {
						return componentHTTPResponse(503), nil
					}
					return componentHTTPResponse(200), nil
				})
				s, good, bad := componentHTTPSender(t, kind, transport, 10*time.Millisecond)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				in := s.Spawn(ctx)
				m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"n": 1}}
				in <- m
				result := good
				if fail {
					result = bad
				}
				if senderTake(t, result) != m {
					t.Fatal("wrong result ownership")
				}
				mu.Lock()
				n := calls
				mu.Unlock()
				want := 2
				if fail {
					want = 4
				}
				if n != want {
					t.Fatalf("attempts=%d want=%d", n, want)
				}
				if !fail {
					in <- &library.FluentMsg{Tag: "logs", ID: 2, Message: map[string]interface{}{"n": 2}}
					if senderTake(t, good).ID != 2 {
						t.Fatal("timeout did not flush partial batch")
					}
				}
				close(in)
				if fail && len(good) != 0 {
					t.Fatal("failed batch also acknowledged")
				}
				if len(payloads) == 0 || !bytes.Contains(payloads[0], []byte(`"n":1`)) {
					t.Fatal("wrong compressed payload")
				}
			})
		}
	}
}
func TestComponentHTTPAndESRequestCancellation(t *testing.T) {
	for _, kind := range []string{"http", "es"} {
		t.Run(kind, func(t *testing.T) {
			entered, exited := make(chan struct{}), make(chan struct{})
			transport := componentTransport(func(req *http.Request) (*http.Response, error) {
				close(entered)
				<-req.Context().Done()
				close(exited)
				return nil, req.Context().Err()
			})
			s, good, bad := componentHTTPSender(t, kind, transport, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.Spawn(ctx)
			in <- &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("request never started")
			}
			cancel()
			select {
			case <-exited:
			case <-time.After(time.Second):
				t.Error("in-flight HTTP request does not inherit worker cancellation")
			}
			if len(good)+len(bad) != 0 {
				t.Fatal("canceled batch published a completed result")
			}
		})
	}
}

type componentKafkaProducer struct {
	mu      sync.Mutex
	calls   int
	records [][]byte
	err     error
	closed  chan struct{}
	once    sync.Once
}

func (p *componentKafkaProducer) SendMessage(m *sarama.ProducerMessage) (int32, int64, error) {
	return 0, 0, p.SendMessages([]*sarama.ProducerMessage{m})
}
func (p *componentKafkaProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, m := range msgs {
		if m.Value == nil {
			p.records = append(p.records, nil)
			continue
		}
		b, err := m.Value.Encode()
		if err != nil {
			return err
		}
		p.records = append(p.records, append([]byte(nil), b...))
	}
	return p.err
}
func (p *componentKafkaProducer) Close() error { p.once.Do(func() { close(p.closed) }); return nil }
func newComponentKafka(p *componentKafkaProducer) *KafkaSender {
	s := NewKafkaSender(&KafkaSenderCfg{Name: "kafka", Brokers: []string{"unused"}, Topic: "logs", Tags: []string{"logs"}, NFork: 1, BatchSize: 1, InChanSize: 8, MaxWait: time.Hour})
	s.newProducer = func([]string) (sarama.SyncProducer, error) { return p, nil }
	return s
}
func TestComponentKafkaInvalidRecordNeverReusesPreviousPayload(t *testing.T) {
	p := &componentKafkaProducer{closed: make(chan struct{})}
	s := newComponentKafka(p)
	good, bad := senderChannels(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := s.Spawn(ctx)
	first := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{"value": "first"}}
	in <- first
	if senderTake(t, good) != first {
		t.Fatal("successful record failed")
	}
	invalid := &library.FluentMsg{Tag: "logs", ID: 2, Message: map[string]interface{}{"bad": make(chan int)}}
	in <- invalid
	select {
	case m := <-bad:
		if m != invalid {
			t.Fatal("wrong failure")
		}
	case <-good:
		t.Error("unencodable message acknowledged using stale payload")
	case <-time.After(time.Second):
		t.Error("unencodable record never reported")
	}
	close(in)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != 1 {
		t.Errorf("serialization failure reached Kafka: %d calls", p.calls)
	}
	var decoded map[string]interface{}
	if err := stdjson.Unmarshal(p.records[0], &decoded); err != nil || decoded["value"] != "first" {
		t.Fatal("valid payload changed")
	}
}
func TestComponentKafkaClosesProducerOnExit(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			p := &componentKafkaProducer{closed: make(chan struct{})}
			s := newComponentKafka(p)
			good, _ := senderChannels(s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			in := s.Spawn(ctx)
			in <- &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
			senderTake(t, good)
			if cancelled {
				cancel()
			} else {
				close(in)
			}
			select {
			case <-p.closed:
			case <-time.After(time.Second):
				t.Fatal("Kafka producer leaked on worker exit")
			}
		})
	}
}
func TestComponentKafkaRetryFailureAndRecovery(t *testing.T) {
	p := &componentKafkaProducer{closed: make(chan struct{}), err: errors.New("broker rejected")}
	s := newComponentKafka(p)
	healthy := &componentKafkaProducer{closed: make(chan struct{})}
	connections := 0
	s.newProducer = func([]string) (sarama.SyncProducer, error) {
		connections++
		if connections == 1 {
			return p, nil
		}
		return healthy, nil
	}
	good, bad := senderChannels(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := s.Spawn(ctx)
	m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{}}
	in <- m
	if senderTake(t, bad) != m || len(good) != 0 {
		t.Fatal("failure result wrong")
	}
	p.mu.Lock()
	n := p.calls
	p.mu.Unlock()
	if n != 4 {
		t.Fatalf("attempts=%d", n)
	}
	select {
	case <-p.closed:
	case <-time.After(time.Second):
		t.Fatal("failed producer not closed")
	}
	// The next record must use a fresh producer and only acknowledge its own payload.
	recovered := &library.FluentMsg{Tag: "logs", ID: 2, Message: map[string]interface{}{"value": "recovered"}}
	in <- recovered
	if senderTake(t, good) != recovered {
		t.Fatal("fresh connection failed to recover")
	}
	close(in)
	select {
	case <-healthy.closed:
	case <-time.After(time.Second):
		t.Fatal("recovered producer leaked")
	}
	healthy.mu.Lock()
	defer healthy.mu.Unlock()
	if healthy.calls != 1 || len(healthy.records) != 1 {
		t.Fatal("recovery replayed a stale batch")
	}
	var decoded map[string]interface{}
	if err := stdjson.Unmarshal(healthy.records[0], &decoded); err != nil || decoded["value"] != "recovered" {
		t.Fatal("recovery payload was corrupted")
	}
}
func TestComponentStdoutCommitAndFailureContracts(t *testing.T) {
	for _, commit := range []bool{false, true} {
		for _, level := range []string{"info", "debug", "silent"} {
			t.Run(fmt.Sprintf("%v/%s", commit, level), func(t *testing.T) {
				s := NewStdoutSender(&StdoutSenderCfg{Name: "stdout", Tags: []string{"logs"}, IsCommit: commit, LogLevel: level, InChanSize: 4})
				good, bad := senderChannels(s)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				in := s.Spawn(ctx)
				m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"message": "test"}}
				in <- m
				out := bad
				if commit {
					out = good
				}
				if senderTake(t, out) != m || s.Get() != 1 || s.GetName() != "stdout" || !s.IsTagSupported("logs") || s.IsTagSupported("other") {
					t.Fatal("stdout terminal result/count failed")
				}
				close(in)
			})
		}
	}
}
