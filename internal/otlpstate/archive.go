package otlpstate

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// WalkAcceptedArchive reads checksum-validated complete acceptance receipts.
// It never modifies, acknowledges or prunes them. Callback must not reenter Store.
func (s *Store) WalkAcceptedArchive(ctx context.Context, namespace string, visit func(Key, Envelope) error) (int, error) {
	if ctx == nil || visit == nil || !validText(namespace, 512) {
		return 0, errors.New("invalid accepted archive reader")
	}
	s.life.RLock()
	defer s.life.RUnlock()
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
	n := 0
	for {
		if err = ctx.Err(); err != nil {
			return n, err
		}
		names, readErr := dir.Readdirnames(256)
		for _, name := range names {
			if len(name) != 69 || !strings.HasSuffix(name, ".json") {
				continue
			}
			hash, e := hex.DecodeString(name[:64])
			if e != nil || hex.EncodeToString(hash) != name[:64] {
				continue
			}
			rec, e := s.pruneRecord(name)
			if e != nil {
				return n, e
			}
			if rec.Key.Journal != namespace || rec.Outcome.Kind != Accepted {
				continue
			}
			if e = visit(rec.Key, copyEnvelope(rec.Envelope)); e != nil {
				return n, e
			}
			n++
		}
		if errors.Is(readErr, io.EOF) {
			return n, nil
		}
		if readErr != nil {
			return n, readErr
		}
	}
}
