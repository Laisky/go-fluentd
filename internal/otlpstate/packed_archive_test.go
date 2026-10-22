//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package otlpstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func packedFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := Open(dir, Limits{PayloadBytes: 8192, ResponseBytes: 512})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}
func addPackedReceipt(t *testing.T, s *Store, id int64, namespace string, kind Kind) {
	t.Helper()
	_, e := s.DoDelivery(t.Context(), Key{Destination: "home", Journal: namespace, RecordID: id}, Envelope{Signal: "logs", ContentType: "application/json", Items: 1, Payload: []byte(fmt.Sprintf(`{"original":%d,"unicode":"中文"}`, id))}, func(context.Context, Envelope) (Outcome, error) {
		status := 200
		if kind == Permanent {
			status = 400
		}
		return Outcome{Kind: kind, HTTPStatus: status}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func countPacked(t *testing.T, s *Store, ns string) int {
	t.Helper()
	n, e := s.WalkAcceptedArchive(t.Context(), ns, func(k Key, v Envelope) error {
		if !strings.Contains(string(v.Payload), fmt.Sprintf(`"original":%d`, k.RecordID)) {
			return ErrConflict
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return n
}
func TestAcceptedPackingPreservesOriginalsAndProtectedRecords(t *testing.T) {
	s, dir := packedFixture(t)
	for i := int64(1); i <= 80; i++ {
		addPackedReceipt(t, s, i, "ns", Accepted)
	}
	addPackedReceipt(t, s, 100, "ns", Accepted)
	addPackedReceipt(t, s, 101, "ns", Permanent)
	addPackedReceipt(t, s, 1, "foreign", Accepted)
	protected := map[string][]byte{}
	for _, k := range []Key{{"home", "ns", 100}, {"home", "ns", 101}, {"home", "foreign", 1}} {
		h := keyHash(k)
		n := hex.EncodeToString(h[:]) + ".json"
		b, e := os.ReadFile(filepath.Join(dir, n))
		if e != nil {
			t.Fatal(e)
		}
		protected[n] = b
	}
	total := 0
	for i := 0; i < 4; i++ {
		n, e := s.CompactAccepted(t.Context(), "ns", 80)
		if e != nil {
			t.Fatal(e)
		}
		total += n
	}
	if total != 80 || countPacked(t, s, "ns") != 81 {
		t.Fatal("packing lost originals", total)
	}
	for name, want := range protected {
		have, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil || string(have) != string(want) {
			t.Fatal("protected original changed", name, e)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) > 10 {
		t.Fatal("one-file-per-receipt ceiling persists", len(entries))
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	q, e := Open(dir, s.limits)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	if countPacked(t, q, "ns") != 81 {
		t.Fatal("cold reopen lost original")
	}
}
func TestAcceptedPackingSyncFailureRetainsLooseOriginal(t *testing.T) {
	for _, boundary := range []string{"part-sync", "index-stage-sync", "index-directory-sync"} {
		t.Run(boundary, func(t *testing.T) {
			s, dir := packedFixture(t)
			addPackedReceipt(t, s, 1, "ns", Accepted)
			recordSync, dirSync := s.syncRecord, s.syncDirectory
			indexWrites, dirs := 0, 0
			s.syncRecord = func(f *os.File) error {
				if strings.HasSuffix(f.Name(), ".pack") && boundary == "part-sync" {
					st, _ := f.Stat()
					if st.Size() > 0 {
						return errors.New("synthetic part sync failure")
					}
				}
				if strings.HasPrefix(filepath.Base(f.Name()), ".accepted-index-") {
					indexWrites++
					if boundary == "index-stage-sync" && indexWrites == 2 {
						return errors.New("synthetic index sync failure")
					}
				}
				return recordSync(f)
			}
			s.syncDirectory = func(f *os.File) error {
				dirs++
				if boundary == "index-directory-sync" && dirs == 3 {
					return errors.New("synthetic directory sync failure")
				}
				return dirSync(f)
			}
			if _, e := s.CompactAccepted(t.Context(), "ns", 1); e == nil {
				t.Fatal("failed sync acknowledged compaction")
			}
			h := keyHash(Key{"home", "ns", 1})
			original := filepath.Join(dir, hex.EncodeToString(h[:])+".json")
			if _, e := os.Stat(original); e != nil {
				t.Fatal("original discarded before durable copy", e)
			}
			s.Close()
			q, e := Open(dir, s.limits)
			if e != nil {
				t.Fatal("valid commit prefix could not reopen", e)
			}
			defer q.Close()
			if _, e = q.CompactAccepted(t.Context(), "ns", 1); e != nil {
				t.Fatal(e)
			}
			if countPacked(t, q, "ns") != 1 {
				t.Fatal("recovery promoted tail or lost original")
			}
		})
	}
}
func TestAcceptedPackingMissingOrCorruptAuthorityRefuses(t *testing.T) {
	for _, mode := range []string{"missing-index", "corrupt-index", "truncated-committed", "foreign-stage", "stage-symlink", "stage-hardlink", "stage-mode"} {
		t.Run(mode, func(t *testing.T) {
			s, dir := packedFixture(t)
			addPackedReceipt(t, s, 1, "ns", Accepted)
			if _, e := s.CompactAccepted(t.Context(), "ns", 1); e != nil {
				t.Fatal(e)
			}
			s.Close()
			switch mode {
			case "missing-index":
				os.Remove(filepath.Join(dir, acceptedName(1, "index.json")))
			case "corrupt-index":
				os.WriteFile(filepath.Join(dir, acceptedName(1, "index.json")), []byte(`{}`), 0600)
			case "truncated-committed":
				os.Truncate(filepath.Join(dir, acceptedName(1, "pack")), 1)
			case "foreign-stage":
				os.WriteFile(filepath.Join(dir, ".accepted-index-not-canonical"), nil, 0600)
			case "stage-symlink":
				os.Symlink(filepath.Join(dir, acceptedName(1, "pack")), filepath.Join(dir, ".accepted-index-1"))
			case "stage-hardlink":
				os.Link(filepath.Join(dir, acceptedName(1, "pack")), filepath.Join(dir, ".accepted-index-1"))
			case "stage-mode":
				os.WriteFile(filepath.Join(dir, ".accepted-index-1"), nil, 0644)
			}
			before := packedHashes(t, dir)
			if q, e := Open(dir, s.limits); e == nil {
				q.Close()
				t.Fatal("unsafe authority accepted", mode)
			}
			after := packedHashes(t, dir)
			if fmt.Sprint(before) != fmt.Sprint(after) {
				t.Fatal("refusal changed evidence")
			}
		})
	}
}
func packedHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	for _, row := range rows {
		info, e := row.Info()
		if e != nil {
			t.Fatal(e)
		}
		if info.Mode().IsRegular() {
			b, e := os.ReadFile(filepath.Join(dir, row.Name()))
			if e != nil {
				t.Fatal(e)
			}
			h := sha256.Sum256(b)
			out[row.Name()] = hex.EncodeToString(h[:])
		}
	}
	return out
}
func TestAcceptedPackingCrashHelper(t *testing.T) {
	if os.Getenv("OTLP_PACK_CRASH_CHILD") != "1" {
		return
	}
	dir := os.Getenv("OTLP_PACK_CRASH_ROOT")
	s, e := Open(dir, Limits{8192, 512})
	if e != nil {
		t.Fatal(e)
	}
	addPackedReceipt(t, s, 1, "ns", Accepted)
	original := s.syncRecord
	indexes := 0
	s.syncRecord = func(f *os.File) error {
		if strings.HasPrefix(filepath.Base(f.Name()), ".accepted-index-") {
			indexes++
			if indexes == 2 {
				syscall.Kill(os.Getpid(), syscall.SIGKILL)
			}
		}
		return original(f)
	}
	s.CompactAccepted(t.Context(), "ns", 1)
	t.Fatal("SIGKILL boundary missed")
}
func TestAcceptedPackingRepeatedSIGKILLDoesNotPromoteTail(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 8; i++ {
		child := exec.Command(os.Args[0], "-test.run=^TestAcceptedPackingCrashHelper$")
		child.Env = append(os.Environ(), "OTLP_PACK_CRASH_CHILD=1", "OTLP_PACK_CRASH_ROOT="+dir)
		if e := child.Run(); e == nil {
			t.Fatal("crash child survived")
		}
		ws, ok := child.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatal("not the intended crash", child.ProcessState)
		}
	}
	before := packedHashes(t, dir)
	s, e := Open(dir, Limits{8192, 512})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.CompactAccepted(t.Context(), "ns", 1); e != nil {
		t.Fatal(e)
	}
	if countPacked(t, s, "ns") != 1 {
		t.Fatal("uncommitted crash tail became authority")
	}
	after := packedHashes(t, dir)
	for name, hash := range before {
		if strings.HasPrefix(name, "accepted-pack-") || strings.HasPrefix(name, ".accepted-index-") {
			if after[name] != hash {
				t.Fatal("crash evidence altered", name)
			}
		}
	}
	t.Log("eight real SIGKILL cycles: committed prefixes recovered, uncommitted tails/stages unchanged, exact original retained")
}
func TestAcceptedPackingBoundsCrashMetadata(t *testing.T) {
	s, dir := packedFixture(t)
	s.Close()
	for i := 0; i < 128; i++ {
		if e := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".accepted-index-%d", i)), []byte("partial"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	q, e := Open(dir, s.limits)
	if e != nil {
		t.Fatal("bounded retained stages falsely refused", e)
	}
	q.Close()
	if e = os.WriteFile(filepath.Join(dir, ".accepted-index-128"), []byte("partial"), 0600); e != nil {
		t.Fatal(e)
	}
	before := packedHashes(t, dir)
	if q, e = Open(dir, s.limits); e == nil {
		q.Close()
		t.Fatal("unbounded stage accumulation admitted")
	}
	if fmt.Sprint(before) != fmt.Sprint(packedHashes(t, dir)) {
		t.Fatal("metadata exhaustion deleted evidence")
	}
}
func TestAcceptedPackingMissingActivePrimaryCannotAppend(t *testing.T) {
	s, dir := packedFixture(t)
	addPackedReceipt(t, s, 1, "ns", Accepted)
	if _, e := s.CompactAccepted(t.Context(), "ns", 1); e != nil {
		t.Fatal(e)
	}
	addPackedReceipt(t, s, 2, "ns", Accepted)
	if e := os.Remove(filepath.Join(dir, acceptedName(1, "index.json"))); e != nil {
		t.Fatal(e)
	}
	before := packedHashes(t, dir)
	if _, e := s.CompactAccepted(t.Context(), "ns", 2); e == nil {
		t.Fatal("missing primary repaired implicitly")
	}
	if fmt.Sprint(before) != fmt.Sprint(packedHashes(t, dir)) {
		t.Fatal("missing-primary refusal changed original data")
	}
}
