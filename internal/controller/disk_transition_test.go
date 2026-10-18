//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"errors"
	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/diskbudget"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func copyTransitionFixture(t *testing.T, source, target string) {
	t.Helper()
	entries, e := os.ReadDir(source)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			t.Fatal("nonregular archive entry", e)
		}
		from, e := os.Open(filepath.Join(source, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		to, e := os.OpenFile(filepath.Join(target, entry.Name()), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, copyErr := io.Copy(to, from)
		syncErr := to.Sync()
		closeErr := to.Close()
		from.Close()
		if e = errors.Join(copyErr, syncErr, closeErr); e != nil {
			t.Fatal(e)
		}
	}
	dir, e := os.Open(target)
	if e != nil {
		t.Fatal(e)
	}
	e = errors.Join(dir.Sync(), dir.Close())
	if e != nil {
		t.Fatal(e)
	}
}
func TestOverallTransitionOverCapNeedsSeparateStorageAndPreservesEveryOriginal(t *testing.T) {
	source := legacyRecoverySource(t, true, "original archive after acknowledgement")
	// Scale the observed 3.709GB/3GB relationship without touching live state.
	filler, e := os.OpenFile(filepath.Join(source, "unknown-retained-evidence"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte(i*31 + 7)
	}
	for i := 0; i < 10; i++ {
		if _, e = filler.Write(block); e != nil {
			t.Fatal(e)
		}
	}
	filler.Sync()
	filler.Close()
	before := legacySourceHashes(t, source)
	coordinator := sharedPrivateDir(t)
	if b, e := diskbudget.Open(t.Context(), coordinator, source, 8<<20); e == nil {
		b.Close()
		t.Fatal("over-cap unregistered state adopted")
	}
	// The owner-lock inode may be added by the refusal; every pre-existing byte
	// must still match. It is not evidence that any payload became eligible GC.
	after := legacySourceHashes(t, source)
	for name, sum := range before {
		if after[name] != sum {
			t.Fatal("refusal changed an original", name)
		}
	}
	// Model separately provisioned archive storage. This is a safe cold-copy
	// qualification, NOT proof that old b1 space has been reclaimed or excluded.
	target := sharedPrivateDir(t)
	copyTransitionFixture(t, source, target)
	copied := legacySourceHashes(t, target)
	if !reflect.DeepEqual(after, copied) {
		t.Fatal("cold archive omitted bytes or evidence")
	}
	root, e := os.OpenRoot(target)
	if e != nil {
		t.Fatal(e)
	}
	archive, e := journal.OpenClosedRecoveryArchive(root, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	n, e := archive.WalkRaw(t.Context(), func(d *journal.Data, raw []byte) error {
		if d.ID != 7 || len(raw) == 0 {
			t.Fatal("restored acknowledged original missing")
		}
		return nil
	})
	archive.Close()
	root.Close()
	if e != nil || n != 1 {
		t.Fatal("cold-copy original not recoverable", n, e)
	}
	if !reflect.DeepEqual(after, legacySourceHashes(t, source)) {
		t.Fatal("copy retired original source")
	}
	// A corrupt copy must fail verification; it cannot authorize old-state retirement.
	path := filepath.Join(target, "unknown-retained-evidence")
	fp, e := os.OpenFile(path, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	fp.WriteAt([]byte{255}, 0)
	fp.Close()

	if reflect.DeepEqual(after, legacySourceHashes(t, target)) {
		t.Fatal("corruption falsely verified")
	}
}
