//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOverallArchiveOfflineRecoveryOwnershipDryRunAndExecute(t *testing.T) {
	ctx := context.Background()
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	jc := sharedConfig(root, group)
	jc.RetainAccepted = true
	p, e := OpenOTLPJournal(ctx, jc, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 2; i++ {
		if e = p.Admit(ctx, archiveRequest(t, i)); e != nil {
			t.Fatal(e)
		}
	}
	capacityDrain(t, p)
	var calls atomic.Int64
	originals := map[string]bool{string(archiveRequest(t, 1).Payload()): false, string(archiveRequest(t, 2).Payload()): false}
	t.Setenv("SYNTHETIC_ARCHIVE_TOKEN", "synthetic-private-archive-token")
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		if _, ok := originals[string(raw)]; !ok {
			t.Error("original recovery content changed")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-private-archive-token" {
			t.Error("configured authentication missing")
		}
		originals[string(raw)] = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
	}))
	defer sink.Close()
	c := nativeStorageTestConfig(t, sink.URL, 1<<20)
	c.StorageDirectory = root
	c.Destinations[0].ID = "a"
	c.Destinations[0].LogStorageCodec = ""
	c.Destinations[0].BearerTokenEnv = "SYNTHETIC_ARCHIVE_TOKEN"
	c.RetainAccepted = true
	if _, e = RecoverOTLPAcceptedArchive(ctx, c, true); e == nil || calls.Load() != 0 {
		t.Fatal("recovery bypassed live owner", e)
	}
	// Offline recovery is read-only even with no new-admission headroom.
	snap, _ := p.budget.Snapshot(ctx)
	if e = p.budget.Reserve(ctx, snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	hashes := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		e := filepath.Walk(root, func(path string, info os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if info.Mode().IsRegular() {
				b, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				h := sha256.Sum256(b)
				rel, _ := filepath.Rel(root, path)
				out[rel] = hex.EncodeToString(h[:])
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	before := hashes()
	if n, e := RecoverOTLPAcceptedArchive(ctx, c, false); e != nil || n != 2 || calls.Load() != 0 {
		t.Fatal("dry run sent or missed archive", n, e)
	}
	if n, e := RecoverOTLPAcceptedArchive(ctx, c, true); e != nil || n != 2 || calls.Load() != 2 {
		t.Fatal("offline execute failed", n, e)
	}
	for _, seen := range originals {
		if !seen {
			t.Fatal("original missing")
		}
	}
	if !reflect.DeepEqual(before, hashes()) {
		t.Fatal("read-only recovery changed source names or bytes")
	}
	// Corrupt a synthetic packed original: validate all before sending anything.
	entries, _ := os.ReadDir(root)
	corrupted := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".pack") {
			path := filepath.Join(root, entry.Name())
			b, e := os.ReadFile(path)
			if e != nil || len(b) < 45 {
				t.Fatal("missing packed original", e)
			}
			b[44] ^= 1
			if e = os.WriteFile(path, b, 0600); e != nil {
				t.Fatal(e)
			}
			corrupted = true
			break
		}
	}
	if !corrupted {
		t.Fatal("corruption control did not touch a retained original")
	}
	_, e = RecoverOTLPAcceptedArchive(ctx, c, true)
	if e == nil || calls.Load() != 2 || strings.Contains(e.Error(), "synthetic-private-archive-token") {
		t.Fatal("corrupt evidence sent or diagnostics exposed credentials", e)
	}
}
func TestOverallArchiveRecoveryRefusesFreshRoot(t *testing.T) {
	root := sharedPrivateDir(t)
	c := OTLPServiceConfig{RetainAccepted: true, StorageDirectory: root}
	if _, e := RecoverOTLPAcceptedArchive(context.Background(), c, false); e == nil {
		t.Fatal("fresh root adopted")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("fresh recovery mutated root")
	}
}
