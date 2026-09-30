package otlpstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PruneAccepted removes only valid full-acceptance receipts at or below a
// caller-proven durable released frontier. The owner MUST first persist that
// frontier, refuse replay below it, and prevent identity reuse after restart.
// This method is not age-based deletion and never removes quarantine, uncertain
// temporary evidence, foreign namespaces, or malformed records. It returns the
// number removed; cancellation can leave a safe partially reclaimed directory.
func (s *Store) PruneAccepted(ctx context.Context, namespace string, through int64) (int, error) {
	if ctx == nil || !validText(namespace, 512) || through < 0 {
		return 0, errors.New("invalid OTLP receipt frontier")
	}
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	if err := s.failed(); err != nil {
		return 0, err
	}
	st, err := os.Lstat(s.dir)
	if err != nil {
		return 0, err
	}
	if !os.SameFile(st, s.dirInfo) {
		return 0, ErrUncertain
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	removed := 0
	for {
		if err = ctx.Err(); err != nil {
			return removed, err
		}
		names, readErr := dir.Readdirnames(256)
		for _, name := range names {
			if len(name) != 69 || !strings.HasSuffix(name, ".json") {
				continue
			}
			hash, err := hex.DecodeString(name[:64])
			if err != nil || hex.EncodeToString(hash) != name[:64] {
				continue
			}
			rec, err := s.pruneRecord(name)
			if err != nil {
				return removed, err
			}
			// A terminal receipt is the only remaining copy of quarantined payload.
			// It is deliberately retained even when the WAL's obligation is resolved.
			if rec.Outcome.Kind != Accepted || rec.Key.Journal != namespace || rec.Key.RecordID > through {
				continue
			}
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
				return removed, err
			}
			removed++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return removed, readErr
		}
	}
	if removed > 0 {
		err = s.syncDirectory(s.dirFile)
	}
	return removed, err
}

// pruneRecord validates both the receipt checksum and its hashed filename before
// allowing GC to use its namespace, identity or outcome. Reads are size bounded.
func (s *Store) pruneRecord(name string) (*entry, error) {
	path := filepath.Join(s.dir, name)
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	max := int64(s.limits.PayloadBytes+s.limits.ResponseBytes)*2 + 32*1024
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrCorrupt
	}
	var disk diskRecord
	if decodeStrict(b, &disk) != nil {
		return nil, ErrCorrupt
	}
	sum := sha256.Sum256(disk.Entry)
	if hex.EncodeToString(sum[:]) != disk.SHA256 {
		return nil, ErrCorrupt
	}
	var rec entry
	if decodeStrict(disk.Entry, &rec) != nil {
		return nil, ErrCorrupt
	}
	validVersion := (rec.Version == 1 && rec.Outcome.Kind.terminal()) || (rec.Version == 2 && rec.Outcome.Kind == Accepted)
	hash := keyHash(rec.Key)
	if !validVersion || hex.EncodeToString(hash[:])+".json" != name || s.validate(rec.Key, rec.Envelope) != nil || s.validateOutcome(rec.Outcome, rec.Envelope.Items) != nil {
		return nil, ErrCorrupt
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.RecordedAt); err != nil {
		return nil, ErrCorrupt
	}
	return &rec, nil
}
