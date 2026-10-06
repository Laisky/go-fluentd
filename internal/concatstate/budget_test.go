package concatstate

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"gofluentd/library"
)

func line(id int64, source, text string) *library.FluentMsg {
	return &library.FluentMsg{ID: id, Tag: "a", JournalTag: "original", Message: map[string]interface{}{"source": source, "log": []byte(text)}}
}
func rule(m *library.FluentMsg) (Rule, bool) {
	return Rule{MessageKey: "log", IdentifierKey: "source", Head: regexp.MustCompile("^HEAD")}, true
}
func take(t *testing.T, ch <-chan *library.FluentMsg) *library.FluentMsg {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(time.Second):
		t.Fatal("handoff timeout")
		return nil
	}
}
func start(ctx context.Context, b *Budget, in chan *library.FluentMsg, out chan *library.FluentMsg, wait time.Duration) *sync.WaitGroup {
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		Run(ctx, in, b, wait, 10000, true, rule, func(m *library.FluentMsg) bool {
			select {
			case out <- m:
				return true
			case <-ctx.Done():
				return false
			}
		}, func(*library.FluentMsg) {})
	}()
	return wg
}
func barrier(t *testing.T, in chan *library.FluentMsg, out chan *library.FluentMsg) {
	t.Helper()
	m := &library.FluentMsg{Message: map[string]interface{}{"barrier": true}}
	in <- m
	if take(t, out) != m {
		t.Fatal("unexpected buffered emission before barrier")
	}
}
func empty(t *testing.T, b *Budget) {
	t.Helper()
	n := b.Snapshot()
	if n.Entries != 0 || n.Bytes != 0 {
		t.Fatalf("reservation leak: %+v", n)
	}
}

func TestBudgetConfigurationAndAtomicReservation(t *testing.T) {
	b, err := NewBudget(0, 0)
	if err != nil || b.Snapshot().MaxEntries != DefaultMessages || b.Snapshot().MaxBytes != DefaultBytes {
		t.Fatal(b, err)
	}
	if Default(b) != b || Default(nil) == nil {
		t.Fatal("default budget ownership")
	}
	for _, c := range []struct {
		n int
		b int64
	}{{-1, 1}, {MaxMessages + 1, 1}, {1, -1}, {1, MaxBytes + 1}} {
		if _, err := NewBudget(c.n, c.b); err == nil {
			t.Fatal(c)
		}
	}
	b, _ = NewBudget(2, 3)
	if !b.reserve(1, 2) || b.reserve(1, 2) || !b.reserve(1, 1) || b.reserve(1, 0) || b.reserve(0, 1) || b.reserve(-1, 0) || b.reserve(0, -1) {
		t.Fatal(b.Snapshot())
	}
	b.release(2, 3)
	empty(t, b)
	b, _ = NewBudget(8, 64)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.reserve(1, 8) }()
	}
	wg.Wait()
	if n := b.Snapshot(); n.Entries != 8 || n.Bytes != 64 || n.Refused != 92 {
		t.Fatal(n)
	}
}

func TestWeightCompleteMetadataAndWorkBounds(t *testing.T) {
	m := line(1, "s", "HEAD")
	base, ok := Weight(m, "s")
	if !ok {
		t.Fatal("ordinary record")
	}
	m.ExtIds = make([]int64, 0, 1024)
	m.Message["large"] = make([]byte, 0, 4096)
	m.DeliveryID = strings.Repeat("d", 50)
	m.SourceFormat = "legacy"
	n, ok := Weight(m, "s")
	if !ok || n-base < 8192+4096+50+6 {
		t.Fatal(n, base, ok)
	}
	if _, ok := Weight(nil, ""); ok {
		t.Fatal("nil weighed")
	}
	for _, v := range []interface{}{make(chan int), make([]interface{}, MaxValues+1)} {
		m.Message["bad"] = v
		if _, ok := Weight(m, "s"); ok {
			t.Fatalf("unbounded/unknown value %T", v)
		}
	}
	cyclic := map[string]interface{}{}
	cyclic["self"] = cyclic
	m.Message = cyclic
	if _, ok := Weight(m, "s"); ok {
		t.Fatal("cycle accepted")
	}
	m.Message = map[string]interface{}{"scalar": []interface{}{nil, true, int8(1), uint64(1), float64(1), "ok", []byte("x"), map[string]interface{}{}}}
	if _, ok := Weight(m, ""); !ok {
		t.Fatal("ordinary nested scalar rejected")
	}
	w := weight{bytes: MaxBytes, remaining: 1}
	if w.add(1) || w.add(-1) {
		t.Fatal("overflow admitted")
	}
	if capacity(0, 4) != 4 || capacity(4, 5) != 8 || capacity(4, 4) != 4 || capacity(math.MaxInt, 1) != math.MaxInt {
		t.Fatal("capacity arithmetic")
	}
}

func TestByteBoundaryAndPreallocationGrowth(t *testing.T) {
	for _, delta := range []int64{0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			h := line(1, "s", "HEAD")
			n, _ := Weight(h, "s")
			b, _ := NewBudget(3, n-delta)
			in, out := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 4)
			in <- h
			close(in)
			Run(context.Background(), in, b, time.Hour, 10000, true, rule, func(m *library.FluentMsg) bool { out <- m; return true }, func(*library.FluentMsg) {})
			if take(t, out) != h {
				t.Fatal("boundary lost record")
			}
			if (b.Snapshot().Refused > 0) != (delta > 0) {
				t.Fatal(b.Snapshot())
			}
			empty(t, b)
		})
	}
	for _, kind := range []string{"text", "ids"} {
		t.Run(kind, func(t *testing.T) {
			h := line(1, "s", "HEAD")
			tail := line(2, "s", "")
			if kind == "text" {
				tail.Message["log"] = []byte("more")
			}
			tail.ExtIds = []int64{3, 4}
			n, _ := Weight(h, "s")
			b, _ := NewBudget(1, n)
			if !b.reserve(1, n) {
				t.Fatal("setup")
			}
			p := &pending{msg: h, charge: n}
			before := append([]byte(nil), h.Message["log"].([]byte)...)
			oldCap := cap(h.Message["log"].([]byte))
			if grow(b, p, tail, "log", tail.Message["log"].([]byte), true) {
				t.Fatal("unreserved growth")
			}
			if !reflect.DeepEqual(before, h.Message["log"]) || cap(h.Message["log"].([]byte)) != oldCap || len(h.ExtIds) != 0 || p.charge != n {
				t.Fatal("mutation before refusal")
			}
			b.release(1, n)
			empty(t, b)
		})
	}
}

func TestWorkerSaturationKeepsOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := line(1, "s", "HEAD")
		n, _ := Weight(h, "s")
		b, _ := NewBudget(2, n)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg, 8)
		wg := start(ctx, b, in, out, time.Hour)
		defer wg.Wait()
		in <- h
		barrier(t, in, out)
		if b.Snapshot().Entries != 1 {
			t.Fatal("head not retained")
		}
		tail := line(2, "s", " tail")
		tail.ExtIds = []int64{9}
		in <- tail
		if take(t, out) != h || take(t, out) != tail || !reflect.DeepEqual(tail.ExtIds, []int64{9}) || tail.JournalTag != "original" || tail.ID != 2 || len(h.ExtIds) != 0 {
			t.Fatal("false ownership transfer at saturation")
		}
		synctest.Wait()
		empty(t, b)
		close(in)
	})
}

func TestWorkerSharedBudgetMetadataBypassAndNormalJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBudget(1, 4096)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, z, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg), make(chan *library.FluentMsg, 8)
		w1, w2 := start(ctx, b, a, out, time.Hour), start(ctx, b, z, out, time.Hour)
		defer w1.Wait()
		defer w2.Wait()
		h := line(1, "s", "HEAD")
		a <- h
		barrier(t, a, out)
		other := line(2, "other", "HEAD")
		z <- other
		if take(t, out) != other {
			t.Fatal("workers bypass shared cap")
		}
		tail := line(3, "s", " tail")
		tail.ExtIds = []int64{4}
		a <- tail
		barrier(t, a, out)
		if string(h.Message["log"].([]byte)) != "HEAD tail" || !reflect.DeepEqual(h.ExtIds, []int64{3, 4}) {
			t.Fatal("join ownership")
		}
		close(a)
		if take(t, out) != h {
			t.Fatal("close flush")
		}
		w1.Wait()
		empty(t, b)
		large := line(5, "large", "HEAD")
		large.Message["metadata"] = strings.Repeat("x", 5000)
		z <- large
		if take(t, out) != large {
			t.Fatal("metadata not charged")
		}
		bad := line(6, "bad", "HEAD")
		bad.Message["unknown"] = make(chan int)
		z <- bad
		if take(t, out) != bad {
			t.Fatal("unknown type retained")
		}
		close(z)
		w2.Wait()
		empty(t, b)
	})
}

func TestExpiryWorkPerTurnAndTouch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBudget(100, 1<<20)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg, 100)
		wg := start(ctx, b, in, out, time.Second)
		defer wg.Wait()
		for i := 0; i < 70; i++ {
			in <- line(int64(i), fmt.Sprint(i), "HEAD")
		}
		barrier(t, in, out)
		time.Sleep(500 * time.Millisecond)
		in <- line(100, "0", "tail")
		barrier(t, in, out)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		if len(out) != ExpirePerTurn || b.Snapshot().Entries != 70-ExpirePerTurn {
			t.Fatalf("expiry work unbounded: out=%d stats=%+v", len(out), b.Snapshot())
		}
		time.Sleep(80 * time.Millisecond)
		synctest.Wait()
		if b.Snapshot().Entries != 1 {
			t.Fatal("expiry queue order/touch", b.Snapshot())
		}
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		empty(t, b)
		close(in)
	})
}

func TestBlockedHandoffRetainsReservationAndCancelReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b, _ := NewBudget(1, 4096)
		ctx, cancel := context.WithCancel(context.Background())
		in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg)
		wg := start(ctx, b, in, out, time.Hour)
		in <- line(1, "s", "HEAD")
		close(in)
		synctest.Wait()
		if n := b.Snapshot(); n.Entries != 1 || n.Bytes == 0 {
			t.Fatal("released before downstream handoff", n)
		}
		cancel()
		wg.Wait()
		empty(t, b)
	})
}

func TestSeparateJournalOwnersAndAcceptanceReceipts(t *testing.T) {
	for _, kind := range []string{"journal", "source", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			b, _ := NewBudget(4, 10000)
			h, tail := line(1, "s", "HEAD"), line(2, "s", "tail")
			switch kind {
			case "journal":
				tail.JournalTag = "another"
			case "source":
				tail.SourceFormat = "json"
			case "receipt":
				h.DurableAck = make(chan error, 1)
				tail.DurableAck = make(chan error, 1)
			}
			in, out := make(chan *library.FluentMsg, 2), make(chan *library.FluentMsg, 4)
			in <- h
			in <- tail
			close(in)
			Run(context.Background(), in, b, time.Hour, 10000, true, rule, func(m *library.FluentMsg) bool { out <- m; return true }, func(*library.FluentMsg) { t.Error("independent record recycled") })
			if len(out) != 2 || len(h.ExtIds) != 0 || len(h.DurableAck) != 0 || len(tail.DurableAck) != 0 {
				t.Fatal("false journal/acceptance transfer")
			}
			empty(t, b)
		})
	}
}

func BenchmarkPendingBudget(b *testing.B) {
	for _, n := range []int{16, 256, 1024} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			budget, _ := NewBudget(n, 1<<20)
			m := line(1, "s", "HEAD")
			w, _ := Weight(m, "s")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for k := 0; k < n; k++ {
					budget.reserve(1, w)
				}
				for k := 0; k < n; k++ {
					budget.release(1, w)
				}
			}
			b.ReportMetric(float64(budget.Snapshot().Entries), "retained-entries")
		})
	}
}

func TestEmptyTailsCannotAccumulateUnboundedAcknowledgementIDs(t *testing.T) {
	b, _ := NewBudget(2, 2048)
	in, out := make(chan *library.FluentMsg, 202), make(chan *library.FluentMsg, 202)
	in <- line(1, "s", "HEAD")
	for i := 2; i <= 201; i++ {
		in <- line(int64(i), "s", "")
	}
	close(in)
	Run(context.Background(), in, b, time.Hour, 10000, true, rule, func(m *library.FluentMsg) bool { out <- m; return true }, func(*library.FluentMsg) {})
	if b.Snapshot().Refused == 0 || len(out) < 2 {
		t.Fatal("ACK growth bypassed byte budget")
	}
	seen := map[int64]int{}
	for len(out) > 0 {
		m := <-out
		seen[m.ID]++
		for _, id := range m.ExtIds {
			seen[id]++
		}
	}
	if len(seen) != 201 {
		t.Fatal("missing ACK identity", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatal("duplicate ACK identity", id, n)
		}
	}
	empty(t, b)
}

func FuzzPendingStateOwnership(f *testing.F) {
	f.Add([]byte{1, 2, 3, 0, 1, 4, 5, 2, 7})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128 {
			return
		}
		budget, _ := NewBudget(3, 2048)
		in := make(chan *library.FluentMsg, len(data))
		count := 0
		for i, v := range data {
			text := " tail"
			if v%3 == 0 {
				text = "HEAD"
			}
			if v%7 == 0 {
				text = ""
			}
			m := line(int64(2*i+1), fmt.Sprint(v%5), text)
			m.ExtIds = []int64{int64(2*i + 2)}
			m.JournalTag = fmt.Sprint(v % 2)
			in <- m
			count += 2
		}
		close(in)
		seen := map[int64]int{}
		Run(context.Background(), in, budget, time.Hour, 20, true, rule, func(m *library.FluentMsg) bool {
			seen[m.ID]++
			for _, id := range m.ExtIds {
				seen[id]++
			}
			return true
		}, func(*library.FluentMsg) {})
		if len(seen) != count {
			t.Fatal("lost identity", len(seen), count)
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatal("duplicate identity", id, n)
			}
		}
		empty(t, budget)
	})
}
