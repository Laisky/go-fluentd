//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	if capacityReceiptCount(t, root) != 2 {
		t.Fatal("recovery removed original")
	}
	// Corrupt a synthetic archived receipt: validate all before sending anything.
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if len(entry.Name()) == 69 && strings.HasSuffix(entry.Name(), ".json") {
			if e = os.WriteFile(filepath.Join(root, entry.Name()), []byte("{}"), 0600); e != nil {
				t.Fatal(e)
			}
			break
		}
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
