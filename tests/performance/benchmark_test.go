// Package performance benchmarks observable component operations. Disk writes,
// explicit durability barriers, CPU transforms, and network peers are separate.
package performance

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
	"gofluentd/internal/acceptorfilters"
	"gofluentd/internal/controller"
	"gofluentd/internal/monitor"
	"gofluentd/internal/postfilters"
	"gofluentd/internal/recvs"
	"gofluentd/internal/senders"
	"gofluentd/internal/tagfilters"
	"gofluentd/library"
	"gofluentd/library/log"
)

func TestMain(m *testing.M) {
	_ = log.Logger.ChangeLevel("error")
	_ = journal.Logger.ChangeLevel("error")
	_ = utils.Logger.ChangeLevel("error")
	gin.SetMode(gin.ReleaseMode)
	os.Exit(m.Run())
}

// Immutable text is reused; map/wrapper allocation belongs to transform cost.
var payload = strings.Repeat("representative event payload 0123456789 ", 55)[:2048]

func message(id int64) *library.FluentMsg {
	return &library.FluentMsg{Tag: "logs", ID: id, Message: map[string]interface{}{
		"event": id, "log": payload, "tenant": "tenant-17", "level": "INFO",
		"host": "node-a", "service": "ingester", "sequence": id, "source": "stdout",
	}}
}
func rate(b *testing.B, n int) {
	b.ReportMetric(float64(n), "msgs/op")
	b.ReportMetric(float64(b.N)*float64(n)/b.Elapsed().Seconds(), "msgs/s")
}
func require(b *testing.B, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
}

func BenchmarkPerfAcceptorFilter(b *testing.B) {
	f := acceptorfilters.NewDefaultFilter(&acceptorfilters.DefaultFilterCfg{RemoveEmptyTag: true, RemoveUnsupportTag: true, AcceptTags: []string{"logs"}, AddCfg: library.AddCfg{"logs": {{"route": "%{tenant}-%{@tag}"}}}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := f.Filter(message(int64(i)))
		if m == nil || m.Message["route"] != "tenant-17-logs" {
			b.Fatal("bad accepted message")
		}
	}
	rate(b, 1)
}
func BenchmarkPerfPostFilter(b *testing.B) {
	for _, rename := range []bool{false, true} {
		b.Run(fmt.Sprintf("rename=%v", rename), func(b *testing.B) {
			f := postfilters.NewDefaultFilter(&postfilters.DefaultFilterCfg{MaxLen: 4096})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := message(int64(i))
				if rename {
					m.Message["nested.field"] = []byte("value")
				}
				got := f.Filter(m)
				if got.Message["log"] != payload {
					b.Fatal("payload changed")
				}
				if rename && got.Message["nested__field"] != "value" {
					b.Fatal("normalization failed")
				}
			}
			rate(b, 1)
		})
	}
}
func BenchmarkPerfParser(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := tagfilters.NewParserFact(&tagfilters.ParserFactCfg{Tags: []string{"logs"}, ParseJSONKey: "args", MustInclude: "tenant"})
	in, out := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
	done := make(chan struct{})
	go func() { defer close(done); f.StartNewParser(ctx, out, in) }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := message(int64(i))
		m.Message["args"] = `{"nested":{"value":42}}`
		in <- m
		got := <-out
		if got.ID != int64(i) || got.Message["nested__value"] != float64(42) {
			b.Fatal("parser mismatch")
		}
	}
	b.StopTimer()
	close(in)
	<-done
	rate(b, 1)
}
func BenchmarkPerfConcatenator(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &tagfilters.ConcatorCfg{MsgKey: "log", Identifier: "source", Regexp: regexp.MustCompile(`^HEAD`)}
	f := tagfilters.NewConcatorFact(&tagfilters.ConcatorFactCfg{NFork: 1, MaxLen: 8, Plugins: map[string]*tagfilters.ConcatorCfg{"logs": cfg}})
	f.SetMsgPool(&sync.Pool{})
	in, out := make(chan *library.FluentMsg, 2), make(chan *library.FluentMsg, 1)
	done := make(chan struct{})
	go func() { defer close(done); f.StartNewConcator(ctx, cfg, out, in) }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h, t := message(int64(i*2)), message(int64(i*2+1))
		h.Message["log"] = "HEAD"
		t.Message["log"] = " tail"
		in <- h
		in <- t
		got := <-out
		if string(got.Message["log"].([]byte)) != "HEAD tail" || len(got.ExtIds) != 1 || got.ExtIds[0] != t.ID {
			b.Fatal("concat ownership mismatch")
		}
	}
	b.StopTimer()
	close(in)
	<-done
	rate(b, 2)
}
func BenchmarkPerfFluentEncoder(b *testing.B) {
	for _, n := range []int{1, 64, 512} {
		b.Run(fmt.Sprintf("batch=%d", n), func(b *testing.B) {
			msgs := make([]*library.FluentMsg, n)
			for i := range msgs {
				msgs[i] = message(int64(i))
			}
			enc := library.NewFluentEncoder(io.Discard)
			b.ReportAllocs()
			b.SetBytes(int64(n * len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				require(b, enc.EncodeBatch("logs", msgs))
				require(b, enc.Flush())
			}
			rate(b, n)
		})
	}
}

type passthrough struct{}

func (passthrough) Spawn(_ context.Context, _ string, out chan<- *library.FluentMsg) (chan<- *library.FluentMsg, error) {
	return out, nil
}
func BenchmarkPerfDispatcher(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 1)
	d := controller.NewDispatcher(&controller.DispatcherCfg{InChan: in, TagPipeline: passthrough{}, NFork: 1, OutChanSize: 1})
	d.Run(ctx)
	m := message(1)
	in <- m
	<-d.GetOutChan() // warm tag creation
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in <- m
		if <-d.GetOutChan() != m {
			b.Fatal("lost routed record")
		}
	}
	b.StopTimer()
	close(in)
	rate(b, 1)
}

type ackSender struct {
	senders.BaseSender
	name   string
	result chan<- *library.FluentMsg
	done   chan struct{}
}

func (s *ackSender) GetName() string                               { return s.name }
func (s *ackSender) SetSuccessedChan(ch chan<- *library.FluentMsg) { s.result = ch }
func (s *ackSender) Spawn(ctx context.Context) chan<- *library.FluentMsg {
	in := make(chan *library.FluentMsg, 1)
	go func() {
		defer close(s.done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-in:
				select {
				case s.result <- m:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return in
}
func BenchmarkPerfProducer(b *testing.B) {
	for _, n := range []int{1, 2} {
		b.Run(fmt.Sprintf("sinks=%d", n), func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ss := make([]senders.SenderItf, n)
			for i := range ss {
				s := &ackSender{name: fmt.Sprint(i), done: make(chan struct{})}
				s.SetSupportedTags([]string{"logs"})
				ss[i] = s
			}
			in, out := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
			p, err := controller.NewProducer(&controller.ProducerCfg{NFork: 1, InChan: in, CommitChan: out, MsgPool: &sync.Pool{}, DiscardChanSize: 8}, ss...)
			require(b, err)
			p.Run(ctx)
			m := message(1)
			in <- m
			<-out
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				in <- m
				if <-out != m {
					b.Fatal("bad acknowledgement")
				}
			}
			b.StopTimer()
			close(in)
			cancel()
			for _, s := range ss {
				<-s.(*ackSender).done
			}
			rate(b, 1)
		})
	}
}
func BenchmarkPerfMonitor(b *testing.B) {
	for i := 0; i < 32; i++ {
		monitor.AddMetric(fmt.Sprintf("bench-%d", i), func() map[string]interface{} { return map[string]interface{}{"count": 123, "depth": 5} })
	}
	e := gin.New()
	monitor.BindHTTP(e)
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
}
func BenchmarkPerfHTTPReceive(b *testing.B) {
	e := gin.New()
	r := recvs.NewHTTPRecv(&recvs.HTTPRecvCfg{HTTPSrv: e, Path: "/logs/:env", Env: "sit", Tag: "forward", OrigTag: "app", TagKey: "tag", TimeKey: "ts", TimeFormat: time.RFC3339, TSRegexp: regexp.MustCompile(`^.{20}$`), SigKey: "sig", SigSalt: []byte("perf"), MaxBodySize: 8192, MaxAllowedDelaySec: time.Hour, MaxAllowedAheadSec: time.Minute})
	pool := &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}
	r.SetMsgPool(pool)
	r.SetCounter(utils.NewCounter())
	out := make(chan *library.FluentMsg, 1)
	r.SetAsyncOutChan(out)
	ts := time.Now().UTC().Format(time.RFC3339)
	sum := md5.Sum([]byte(ts + "perf"))
	body, err := json.Marshal(map[string]interface{}{"ts": ts, "sig": hex.EncodeToString(sum[:]), "log": payload})
	require(b, err)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest("POST", "/logs/sit", bytes.NewReader(body)))
		if w.Code != 200 {
			b.Fatal(w.Code, w.Body.String())
		}
		m := <-out
		if m.Message["log"] != payload {
			b.Fatal("bad received payload")
		}
		pool.Put(m)
	}
	rate(b, 1)
}

// This local peer decodes every actual request. This measures sender preparation
// and loopback transfer, not durable backend or public network capacity.
func BenchmarkPerfHTTPSender(b *testing.B) {
	for _, kind := range []string{"http", "es"} {
		b.Run(kind, func(b *testing.B) {
			const batch = 64
			var received atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gz, err := gzip.NewReader(r.Body)
				if err != nil {
					b.Error(err)
					w.WriteHeader(400)
					return
				}
				data, err := io.ReadAll(gz)
				gz.Close()
				r.Body.Close()
				if err != nil {
					b.Error(err)
					w.WriteHeader(400)
					return
				}
				if kind == "http" {
					var docs []map[string]interface{}
					if err = json.Unmarshal(data, &docs); err != nil || len(docs) == 0 || len(docs) > batch {
						b.Errorf("invalid batch: %s", data)
						w.WriteHeader(400)
						return
					}
					received.Add(int64(len(docs)))
				} else {
					lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
					if len(lines) == 0 || len(lines)%2 != 0 || len(lines) > batch*2 {
						b.Error("invalid bulk count")
						w.WriteHeader(400)
						return
					}
					for j := 1; j < len(lines); j += 2 {
						var doc map[string]interface{}
						if err = json.Unmarshal(lines[j], &doc); err != nil || doc["log"] != payload {
							b.Error("corrupt bulk payload")
							w.WriteHeader(400)
							return
						}
					}
					received.Add(int64(len(lines) / 2))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"errors":false}`))
			}))
			defer srv.Close()
			var s senders.SenderItf
			if kind == "http" {
				s = senders.NewHTTPSender(&senders.HTTPSenderCfg{Addr: srv.URL, NFork: 1, BatchSize: batch, MaxWait: time.Hour, InChanSize: batch, Tags: []string{"logs"}})
			} else {
				s = senders.NewElasticSearchSender(&senders.ElasticSearchSenderCfg{Addr: srv.URL, NFork: 1, BatchSize: batch, MaxWait: time.Hour, InChanSize: batch, Tags: []string{"logs"}, TagIndexMap: map[string]string{"logs": "bench"}})
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ok, failed := make(chan *library.FluentMsg, batch), make(chan *library.FluentMsg, batch)
			s.SetSuccessedChan(ok)
			s.SetFailedChan(failed)
			in := s.Spawn(ctx)
			// The worker sends the first message immediately. Warm that path.
			in <- message(-1)
			select {
			case <-ok:
			case <-failed:
				b.Fatal("warmup failed")
			case <-time.After(10 * time.Second):
				b.Fatal("warmup timeout")
			}
			received.Store(0)
			msgs := make([]*library.FluentMsg, batch)
			for i := range msgs {
				msgs[i] = message(int64(i))
			}
			b.ReportAllocs()
			b.SetBytes(batch * int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, m := range msgs {
					in <- m
				}
				for j := 0; j < batch; j++ {
					select {
					case <-ok:
					case <-failed:
						b.Fatal("sender failed")
					case <-time.After(10 * time.Second):
						b.Fatal("sender timed out")
					}
				}
			}
			b.StopTimer()
			if received.Load() != int64(b.N*batch) {
				b.Fatal("peer did not receive every record")
			}
			rate(b, batch)
		})
	}
}

func openJournal(b *testing.B, dir string, compressed bool) *journal.Journal {
	b.Helper()
	j, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(compressed), journal.WithIsAggresiveGC(false), journal.WithBufSizeByte(64<<20), journal.WithRotateDuration(time.Hour), journal.WithRotateCheckInterval(time.Hour), journal.WithFlushInterval(time.Hour), journal.WithCommitIDTTL(time.Hour))
	require(b, err)
	require(b, j.Start(context.Background()))
	return j
}
func BenchmarkPerfJournalAppend(b *testing.B) {
	for _, compressed := range []bool{false, true} {
		for _, every := range []int{0, 1, 64} {
			b.Run(fmt.Sprintf("gzip=%v/syncEvery=%d", compressed, every), func(b *testing.B) {
				dir := b.TempDir()
				j := openJournal(b, dir, compressed)
				defer j.Close()
				record := &journal.Data{Data: message(0).Message}
				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					record.ID = int64(i + 1)
					require(b, j.WriteData(record))
					if every > 0 && (i+1)%every == 0 {
						require(b, j.Sync())
					}
				}
				require(b, j.Sync())
				b.StopTimer()
				j.Close()
				// Reopen through the public API. No fabricated file list or extra rotation.
				reopened := openJournal(b, dir, compressed)
				defer reopened.Close()
				high, err := reopened.LoadMaxId()
				require(b, err)
				if high != int64(b.N) {
					b.Fatalf("persisted high-water=%d want=%d", high, b.N)
				}
				rate(b, 1)
			})
		}
	}
}
func BenchmarkPerfJournalIDs(b *testing.B) {
	for _, compressed := range []bool{false, true} {
		b.Run(fmt.Sprintf("gzip=%v", compressed), func(b *testing.B) {
			j := openJournal(b, b.TempDir(), compressed)
			defer j.Close()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				require(b, j.WriteId(int64(i+1)))
			}
			require(b, j.Sync())
			b.StopTimer()
			if j.GetMetric()["idsSetLen"].(int) != b.N {
				b.Fatal("lost unique ACKs")
			}
			rate(b, 1)
		})
	}
}
func BenchmarkPerfJournalRecovery(b *testing.B) {
	for _, compressed := range []bool{false, true} {
		for _, n := range []int{64, 4096} {
			b.Run(fmt.Sprintf("gzip=%v/records=%d", compressed, n), func(b *testing.B) {
				dir := b.TempDir()
				j := openJournal(b, dir, compressed)
				record := &journal.Data{Data: message(0).Message}
				for i := 0; i < n; i++ {
					record.ID = int64(i + 1)
					require(b, j.WriteData(record))
					if i%2 == 0 {
						require(b, j.WriteId(record.ID))
					}
				}
				require(b, j.Sync())
				j.Close()
				reopened := openJournal(b, dir, compressed)
				defer reopened.Close()
				b.ReportAllocs()
				b.SetBytes(int64(n * len(payload)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					high, err := reopened.LoadMaxId()
					require(b, err)
					if high != int64(n) {
						b.Fatalf("high-water=%d want=%d", high, n)
					}
				}
				rate(b, n)
			})
		}
	}
}
func BenchmarkPerfJournalReplay(b *testing.B) {
	for _, compressed := range []bool{false, true} {
		b.Run(fmt.Sprintf("gzip=%v", compressed), func(b *testing.B) {
			const n = 1024
			dir := b.TempDir()
			dataName := filepath.Join(dir, "records.buf")
			idsName := filepath.Join(dir, "acks.ids")
			if compressed {
				dataName += ".gz"
				idsName += ".gz"
			}
			df, err := os.Create(dataName)
			require(b, err)
			af, err := os.Create(idsName)
			require(b, err)
			de, err := journal.NewDataEncoder(df, compressed)
			require(b, err)
			ae, err := journal.NewIdsEncoder(af, compressed)
			require(b, err)
			for i := 1; i <= n; i++ {
				require(b, de.Write(&journal.Data{ID: int64(i), Data: message(int64(i)).Message}))
				if i%2 == 0 {
					require(b, ae.Write(int64(i)))
				}
			}
			require(b, de.Close())
			require(b, ae.Close())
			require(b, df.Close())
			require(b, af.Close())
			// Loader construction belongs to each measured pass; buffers must not be
			// warmed in setup when measuring the allocations needed by real recovery.
			b.ReportAllocs()
			b.SetBytes(int64(n * len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				l := journal.NewLegacyLoader(ctx, journal.Logger, []string{dataName}, []string{idsName}, compressed, time.Hour)
				seen := 0
				for {
					var d journal.Data
					err := l.Load(&d)
					if err == io.EOF {
						break
					}
					require(b, err)
					if d.ID%2 == 0 || d.Data["log"] != payload {
						b.Fatal("incorrect replay")
					}
					seen++
				}
				cancel()
				if seen != n/2 {
					b.Fatal("lost replay records")
				}
			}
			rate(b, n)
		})
	}
}
func BenchmarkPerfJournalTTL(b *testing.B) {
	const n = 65536
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := journal.NewInt64SetWithTTL(ctx, time.Hour)
	defer s.Close()
	for i := 0; i < n; i++ {
		s.AddInt64(int64(i))
	}
	for _, mode := range []string{"hit", "miss", "refresh"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				id := int64(i % n)
				switch mode {
				case "hit":
					if !s.CheckAndRemove(id) {
						b.Fatal("ACK lookup consumed ID")
					}
				case "miss":
					if s.CheckAndRemove(id + n) {
						b.Fatal("false ACK")
					}
				case "refresh":
					s.AddInt64(id)
				}
			}
			rate(b, 1)
		})
	}
}
