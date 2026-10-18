//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package diskbudget

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSharedBudgetBoundaryAndFalsePositive(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, dir, root, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	a, e := b.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	room := a.Limit - a.ChargedBytes - a.ReservedBytes
	if e = b.Reserve(ctx, room); e != nil {
		t.Fatal("exact boundary rejected", e)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
	if e = b.Reserve(ctx, 1); !errors.Is(e, ErrCapacity) {
		t.Fatal("over cap accepted", e)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
	if string(before) != string(after) {
		t.Fatal("refusal consumed capacity")
	}
	if e = b.Charge(ctx, 4096); e != nil {
		t.Fatal("funded recovery blocked", e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = b.Reserve(cancelled, 0); !errors.Is(e, context.Canceled) {
		t.Fatal("cancellation ignored", e)
	}
}
func TestSharedBudgetConcurrentOwnersAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := budgetPrivateDir(t)
	roots := []string{budgetPrivateDir(t), budgetPrivateDir(t)}
	bs := make([]*Budget, 2)
	for i := range bs {
		var e error
		bs[i], e = Open(ctx, dir, roots[i], 16<<20)
		if e != nil {
			t.Fatal(e)
		}
		defer bs[i].Close()
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e := bs[i%2].Charge(ctx, 32768)
				if e != nil && !errors.Is(e, ErrCapacity) {
					t.Error(e)
				}
			}
		}(i)
	}
	wg.Wait()
	s, e := bs[0].Snapshot(ctx)
	if e != nil || s.ChargedBytes+s.ReservedBytes > s.Limit {
		t.Fatal(s, e)
	}
	bs[0].Close()
	q, e := Open(ctx, dir, roots[0], 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer q.Close()
	s2, _ := q.Snapshot(ctx)
	if s2 != s {
		t.Fatal("restart forgave charged growth", s, s2)
	}
}
func TestSharedBudgetExistingOverCapAndForeignEvidence(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	f, e := os.Create(filepath.Join(root, "accepted-wal"))
	if e != nil {
		t.Fatal(e)
	}
	f.Truncate(3000000001)
	f.Close()
	if _, e = Open(ctx, dir, root, DefaultLimitBytes); !errors.Is(e, ErrCapacity) {
		t.Fatal("existing over-cap queue adopted", e)
	}
	st, e := os.Stat(filepath.Join(root, "accepted-wal"))
	if e != nil || st.Size() != 3000000001 {
		t.Fatal("existing evidence changed", e)
	}
	root2 := budgetPrivateDir(t)
	os.WriteFile(filepath.Join(root2, "legacy-queue"), []byte("accepted"), 0600)
	if _, e = Open(ctx, dir, root2, DefaultLimitBytes); !errors.Is(e, ErrMigration) {
		t.Fatal("legacy migration silently adopted", e)
	}
	root3 := budgetPrivateDir(t)
	b, e := Open(ctx, dir, root3, DefaultLimitBytes)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	os.WriteFile(filepath.Join(dir, ".budget-pending-crash"), []byte("uncertain"), 0600)
	if e = b.Charge(ctx, 1); e == nil {
		t.Fatal("uncertain ledger evidence ignored")
	}
}
func TestSharedBudgetProcessHelper(t *testing.T) {
	if os.Getenv("OTLP_BUDGET_CHILD") != "1" {
		return
	}
	b, e := Open(context.Background(), os.Getenv("OTLP_BUDGET_DIR"), os.Getenv("OTLP_BUDGET_ROOT"), 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	for i := 0; i < 120; i++ {
		e = b.Charge(context.Background(), 65536)
		if e != nil && !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
	}
}
func TestSharedBudgetIndependentProcesses(t *testing.T) {
	dir := budgetPrivateDir(t)
	roots := []string{budgetPrivateDir(t), budgetPrivateDir(t)}
	cmds := make([]*exec.Cmd, 2)
	for i := range cmds {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestSharedBudgetProcessHelper$")
		cmds[i].Env = append(os.Environ(), "OTLP_BUDGET_CHILD=1", "OTLP_BUDGET_DIR="+dir, "OTLP_BUDGET_ROOT="+roots[i])
		if e := cmds[i].Start(); e != nil {
			t.Fatal(e)
		}
	}
	for _, c := range cmds {
		if e := c.Wait(); e != nil {
			t.Fatal(e)
		}
	}
	b, e := Open(context.Background(), dir, roots[0], 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	s, e := b.Snapshot(context.Background())
	if e != nil || s.ChargedBytes+s.ReservedBytes > s.Limit {
		t.Fatal(s, e)
	}
}
func TestSharedBudgetRootOwnershipAndLimitMismatch(t *testing.T) {
	ctx := context.Background()
	d, r := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, d, r, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if _, e = Open(ctx, d, r, 8<<20); e == nil {
		t.Fatal("duplicate writer owned root")
	}
	if _, e = Open(ctx, d, budgetPrivateDir(t), 9<<20); e == nil {
		t.Fatal("different aggregate limits mixed")
	}
	nested := filepath.Join(r, "nested")
	os.Mkdir(nested, 0700)
	if _, e = Open(ctx, d, nested, 8<<20); e == nil {
		t.Fatal("overlapping accounting roots")
	}
}
func TestSharedBudgetReconcileCountsBlocksDirectoriesAndUncertainTemps(t *testing.T) {
	ctx := context.Background()
	d, r := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, d, r, 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = b.Charge(ctx, 1<<20); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(r, ".pending-retained")
	os.WriteFile(p, []byte("x"), 0600)
	os.Mkdir(filepath.Join(r, "index"), 0700)
	if e = b.Reconcile(ctx, true); e != nil {
		t.Fatal(e)
	}
	s, e := b.Snapshot(ctx)
	if e != nil || s.ChargedBytes < ledgerAllowance+3*4096 {
		t.Fatal("blocks/directories excluded", s, e)
	}
	// A canceled reconcile never refunds a claim.
	before, _ := b.Snapshot(ctx)
	c, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if e = b.Reconcile(c, true); e == nil {
		t.Fatal("canceled scan accepted")
	}
	after, _ := b.Snapshot(ctx)
	if before != after {
		t.Fatal("partial inventory refunded capacity")
	}
}

func budgetPrivateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestSharedBudgetFailedLedgerWriteStopsAndRemainsUnknown(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, dir, root, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	before, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
	if e = os.Chmod(dir, 0500); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if e = b.Charge(ctx, 4096); e == nil {
		t.Fatal("failed ledger publication accepted")
	}
	os.Chmod(dir, 0700)
	after, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
	if string(before) != string(after) {
		t.Fatal("failed publication changed grant")
	}
	if e = b.Charge(ctx, 1); e == nil {
		t.Fatal("uncertain writer continued")
	}
	if _, e = b.Snapshot(ctx); e == nil {
		t.Fatal("uncertain persistence reported healthy")
	}
}

func TestSharedBudgetReplacementCannotResetAccounting(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, dir, root, 8<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	retained := root + "-retained"
	if e = os.Rename(root, retained); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if e = b.Charge(ctx, 1); e == nil {
		t.Fatal("replacement ownership domain received a grant")
	}
	if _, e = b.Snapshot(ctx); e == nil {
		t.Fatal("replaced root reported healthy")
	}
	if _, e = os.Stat(filepath.Join(retained, ".go-fluentd-disk-owner.lock")); e != nil {
		t.Fatal("retained old ownership evidence lost", e)
	}
}
