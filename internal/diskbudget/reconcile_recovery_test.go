//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package diskbudget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Reproduce a full retained store consuming its funded startup metadata space.
// Finishing quiescent recovery may release reservations, but cannot demand a
// new bootstrap allocation from capacity already promised to other roots.
func TestSharedBudgetFullRestartDoesNotReplenishConsumedBootstrap(t *testing.T) {
	ctx := context.Background()
	dir, root, otherRoot := budgetPrivateDir(t), budgetPrivateDir(t), budgetPrivateDir(t)
	b, err := Open(ctx, dir, root, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	other, err := Open(ctx, dir, otherRoot, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = b.Charge(ctx, 1<<20); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "accepted-original")
	f, err := os.OpenFile(original, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(make([]byte, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Sync(); err != nil {
		t.Fatal(err)
	}
	d.Close()
	snapshot, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Reserve(ctx, snapshot.Limit-snapshot.ChargedBytes-snapshot.ReservedBytes); err != nil {
		t.Fatal(err)
	}
	b.Close()
	reopened, err := Open(ctx, dir, root, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Reconcile(ctx, true); err != nil {
		t.Fatal("funded full-store restart demanded new capacity", err)
	}
	if err = reopened.Charge(ctx, 4096); err != nil {
		t.Fatal("remaining funded progress blocked", err)
	}
	if err = reopened.Admit(ctx, 4096, 4096); !errors.Is(err, ErrCapacity) {
		t.Fatal("recovery manufactured fresh admission capacity", err)
	}
	after, err := reopened.Snapshot(ctx)
	if err != nil || after.ChargedBytes+after.ReservedBytes > after.Limit {
		t.Fatal(after, err)
	}
	info, err := os.Stat(original)
	if err != nil || info.Size() != 1<<20 {
		t.Fatal("recovery altered retained original", info, err)
	}
}
