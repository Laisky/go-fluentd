package concatstate

import (
	"container/list"
	"context"
	"regexp"
	"strings"
	"time"

	"gofluentd/library"
)

type Rule struct {
	MessageKey, IdentifierKey string
	Head                      *regexp.Regexp
}
type key struct{ tag, identifier, journal string }
type pending struct {
	key     key
	msg     *library.FluentMsg
	charge  int64
	updated time.Time
	node    *list.Element
}

// Run owns one worker's pending state. A bounded expiry queue replaces scans.
// Saturation emits records separately. It never acknowledges any record.
// The caller supplies handoff and wrapper recycling, not delivery callbacks.
func Run(ctx context.Context, in <-chan *library.FluentMsg, budget *Budget, wait time.Duration, maxLen int, withIDs bool, rule func(*library.FluentMsg) (Rule, bool), emit func(*library.FluentMsg) bool, recycle func(*library.FluentMsg)) {
	budget = Default(budget)
	slot := make(map[key]*pending)
	order := list.New()
	remove := func(p *pending) { delete(slot, p.key); order.Remove(p.node); budget.release(1, p.charge); p.msg = nil }
	defer func() {
		for order.Len() > 0 {
			remove(order.Front().Value.(*pending))
		}
	}()
	send := func(p *pending) bool {
		if !emit(p.msg) {
			return false
		}
		remove(p)
		return true
	}
	add := func(k key, m *library.FluentMsg, now time.Time) bool {
		n, ok := Weight(m, k.identifier)
		if !ok {
			budget.refusal()
			return emit(m)
		}
		if !budget.reserve(1, n) {
			return emit(m)
		}
		k = key{strings.Clone(k.tag), strings.Clone(k.identifier), strings.Clone(k.journal)}
		p := &pending{key: k, msg: m, charge: n, updated: now}
		p.node = order.PushBack(p)
		slot[k] = p
		return true
	}
	interval := wait / 4
	if interval <= 0 {
		interval = time.Millisecond
	}
	if interval > 40*time.Millisecond {
		interval = 40 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for n := 0; n < ExpirePerTurn && order.Len() > 0; n++ {
				p := order.Front().Value.(*pending)
				if now.Sub(p.updated) < wait {
					break
				}
				if !send(p) {
					return
				}
			}
		case m, ok := <-in:
			if !ok {
				for order.Len() > 0 {
					if !send(order.Front().Value.(*pending)) {
						return
					}
				}
				return
			}
			if m == nil {
				continue
			}
			cfg, ok := rule(m)
			if !ok {
				if !emit(m) {
					return
				}
				continue
			}
			var text []byte
			switch v := m.Message[cfg.MessageKey].(type) {
			case string:
				text = []byte(v)
				m.Message[cfg.MessageKey] = text
			case []byte:
				text = v
			default:
				if !emit(m) {
					return
				}
				continue
			}
			var id string
			switch v := m.Message[cfg.IdentifierKey].(type) {
			case string:
				id = v
			case []byte:
				id = string(v)
			default:
				if !emit(m) {
					return
				}
				continue
			}
			k := key{m.Tag, id, m.JournalTag}
			p := slot[k]
			now := time.Now()
			head := cfg.Head.Match(text)
			if p != nil && (head || now.Sub(p.updated) >= wait) {
				if !send(p) {
					return
				}
				p = nil
			}
			if p == nil {
				if !head || len(text) >= maxLen {
					if !emit(m) {
						return
					}
				} else if !add(k, m, now) {
					return
				}
				continue
			}
			// Different source envelopes must not inherit another record's protocol or
			// acceptance receipt. Their independent journal ownership is retained.
			if p.msg.SourceFormat != m.SourceFormat || p.msg.DurableAck != nil || m.DurableAck != nil || !grow(budget, p, m, cfg.MessageKey, text, withIDs) {
				if !send(p) || !emit(m) {
					return
				}
				continue
			}
			p.updated = now
			order.MoveToBack(p.node)
			m.ExtIds = nil
			recycle(m)
			if len(p.msg.Message[cfg.MessageKey].([]byte)) >= maxLen {
				if !send(p) {
					return
				}
			}
		}
	}
}

func capacity(old, needed int) int {
	if needed <= old {
		return old
	}
	if old <= int(MaxBytes)/2 && old*2 >= needed {
		return old * 2
	}
	return needed
}
func grow(b *Budget, p *pending, tail *library.FluentMsg, msgKey string, text []byte, withIDs bool) bool {
	old := p.msg.Message[msgKey].([]byte)
	if len(text) > int(MaxBytes)-len(old) {
		b.refusal()
		return false
	}
	needed := len(old) + len(text)
	newCap := capacity(cap(old), needed)
	ids := p.msg.ExtIds
	idLen, idCap := len(ids), cap(ids)
	if withIDs {
		if len(tail.ExtIds) > int(MaxBytes/8)-idLen-1 {
			b.refusal()
			return false
		}
		idLen += 1 + len(tail.ExtIds)
		idCap = capacity(cap(ids), idLen)
	}
	delta := int64(newCap-cap(old)) + int64(idCap-cap(ids))*8
	// This is checked before make/append so neither text nor ACK arrays can
	// transiently expand the retained graph beyond the admitted accounting.
	if !b.reserve(0, delta) {
		return false
	}
	p.charge += delta
	if newCap > cap(old) {
		buf := make([]byte, len(old), newCap)
		copy(buf, old)
		old = buf
	}
	p.msg.Message[msgKey] = append(old, text...)
	if withIDs {
		if idCap > cap(ids) {
			buf := make([]int64, len(ids), idCap)
			copy(buf, ids)
			ids = buf
		}
		ids = append(ids, tail.ID)
		ids = append(ids, tail.ExtIds...)
		p.msg.ExtIds = ids
	}
	return true
}
