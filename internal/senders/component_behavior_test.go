package senders

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"gofluentd/library"
)

func behaviorSenderResult(t *testing.T, c <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-c:
		return m
	case <-time.After(time.Second):
		t.Fatal("sender did not report a terminal result")
		return nil
	}
}
func TestBehaviorHTTPAndESDrainPartialBatchOnClose(t *testing.T) {
	for _, es := range []bool{false, true} {
		t.Run(fmt.Sprint(es), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ok, bad := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gz, err := gzip.NewReader(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				b, err := io.ReadAll(gz)
				gz.Close()
				if err != nil {
					t.Error(err)
				}
				if es {
					if !strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), `"_index":"logs"`) {
						t.Errorf("bad bulk envelope: %s", b)
					}
				} else {
					var items []map[string]interface{}
					if json.Unmarshal(b, &items) != nil || len(items) != 1 {
						t.Errorf("bad HTTP batch: %s", b)
					}
				}
				count.Add(1)
				fmt.Fprint(w, `{"errors":false}`)
			}))
			defer srv.Close()
			var s SenderItf
			if es {
				s = NewElasticSearchSender(&ElasticSearchSenderCfg{Name: "es-behavior", Addr: srv.URL, NFork: 1, BatchSize: 4, InChanSize: 8, MaxWait: time.Hour, Tags: []string{"logs"}, TagIndexMap: map[string]string{"logs": "logs"}})
			} else {
				s = NewHTTPSender(&HTTPSenderCfg{Name: "http-behavior", Addr: srv.URL, NFork: 1, BatchSize: 4, InChanSize: 8, MaxWait: time.Hour})
			}
			s.SetSuccessedChan(ok)
			s.SetFailedChan(bad)
			in := s.Spawn(ctx)
			first := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{"value": "first"}}
			in <- first
			if behaviorSenderResult(t, ok) != first {
				t.Fatal("first record")
			}
			tail := &library.FluentMsg{Tag: "logs", ID: 2, Message: map[string]interface{}{"value": "tail"}}
			in <- tail
			close(in)
			if behaviorSenderResult(t, ok) != tail || len(bad) != 0 || count.Load() != 2 {
				t.Fatal("partial batch lost, duplicated or failed")
			}
		})
	}
}
func TestBehaviorHTTPAndESRetryOutcomes(t *testing.T) {
	for _, es := range []bool{false, true} {
		for _, failures := range []int32{0, 2, 99} {
			t.Run(fmt.Sprintf("es=%t/fail=%d", es, failures), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ok, bad := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
				var calls atomic.Int32
				client := &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
					status := 200
					if calls.Add(1) <= failures {
						status = 503
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"errors":false}`))}, nil
				})}
				var s SenderItf
				if es {
					e := regressionES()
					e.NFork = 1
					e.BatchSize = 1
					e.httpClient = client
					s = e
				} else {
					h := NewHTTPSender(&HTTPSenderCfg{Addr: "http://example.invalid", NFork: 1, BatchSize: 1, MaxWait: time.Second})
					h.httpClient = client
					s = h
				}
				s.SetSuccessedChan(ok)
				s.SetFailedChan(bad)
				in := s.Spawn(ctx)
				m := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"x": 1}}
				in <- m
				want := failures + 1
				if failures > 3 {
					want = 4
					if behaviorSenderResult(t, bad) != m || len(ok) != 0 {
						t.Fatal("failure acknowledged")
					}
				} else if behaviorSenderResult(t, ok) != m || len(bad) != 0 {
					t.Fatal("success incorrectly failed")
				}
				close(in)
				if calls.Load() != want {
					t.Fatalf("calls=%d want %d", calls.Load(), want)
				}
			})
		}
	}
}
func TestBehaviorHTTPAndESCancellationReachesRequest(t *testing.T) {
	for _, es := range []bool{false, true} {
		t.Run(fmt.Sprint(es), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			finished := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			client := &http.Client{Transport: regressionRoundTripper(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 {
					close(started)
					defer close(finished)
				}
				select {
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-release:
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errors":false}`))}, nil
				}
			})}
			var s SenderItf
			if es {
				e := regressionES()
				e.NFork = 1
				e.BatchSize = 1
				e.httpClient = client
				s = e
			} else {
				h := NewHTTPSender(&HTTPSenderCfg{Addr: "http://example.invalid", NFork: 1, BatchSize: 1, MaxWait: time.Second})
				h.httpClient = client
				s = h
			}
			ok, bad := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
			s.SetSuccessedChan(ok)
			s.SetFailedChan(bad)
			in := s.Spawn(ctx)
			in <- &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"x": 1}}
			<-started
			cancel()
			select {
			case <-finished:
			case <-time.After(100 * time.Millisecond):
				t.Error("worker cancellation did not reach HTTP request")
				close(release)
				<-finished
			}
		})
	}
}
func TestBehaviorBulkSenderConfigurationDefaults(t *testing.T) {
	h := NewHTTPSender(&HTTPSenderCfg{Addr: "http://example.invalid"})
	if h.NFork < 1 || h.BatchSize < 1 || h.MaxWait <= 0 {
		t.Error("HTTP defaults create no worker or panic")
	}
	k := NewKafkaSender(&KafkaSenderCfg{Brokers: []string{"127.0.0.1:1"}, Topic: "logs"})
	if k.NFork < 1 || k.BatchSize < 1 || k.MaxWait <= 0 {
		t.Error("Kafka defaults create no worker or panic")
	}
}
func TestBehaviorElasticsearchValidatesSuppliedItems(t *testing.T) {
	for _, tc := range []struct {
		body string
		bad  bool
	}{
		{`{"errors":false}`, false}, // supported filter_path=errors response
		{`{"errors":false,"items":[{"index":{"status":201}}]}`, false},
		{`{"errors":false,"items":[{"index":{"status":429}}]}`, true},
		{`{"errors":false,"items":[{"index":{}}]}`, true},
		{`{"errors":false,"items":[null]}`, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			s := regressionES()
			err := s.checkResp(&http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))})
			if (err != nil) != tc.bad {
				t.Errorf("response=%s error=%v expected error=%t", tc.body, err, tc.bad)
			}
		})
	}
}
func TestBehaviorKafkaEncodingFailureNeverSendsStalePayload(t *testing.T) {
	broker := sarama.NewMockBroker(t, 0)
	defer broker.Close()
	broker.SetHandlerByMap(map[string]sarama.MockResponse{
		"MetadataRequest": sarama.NewMockMetadataResponse(t).SetBroker(broker.Addr(), broker.BrokerID()).SetLeader("logs", 0, broker.BrokerID()),
		"ProduceRequest":  sarama.NewMockProduceResponse(t).SetError("logs", 0, sarama.ErrNoError),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewKafkaSender(&KafkaSenderCfg{Name: "kafka-behavior", Brokers: []string{broker.Addr()}, Topic: "logs", NFork: 1, BatchSize: 1, InChanSize: 8, MaxWait: time.Second})
	ok, bad := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
	s.SetSuccessedChan(ok)
	s.SetFailedChan(bad)
	in := s.Spawn(ctx)
	first := &library.FluentMsg{ID: 1, Message: map[string]interface{}{"value": "first"}}
	in <- first
	if behaviorSenderResult(t, ok) != first {
		t.Fatal("control delivery failed")
	}
	invalid := &library.FluentMsg{ID: 2, Message: map[string]interface{}{"invalid": make(chan int)}}
	in <- invalid
	select {
	case m := <-bad:
		if m != invalid {
			t.Fatal("wrong failure ownership")
		}
	case <-ok:
		t.Error("unencodable message was acknowledged with stale/empty Kafka payload")
	case <-time.After(time.Second):
		t.Error("invalid message never completed")
	}
	close(in)
}
func TestBehaviorStdoutSenderDisposition(t *testing.T) {
	for _, commit := range []bool{false, true} {
		for _, level := range []string{"info", "debug", "none"} {
			ctx, cancel := context.WithCancel(context.Background())
			s := NewStdoutSender(&StdoutSenderCfg{Name: "behavior", IsCommit: commit, LogLevel: level, InChanSize: 8, Tags: []string{"logs"}})
			ok, bad := make(chan *library.FluentMsg, 8), make(chan *library.FluentMsg, 8)
			s.SetSuccessedChan(ok)
			s.SetFailedChan(bad)
			in := s.Spawn(ctx)
			m := &library.FluentMsg{Tag: "logs", ID: 1, Message: map[string]interface{}{"value": "test"}}
			in <- m
			close(in)
			if commit {
				if behaviorSenderResult(t, ok) != m || len(bad) != 0 {
					t.Fatal("commit disposition")
				}
			} else if behaviorSenderResult(t, bad) != m || len(ok) != 0 {
				t.Fatal("no-commit disposition")
			}
			cancel()
		}
	}
}
