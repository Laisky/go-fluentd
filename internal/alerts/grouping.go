package alerts

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"time"
)

const (
	// templateBytes bounds the synchronous hashing work per event; later text does
	// not split a group.
	templateBytes = 1024
	// fingerprintBytes is the displayed prefix of the keyed digest.
	fingerprintBytes = 6
	// variableToken stands in for a masked token in a template.
	variableToken = 0
)

// groupKey identifies one notification group in the deduplication table.
type groupKey [sha256.Size]byte

// sourceBudget is the per-window state of one allowlisted source. The table holds
// at most maxSources entries, so it stays bounded whatever producers send.
type sourceBudget struct {
	until  time.Time
	groups int
	// pending counts events withheld since the source last notified.
	pending uint64
}

type fingerprinter struct {
	mac    hash.Hash
	buffer []byte
	digest []byte
}

// newFingerprinter returns a pool constructor keyed for this process. The key is
// never persisted, so a displayed fingerprint cannot be matched against a
// dictionary of guessed messages, and it lives exactly as long as the in-memory
// group table it labels.
func newFingerprinter() func() interface{} {
	key := make([]byte, sha256.Size)
	_, _ = rand.Read(key) // documented to never fail
	return func() interface{} {
		return &fingerprinter{mac: hmac.New(sha256.New, key), buffer: make([]byte, 0, 2*templateBytes)}
	}
}

// identify computes the group of an event. The key covers everything the
// notification shows, so two notifications that look alike are the same group.
func (o *Observer) identify(e Event) (key groupKey, fingerprint [fingerprintBytes]byte) {
	f := o.fingerprints.Get().(*fingerprinter)
	defer o.fingerprints.Put(f)
	f.buffer = f.buffer[:0]
	if e.Message != "" {
		f.buffer = appendTemplate(f.buffer, e.Message)
		f.mac.Reset()
		f.mac.Write(f.buffer)
		f.digest = f.mac.Sum(f.digest[:0])
		copy(fingerprint[:], f.digest)
		f.buffer = append(f.buffer[:0], f.digest...)
	}
	f.buffer = append(f.buffer, 0)
	f.buffer = append(f.buffer, e.Source...)
	f.buffer = append(f.buffer, 0)
	f.buffer = append(f.buffer, e.Severity...)
	f.buffer = append(f.buffer, 0)
	f.buffer = append(f.buffer, e.OriginalSeverity...)
	return sha256.Sum256(f.buffer), fingerprint
}

// appendTemplate reduces a message to its error kind: ASCII case is folded and
// every token containing a digit becomes one placeholder, so request ids,
// durations, addresses and UUIDs do not split a group. Dots, colons and slashes
// separate tokens to keep file names and paths significant, and non-ASCII text is
// kept verbatim. This is the usual log-template masking, deliberately simple
// because it runs on the dispatcher path.
func appendTemplate(dst []byte, message string) []byte {
	if len(message) > templateBytes {
		message = message[:templateBytes]
	}
	for i := 0; i < len(message); {
		if !isTokenByte(message[i]) {
			dst = append(dst, message[i])
			i++
			continue
		}
		start, variable := i, false
		for ; i < len(message) && isTokenByte(message[i]); i++ {
			variable = variable || (message[i] >= '0' && message[i] <= '9')
		}
		if variable {
			dst = append(dst, variableToken)
			continue
		}
		for j := start; j < i; j++ {
			c := message[j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			dst = append(dst, c)
		}
	}
	return dst
}

func isTokenByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}

// admit applies the group window and source budget, then queues the notification
// without waiting. Withheld events are charged to their source and reported by
// its next notification or summary.
func (o *Observer) admit(e Event, key groupKey, fingerprint [fingerprintBytes]byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if e.ObservedAt.IsZero() {
		e.ObservedAt = now.UTC()
	}
	budget := o.sourceBudget(e.origin, now)
	if until, ok := o.seen[key]; ok && now.Before(until) {
		budget.pending++
		o.suppressed.Add(1)
		return
	}
	if budget.groups >= o.cfg.MaxGroupsPerSource {
		budget.pending++
		o.sourceLimited.Add(1)
		return
	}
	if fingerprint != ([fingerprintBytes]byte{}) {
		e.Fingerprint = hex.EncodeToString(fingerprint[:])
	}
	e.Suppressed = budget.pending
	select {
	case o.queue <- e:
	default:
		// Nothing was reported, so the group and its pending count stay open.
		o.dropped.Add(1)
		o.droppedEvents.Add(1)
		return
	}
	budget.groups++
	budget.pending = 0
	o.remember(key, now.Add(o.cfg.DedupWindow))
	o.queued.Add(1)
}

// sourceBudget returns the current window of a source, opening a new one when the
// previous window has ended. Pending counts survive until they are reported.
func (o *Observer) sourceBudget(origin string, now time.Time) *sourceBudget {
	budget, ok := o.sources[origin]
	if !ok {
		if len(o.sources) >= maxSources {
			for key := range o.sources {
				delete(o.sources, key)
				break
			}
		}
		budget = &sourceBudget{}
		o.sources[origin] = budget
	}
	if !now.Before(budget.until) {
		budget.until = now.Add(o.cfg.DedupWindow)
		budget.groups = 0
	}
	return budget
}

// remember records a group, evicting one arbitrary entry when full so that new
// groups are never blocked and the table needs no scan.
func (o *Observer) remember(key groupKey, until time.Time) {
	if _, ok := o.seen[key]; !ok && len(o.seen) >= o.cfg.DedupEntries {
		for old := range o.seen {
			delete(o.seen, old)
			o.evicted.Add(1)
			break
		}
	}
	o.seen[key] = until
}

// flushSummaries reports sources whose window ended with withheld events, so a
// burst followed by silence is not forgotten. A source that keeps failing reports
// through its next notification instead. A full queue keeps the count for the
// next pass; it costs at most one slot per selected source.
func (o *Observer) flushSummaries(now time.Time) {
	o.lifecycle.RLock()
	defer o.lifecycle.RUnlock()
	if o.stopped {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for origin, budget := range o.sources {
		if budget.pending == 0 || now.Before(budget.until) {
			continue
		}
		select {
		case o.queue <- Event{Source: origin, Suppressed: budget.pending, ObservedAt: now.UTC(), summary: true}:
			budget.pending = 0
			o.queued.Add(1)
		default:
			return
		}
	}
}
