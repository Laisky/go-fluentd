//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package diskbudget

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The child stops in the same locked pre-rename state as an interrupted ledger
// write. Its staged bytes are deliberately partial or would forgive claims;
// neither may become the source of truth after SIGKILL.
func TestSharedBudgetStagedCrashHelper(t *testing.T) {
	if os.Getenv("STAGED_BUDGET_CHILD") != "1" {
		return
	}
	ctx := context.Background()
	b, err := Open(ctx, os.Getenv("STAGED_BUDGET_DIR"), os.Getenv("STAGED_BUDGET_ROOT"), 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	locked, err := lock(ctx, filepath.Join(b.dir, ".budget.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	l, err := b.read()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STAGED_BUDGET_MODE") == "refund" {
		for key, c := range l.Roots {
			c.Bytes = 0
			c.Reserve = 0
			l.Roots[key] = c
		}
	}
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("STAGED_BUDGET_MODE") != "refund" {
		raw = raw[:len(raw)/2]
	}
	f, err := os.CreateTemp(b.dir, ".budget-pending-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := os.Open(b.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Sync(); err != nil {
		t.Fatal(err)
	}
	d.Close()
	fmt.Fprintln(os.Stdout, f.Name())
	select {}
}

func TestSharedBudgetRepeatedStagedCrashesPreserveCommittedCredit(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, err := Open(ctx, dir, root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("accepted synthetic original")
	if err = b.Admit(ctx, 4096, 64<<10); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "original"), original, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Reserve(ctx, snapshot.Limit-snapshot.ChargedBytes-snapshot.ReservedBytes); err != nil {
		t.Fatal(err)
	}
	before, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	retained := map[string][]byte{}
	for cycle := 0; cycle < 8; cycle++ {
		mode := "partial"
		if cycle%2 == 1 {
			mode = "refund"
		}
		childCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestSharedBudgetStagedCrashHelper$")
		cmd.Env = []string{"GOMAXPROCS=2", "STAGED_BUDGET_CHILD=1", "STAGED_BUDGET_DIR=" + dir, "STAGED_BUDGET_ROOT=" + root, "STAGED_BUDGET_MODE=" + mode}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			cancel()
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- strings.TrimSpace(line) }()
		var path string
		select {
		case path = <-ready:
		case <-childCtx.Done():
			cmd.Wait()
			cancel()
			t.Fatal("staged child did not reach pre-rename state", stderr.String())
		}
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), ".budget-pending-") {
			cmd.Process.Kill()
			cmd.Wait()
			cancel()
			t.Fatal("missing staged handshake", path, stderr.String())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			cancel()
			t.Fatal(err)
		}
		retained[path] = raw
		if err = cmd.Process.Kill(); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err = cmd.Wait(); err == nil {
			cancel()
			t.Fatal("child was not killed")
		}
		cancel()
		q, err := Open(ctx, dir, root, 8<<20)
		if err != nil {
			t.Fatal("committed ledger blocked by abandoned pre-rename intent", cycle, err)
		}
		after, err := q.Snapshot(ctx)
		if err != nil || after != before {
			q.Close()
			t.Fatal("staged intent forgave or invented credit", cycle, before, after, err)
		}
		if err = q.Admit(ctx, 1, 1); !errors.Is(err, ErrCapacity) {
			q.Close()
			t.Fatal("staged refund admitted unfunded new work", cycle, err)
		}
		for file, want := range retained {
			got, e := os.ReadFile(file)
			if e != nil || !bytes.Equal(got, want) {
				q.Close()
				t.Fatal("staged evidence altered", file, e)
			}
		}
		got, e := os.ReadFile(filepath.Join(root, "original"))
		if e != nil || !bytes.Equal(got, original) {
			q.Close()
			t.Fatal("accepted original changed", e)
		}
		if cycle == 7 {
			if err = q.Charge(ctx, 4096); err != nil {
				q.Close()
				t.Fatal("funded completion blocked after crashes", err)
			}
			s, e := q.Snapshot(ctx)
			if e != nil || s.ChargedBytes+s.ReservedBytes > s.Limit {
				q.Close()
				t.Fatal(s, e)
			}
		}
		q.Close()
	}
	t.Logf("SIGKILL cycles=8 preserved_stages=%d committed_credit_unchanged=true", len(retained))
}

func TestSharedBudgetStagedRecoveryStillRejectsForeignAndInvalidPrimary(t *testing.T) {
	for _, kind := range []string{"foreign-name", "non-numeric-stage", "stage-out-of-range", "stage-noncanonical", "stage-symlink", "stage-hardlink", "stage-mode", "stage-oversize", "missing-primary", "corrupt-primary"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
			b, err := Open(ctx, dir, root, 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			stage := filepath.Join(dir, ".budget-pending-1234567890")
			switch kind {
			case "foreign-name":
				stage = filepath.Join(dir, "unrelated-evidence")
				err = os.WriteFile(stage, []byte("{}"), 0600)
			case "non-numeric-stage":
				stage = filepath.Join(dir, ".budget-pending-crash")
				err = os.WriteFile(stage, []byte("{}"), 0600)
			case "stage-out-of-range":
				stage = filepath.Join(dir, ".budget-pending-4294967296")
				err = os.WriteFile(stage, []byte("{}"), 0600)
			case "stage-noncanonical":
				stage = filepath.Join(dir, ".budget-pending-0001")
				err = os.WriteFile(stage, []byte("{}"), 0600)
			case "stage-symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				err = os.WriteFile(outside, []byte("private"), 0600)
				if err == nil {
					err = os.Symlink(outside, stage)
				}
			case "stage-hardlink":
				outside := filepath.Join(t.TempDir(), "outside")
				err = os.WriteFile(outside, []byte("private"), 0600)
				if err == nil {
					err = os.Link(outside, stage)
				}
			case "stage-mode":
				err = os.WriteFile(stage, []byte("{}"), 0600)
				if err == nil {
					err = os.Chmod(stage, 0644)
				}
			case "stage-oversize":
				err = os.WriteFile(stage, make([]byte, (256<<10)+1), 0600)
			default:
				err = os.WriteFile(stage, []byte("{}"), 0600)
				if err == nil {
					if kind == "missing-primary" {
						err = os.Remove(filepath.Join(dir, "budget.json"))
					} else {
						err = os.WriteFile(filepath.Join(dir, "budget.json"), []byte("not JSON"), 0600)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
			if _, err = b.Snapshot(ctx); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
			if err = b.Charge(ctx, 1); err == nil {
				t.Fatal("unsafe metadata allowed mutation")
			}
			after, _ := os.ReadFile(filepath.Join(dir, "budget.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("invalid primary was overwritten")
			}
			if _, err = os.Lstat(stage); err != nil {
				t.Fatal("evidence removed", err)
			}
		})
	}
}

func TestSharedBudgetStagedMetadataExhaustionIsBoundedAndPreserved(t *testing.T) {
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, err := Open(ctx, dir, root, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	files := []string{}
	body := bytes.Repeat([]byte("x"), 8192)
	refused := false
	for n := 0; n < 256; n++ {
		f, e := os.CreateTemp(dir, ".budget-pending-*")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(body); e != nil {
			t.Fatal(e)
		}
		if e = f.Sync(); e != nil {
			t.Fatal(e)
		}
		f.Close()
		files = append(files, f.Name())
		if _, e = b.Snapshot(ctx); e != nil {
			refused = true
			break
		}
	}
	if !refused || len(files) < 2 || len(files) > 128 {
		t.Fatal("metadata guard was not bounded", len(files), refused)
	}
	if err = b.Charge(ctx, 1); err == nil {
		t.Fatal("metadata exhaustion allowed another ledger write")
	}
	used, err := usage(ctx, dir)
	if err != nil || used > ledgerAllowance {
		t.Fatal("coordinator exceeded funded allowance", used, err)
	}
	t.Logf("metadata_pressure staged_files=%d allocated_bytes=%d funded_allowance=%d", len(files), used, ledgerAllowance)
	for _, file := range files {
		got, e := os.ReadFile(file)
		if e != nil || !bytes.Equal(got, body) {
			t.Fatal("staged evidence lost", file, e)
		}
	}
}
