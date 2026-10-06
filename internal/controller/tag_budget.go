package controller

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

const DefaultLegacyMaxTags = 64
const MaximumLegacyMaxTags = 4096
const MaximumLegacyTagBytes = 255

var ErrLegacyTagLimit = errors.New("legacy routing identity budget exhausted")
var ErrLegacyTagInvalid = errors.New("legacy tag must be a nonempty directory component of at most 255 bytes")

// TagBudget bounds lifetime routing identities, not merely currently open
// journals. One instance is shared by every stage of the configured process.
// Reservations survive CloseTag and failed creation: recycling a slot without
// retiring every downstream cache could otherwise bypass the lifetime limit.
// No payload or rejected identity is retained and no journal is deleted/ACKed.
type TagBudget struct {
	mu       sync.RWMutex
	limit    int
	tags     map[string]struct{}
	rejected atomic.Uint64
}

func NewTagBudget(limit int) (*TagBudget, error) {
	if limit == 0 {
		limit = DefaultLegacyMaxTags
	}
	if limit < 1 || limit > MaximumLegacyMaxTags {
		return nil, fmt.Errorf("max_tags must be between 1 and %d (zero selects %d)", MaximumLegacyMaxTags, DefaultLegacyMaxTags)
	}
	return &TagBudget{limit: limit, tags: make(map[string]struct{})}, nil
}

func validLegacyTag(tag string) bool {
	return len(tag) > 0 && len(tag) <= MaximumLegacyTagBytes && tag != "." && tag != ".." && !strings.ContainsAny(tag, "/\x00")
}

func (b *TagBudget) Admit(tag string) error {
	if !validLegacyTag(tag) {
		b.rejected.Add(1)
		return ErrLegacyTagInvalid
	}
	b.mu.RLock()
	_, ok := b.tags[tag]
	b.mu.RUnlock()
	if ok {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.tags[tag]; ok {
		return nil
	}
	if len(b.tags) >= b.limit {
		b.rejected.Add(1)
		return ErrLegacyTagLimit
	}
	b.tags[strings.Clone(tag)] = struct{}{}
	return nil
}

// admitRetained atomically reserves a bounded startup inventory before opening
// any backend. An oversized/invalid inventory cannot partly start the process.
func (b *TagBudget) admitRetained(tags []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	newTags := make(map[string]struct{})
	for _, tag := range tags {
		if !validLegacyTag(tag) {
			b.rejected.Add(1)
			return ErrLegacyTagInvalid
		}
		if _, ok := b.tags[tag]; ok {
			continue
		}
		newTags[tag] = struct{}{}
		if len(b.tags)+len(newTags) > b.limit {
			b.rejected.Add(1)
			return ErrLegacyTagLimit
		}
	}
	for tag := range newTags {
		b.tags[strings.Clone(tag)] = struct{}{}
	}
	return nil
}

func (b *TagBudget) Snapshot() (reserved, limit int, rejected uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.tags), b.limit, b.rejected.Load()
}

func defaultTagBudget(b *TagBudget) *TagBudget {
	if b != nil {
		return b
	}
	b, _ = NewTagBudget(0)
	return b
}
