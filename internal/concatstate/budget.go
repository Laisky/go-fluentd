// Package concatstate owns finite, process-shared multiline reservations.
package concatstate

import (
	"errors"
	"sync"

	"gofluentd/library"
)

const (
	DefaultMessages = 1024
	DefaultBytes    = int64(16 << 20)
	MaxMessages     = 65536
	MaxBytes        = int64(1 << 30)
	MaxValues       = 65536
	MaxDepth        = 32
	ExpirePerTurn   = 32
)

// Budget is shared by all receiver and post-journal workers of a configured
// process. Counts include pending records blocked on downstream handoff.
// Bytes are conservative accounting units, not a process RSS quota.
type Budget struct {
	mu                  sync.Mutex
	entries, maxEntries int
	bytes, maxBytes     int64
	refused             uint64
}
type Stats struct {
	Entries, MaxEntries int
	Bytes, MaxBytes     int64
	Refused             uint64
}

func NewBudget(entries int, bytes int64) (*Budget, error) {
	if entries == 0 {
		entries = DefaultMessages
	}
	if bytes == 0 {
		bytes = DefaultBytes
	}
	if entries < 1 || entries > MaxMessages || bytes < 1 || bytes > MaxBytes {
		return nil, errors.New("concat limits require 1..65536 messages and 1..1073741824 bytes; zero selects finite defaults")
	}
	return &Budget{maxEntries: entries, maxBytes: bytes}, nil
}
func Default(b *Budget) *Budget {
	if b != nil {
		return b
	}
	b, _ = NewBudget(0, 0)
	return b
}
func (b *Budget) reserve(entries int, bytes int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes < 0 || entries < 0 || entries > b.maxEntries-b.entries || bytes > b.maxBytes-b.bytes {
		b.refused++
		return false
	}
	b.entries += entries
	b.bytes += bytes
	return true
}
func (b *Budget) release(entries int, bytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries -= entries
	b.bytes -= bytes
}
func (b *Budget) refusal() { b.mu.Lock(); b.refused++; b.mu.Unlock() }
func (b *Budget) Snapshot() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{b.entries, b.maxEntries, b.bytes, b.maxBytes, b.refused}
}

// Weight walks only the bounded, supported record shape without serialization.
// Unknown types, cycles, depth/work excess and oversized metadata bypass
// concatenation rather than creating an unmeasured long-lived reservation.
func Weight(m *library.FluentMsg, identifier string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	w := weight{bytes: 512, remaining: MaxValues}
	if !w.add(int64(len(identifier))) || !w.add(int64(len(m.Tag))*2) || !w.add(int64(len(m.JournalTag))*2) || !w.add(int64(len(m.SourceFormat))) || !w.add(int64(len(m.DeliveryID))) || !w.add(int64(cap(m.ExtIds))*8) || !w.value(m.Message, 0) {
		return 0, false
	}
	return w.bytes, true
}

type weight struct {
	bytes     int64
	remaining int
}

func (w *weight) add(n int64) bool {
	if n < 0 || n > MaxBytes-w.bytes {
		return false
	}
	w.bytes += n
	return true
}
func (w *weight) value(v interface{}, depth int) bool {
	w.remaining--
	if w.remaining < 0 || depth > MaxDepth || !w.add(32) {
		return false
	}
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	case string:
		return w.add(int64(len(x)))
	case []byte:
		return w.add(int64(cap(x)))
	case []interface{}:
		if len(x) > w.remaining || !w.add(int64(cap(x))*16) {
			return false
		}
		for _, v := range x {
			if !w.value(v, depth+1) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		if len(x) > w.remaining || !w.add(int64(len(x))*128) {
			return false
		}
		for k, v := range x {
			if !w.add(int64(len(k))) || !w.value(v, depth+1) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
