//go:build linux

package diskbudget

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Expected GAP control: the real Docker logging driver is an independent writer
// outside managed roots. A correct collector ledger alone cannot certify a
// whole-host physical cap. This test never changes daemon/production settings.
func TestWholeScopeDockerStdoutIsOutsideCollectorAuthority(t *testing.T) {
	image := os.Getenv("OTLP_SCOPE_DOCKER_IMAGE")
	if image == "" {
		t.Skip("requires explicitly enabled owned Docker scope fixture")
	}
	ctx := context.Background()
	dir, root := budgetPrivateDir(t), budgetPrivateDir(t)
	b, e := Open(ctx, dir, root, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = b.Charge(ctx, 2<<20); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "synthetic-managed-wal"), make([]byte, 2<<20), 0600); e != nil {
		t.Fatal(e)
	}
	snap, e := b.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Reserve(ctx, snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	before, e := b.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Admit(ctx, 1, 0); !errors.Is(e, ErrCapacity) {
		t.Fatal("negative control not full", e)
	}
	name := fmt.Sprintf("task20261018-scope-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := []string{"run", "-d", "--name", name, "--label", "com.laisky.task=task20261017-07-scope-gap", "--network", "none", "--memory", "32m", "--cpus", ".2", "--pids-limit", "16", "--log-driver", "json-file", "--log-opt", "max-size=4m", "--log-opt", "max-file=1", "--entrypoint", "/bin/sh", image, "-c", `head -c 3145728 /dev/zero | tr '[:cntrl:]' x`}
	if out, e := exec.Command("docker", args...).CombinedOutput(); e != nil {
		t.Fatal("owned fixture launch", e, string(out))
	}
	t.Cleanup(func() {
		label, _ := exec.Command("docker", "inspect", "--format", `{{index .Config.Labels "com.laisky.task"}}`, name).Output()
		if strings.TrimSpace(string(label)) == "task20261017-07-scope-gap" {
			if out, e := exec.Command("docker", "rm", "-f", name).CombinedOutput(); e != nil {
				t.Errorf("owned cleanup failed: %v %s", e, out)
			}
		}
	})
	if out, e := exec.Command("docker", "wait", name).Output(); e != nil || strings.TrimSpace(string(out)) != "0" {
		t.Fatal("synthetic writer failed", e)
	}
	path, e := exec.Command("docker", "inspect", "--format", `{{.LogPath}}`, name).Output()
	if e != nil {
		t.Fatal(e)
	}
	info, e := exec.Command("sudo", "-n", "stat", "--format", "%s:%b", strings.TrimSpace(string(path))).Output()
	if e != nil {
		t.Fatal("read own Docker log metadata", e)
	}
	pieces := strings.Split(strings.TrimSpace(string(info)), ":")
	if len(pieces) != 2 {
		t.Fatal("invalid stat metadata")
	}
	size, _ := strconv.ParseInt(pieces[0], 10, 64)
	blocks, _ := strconv.ParseInt(pieces[1], 10, 64)
	managed, e := usage(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	coordinator, e := usage(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	after, e := b.Snapshot(ctx)
	if e != nil || before != after {
		t.Fatal("collector ledger changed unexpectedly", before, after, e)
	}
	if e = b.Admit(ctx, 1, 0); !errors.Is(e, ErrCapacity) {
		t.Fatal("managed refusal bypassed", e)
	}
	actual := managed + coordinator + max(size, blocks*512)
	if size < 3<<20 || actual <= before.Limit {
		t.Fatal("outside-writer GAP not reproduced", size, actual, before)
	}
	t.Logf("EXPECTED GAP: managed ledger full=%d, outside Docker bytes=%d allocated=%d, combined actual=%d exceeds cap=%d; no production change", before.ChargedBytes+before.ReservedBytes, size, blocks*512, actual, before.Limit)
}
