package otlpstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneAcceptedFrontierAndNamespace(t *testing.T) {
	s := acceptedStore(t, t.TempDir())
	for _, k := range []Key{{"a", "generation", 1}, {"a", "generation", 2}, {"b", "other", 1}} {
		if _, err := s.DoDelivery(context.Background(), k, acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) { return acceptedOutcome(), nil }); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneAccepted(context.Background(), "generation", 1)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	for _, k := range []Key{{"a", "generation", 2}, {"b", "other", 1}} {
		r, err := s.DoDelivery(context.Background(), k, acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) {
			t.Fatal("GC erased unproven receipt")
			return Outcome{}, nil
		})
		if err != nil || !r.Replayed {
			t.Fatal(r, err)
		}
	}
}

func TestPruneRejectsChecksumAndFilenameCorruption(t *testing.T) {
	for _, which := range []string{"checksum", "filename"} {
		t.Run(which, func(t *testing.T) {
			dir := t.TempDir()
			s := acceptedStore(t, dir)
			k := acceptedKey()
			if _, err := s.DoDelivery(context.Background(), k, acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) { return acceptedOutcome(), nil }); err != nil {
				t.Fatal(err)
			}
			hash := keyHash(k)
			name := hex.EncodeToString(hash[:]) + ".json"
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if which == "checksum" {
				var disk diskRecord
				if err = json.Unmarshal(b, &disk); err != nil {
					t.Fatal(err)
				}
				disk.SHA256 = "invalid-checksum"
				b, err = json.Marshal(disk)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				wrong := sha256.Sum256([]byte("wrong"))
				if err = os.Rename(path, filepath.Join(dir, hex.EncodeToString(wrong[:])+".json")); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := s.PruneAccepted(context.Background(), k.Journal, k.RecordID); !errors.Is(err, ErrCorrupt) || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}

func TestPruneCancellationClosedAndTerminal(t *testing.T) {
	dir := t.TempDir()
	s := acceptedStore(t, dir)
	k := acceptedKey()
	if _, err := s.DoDelivery(context.Background(), k, acceptedEnvelope(), func(context.Context, Envelope) (Outcome, error) {
		return Outcome{Kind: Permanent, HTTPStatus: 400}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PruneAccepted(context.Background(), k.Journal, k.RecordID); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.PruneAccepted(ctx, k.Journal, k.RecordID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneAccepted(context.Background(), k.Journal, k.RecordID); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
