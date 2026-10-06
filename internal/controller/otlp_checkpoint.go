package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// otlpGeneration version 2 makes the released frontier authoritative. Old
// binaries reject it instead of replaying receipts that have been reclaimed.
type otlpGeneration struct {
	Version         int    `json:"version"`
	Namespace       string `json:"namespace"`
	ReleasedThrough int64  `json:"released_through,omitempty"`
	Checksum        string `json:"checksum,omitempty"`
}

func (g otlpGeneration) checksum() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d", g.Version, g.Namespace, g.ReleasedThrough)))
	return hex.EncodeToString(sum[:])
}

func openOTLPGeneration(dir string) (otlpGeneration, string, error) {
	path, wal := filepath.Join(dir, "generation.json"), filepath.Join(dir, "wal")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return otlpGeneration{}, "", e
		}
		for _, item := range entries {
			if item.Name() != ".otlp-disposition.lock" {
				return otlpGeneration{}, "", errors.New("OTLP storage has no generation metadata; refusing to adopt existing data")
			}
		}
		var seed [32]byte
		if _, e = rand.Read(seed[:]); e != nil {
			return otlpGeneration{}, "", e
		}
		g := otlpGeneration{Version: 1, Namespace: hex.EncodeToString(seed[:])}
		if e = os.Mkdir(wal, 0700); e != nil {
			return otlpGeneration{}, "", e
		}
		b, e := json.Marshal(g)
		if e != nil {
			return otlpGeneration{}, "", e
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return otlpGeneration{}, "", e
		}
		n, e := f.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
		if e == nil {
			var df *os.File
			df, e = os.Open(dir)
			if e == nil {
				e = errors.Join(df.Sync(), df.Close())
			}
		}
		if e != nil {
			return otlpGeneration{}, "", e
		} // Leave evidence on failure; never reset identity.
		return g, wal, nil
	}
	if err != nil {
		return otlpGeneration{}, "", err
	}
	if !st.Mode().IsRegular() || st.Size() > 1024 {
		return otlpGeneration{}, "", errors.New("invalid OTLP generation file")
	}
	f, err := os.Open(path)
	if err != nil {
		return otlpGeneration{}, "", err
	}
	defer f.Close()
	var g otlpGeneration
	d := json.NewDecoder(io.LimitReader(f, 1025))
	d.DisallowUnknownFields()
	if err = d.Decode(&g); err != nil {
		return otlpGeneration{}, "", err
	}
	var extra interface{}
	seed, e := hex.DecodeString(g.Namespace)
	if d.Decode(&extra) != io.EOF || (g.Version != 1 && g.Version != 2) || e != nil || len(seed) != 32 || hex.EncodeToString(seed) != g.Namespace {
		return otlpGeneration{}, "", errors.New("invalid OTLP generation metadata")
	}
	if (g.Version == 1 && (g.ReleasedThrough != 0 || g.Checksum != "")) ||
		(g.Version == 2 && (g.ReleasedThrough < 0 || g.Checksum != g.checksum())) {
		return otlpGeneration{}, "", errors.New("invalid OTLP released-frontier checksum")
	}
	st, err = os.Lstat(wal)
	if err != nil {
		return otlpGeneration{}, "", err
	}
	if !st.IsDir() {
		return otlpGeneration{}, "", errors.New("OTLP WAL must be a dedicated non-symlink directory")
	}
	if err = f.Sync(); err != nil {
		return otlpGeneration{}, "", err
	}
	return g, wal, nil
}

// checkpointAndPrune runs only at frozen snapshot EOF with walGate and passGate
// held. Pending IDs cap the frontier; concurrent newer admissions never enter it.
// Every skipped or released ID is backed by a synchronized ACK or older marker.
func (p *OTLPJournal) checkpointAndPrune(ctx context.Context, cutoff int64) error {
	if !p.cfg.ReceiptGC || cutoff < 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.generation.Version != 2 || cutoff > p.generation.ReleasedThrough {
		next := otlpGeneration{Version: 2, Namespace: p.namespace, ReleasedThrough: cutoff}
		next.Checksum = next.checksum()
		if err := persistOTLPCheckpoint(p.cfg.Directory, next); err != nil {
			return err
		}
		p.generation = next
	}
	n, err := p.store.PruneAccepted(ctx, p.namespace, p.generation.ReleasedThrough)
	p.receiptsReclaimed.Add(uint64(n))
	return err
}

// persistOTLPCheckpoint publishes a checksummed frontier before any receipt is
// removed. Interrupted writes retain evidence. It never changes the namespace.
func persistOTLPCheckpoint(dir string, g otlpGeneration) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".checkpoint-*")
	if err != nil {
		return err
	}
	n, err := f.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "generation.json")); err != nil {
		return err
	}
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(df.Sync(), df.Close())
}

// otlpDirectoryBytes accounts regular-file names throughout the owned root,
// including receipts, WAL/recovery files and incomplete evidence. Hard links
// count twice conservatively. This is an admission check, not a filesystem quota:
// replay and receipt writes need separate headroom and are never discarded to fit.
// Once stopAfter is exceeded, the caller already must refuse admission, so it
// returns a lower bound without scanning the rest of a full ownership directory.
func otlpDirectoryBytes(ctx context.Context, root string, stopAfter int64, maxEntries int) (int64, error) {
	size, _, err := otlpDirectoryUsage(ctx, root, stopAfter, maxEntries)
	return size, err
}

func otlpDirectoryUsage(ctx context.Context, root string, stopAfter int64, maxEntries int) (int64, int64, error) {
	var total int64
	seen := 0
	full := errors.New("OTLP storage scan admission threshold exceeded")
	var walk func(string) error
	walk = func(dir string) error {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer f.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, readErr := f.ReadDir(256)
			for _, item := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				if seen == maxEntries {
					return ErrOTLPJournalScanBudget
				}
				seen++
				info, err := item.Info()
				// A replay can remove completed files while admission inspects storage.
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return err
				}
				if info.IsDir() {
					if err := walk(filepath.Join(dir, item.Name())); err != nil {
						return err
					}
				} else if info.Mode().IsRegular() {
					if info.Size() < 0 || info.Size() > (1<<62)-total {
						return errors.New("OTLP storage size overflow")
					}
					total += info.Size()
					if total > stopAfter {
						return full
					}
				} else {
					return fmt.Errorf("unexpected OTLP storage entry %q", item.Name())
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	if err := walk(root); err != nil && !errors.Is(err, full) {
		return 0, 0, err
	}
	return total, int64(seen), nil
}
