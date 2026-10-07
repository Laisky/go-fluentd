//go:build linux

package controller

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/library"
)

func permissionCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegressionLegacyPrivateRoot(t *testing.T) {
	for _, mode := range []os.FileMode{0755, 0775} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dir := t.TempDir()
			permissionCheck(t, os.Chmod(dir, mode))
			j := &Journal{JournalCfg: &JournalCfg{BufDirPath: dir}}
			if err := j.valid(); err == nil {
				t.Error("PRIVATE_ROOT_REGRESSION: permissive existing root accepted")
			}
			entries, err := os.ReadDir(dir)
			permissionCheck(t, err)
			if len(entries) != 0 {
				t.Error("refusal mutated root")
			}
			info, err := os.Stat(dir)
			permissionCheck(t, err)
			if info.Mode().Perm() != mode {
				t.Error("implicit permission migration")
			}
		})
	}
}
func TestRegressionLegacyPrivateChildAndFiles(t *testing.T) {
	for _, kind := range []string{"directory-0755", "directory-0775", "data", "ids", "lock", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j, ctx := securityJournal(t, root, false)
			path := filepath.Join(root, "logs")
			permissionCheck(t, os.Mkdir(path, 0700))
			name := "20260101_00000001.buf"
			switch kind {
			case "directory-0755":
				permissionCheck(t, os.Chmod(path, 0755))
			case "directory-0775":
				permissionCheck(t, os.Chmod(path, 0775))
			default:
				if kind == "ids" {
					name = "20260101_00000001.ids"
				}
				if kind == "lock" {
					name = ".journal.lock"
				}
				file := filepath.Join(path, name)
				if kind == "symlink" || kind == "hardlink" {
					target := filepath.Join(t.TempDir(), "untouched")
					permissionCheck(t, os.WriteFile(target, nil, 0600))
					if kind == "symlink" {
						permissionCheck(t, os.Symlink(target, file))
					} else {
						permissionCheck(t, os.Link(target, file))
					}
				} else {
					permissionCheck(t, os.WriteFile(file, nil, 0600))
					permissionCheck(t, os.Chmod(file, 0664))
				}
			}
			before, err := os.ReadDir(path)
			permissionCheck(t, err)
			if err := j.createJournalRunner(ctx, "logs"); err == nil {
				t.Errorf("PRIVATE_CHILD_REGRESSION: unsafe %s accepted", kind)
			}
			if _, ok := j.tag2JMap.Load("logs"); ok {
				t.Error("rejected child registered a writer")
			}
			after, err := os.ReadDir(path)
			permissionCheck(t, err)
			if len(after) != len(before) {
				t.Errorf("refusal created/deleted evidence: before=%v after=%v", before, after)
			}
		})
	}
}
func TestRegressionLegacyPrivateCreation(t *testing.T) {
	mask := os.Getenv("LEGACY_PRIVATE_UMASK")
	if mask == "" {
		for _, m := range []string{"0", "77"} {
			t.Run(m, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestRegressionLegacyPrivateCreation$", "-test.v", "-test.timeout=60s")
				cmd.Env = append(os.Environ(), "LEGACY_PRIVATE_UMASK="+m)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("creation subprocess: %v\n%s", err, out)
				}
				t.Logf("%s", out)
			})
		}
		return
	}
	n, err := strconv.ParseInt(mask, 8, 32)
	permissionCheck(t, err)
	syscall.Umask(int(n))
	for _, gz := range []bool{false, true} {
		t.Run(fmt.Sprint("gzip=", gz), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j, ctx := securityJournal(t, root, gz)
			permissionCheck(t, j.createJournalRunner(ctx, "logs"))
			value, _ := j.tag2JMap.Load("logs")
			backend := value.(*journal.Journal)
			permissionCheck(t, backend.WriteData(&journal.Data{ID: 1, Data: map[string]interface{}{"tag": "logs", "message": map[string]interface{}{"payload": "synthetic"}}}))
			permissionCheck(t, backend.WriteId(2))
			permissionCheck(t, backend.Sync())
			for i := 0; i < 3; i++ {
				permissionCheck(t, backend.Rotate(context.Background()))
			}
			permissionCheck(t, j.CloseTag("logs"))
			permissionCheck(t, j.createJournalRunner(ctx, "logs"))
			files := 0
			permissionCheck(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				want := os.FileMode(0600)
				if info.IsDir() {
					want = 0700
				} else {
					files++
				}
				if info.Mode().Perm() != want {
					t.Errorf("PRIVATE_CREATION_REGRESSION: %s mode=%04o want=%04o", path, info.Mode().Perm(), want)
				}
				return nil
			}))
			if files < 3 {
				t.Fatal("no data/ACK/lock fixture")
			}
		})
	}
}

// Use a positive-access control to distinguish real permission denial from a
// subprocess which could not execute or traverse the test fixture. Root-only
// qualification is separate from ordinary CI and never touches live storage.
func TestLegacyPrivateOtherUID(t *testing.T) {
	if root := os.Getenv("LEGACY_PRIVATE_ACCESS_ROOT"); root != "" {
		fp, err := os.OpenFile(filepath.Join(root, "control"), os.O_RDWR, 0)
		permissionCheck(t, err)
		fp.Close()
		for _, flag := range []int{os.O_RDONLY, os.O_WRONLY} {
			fp, err := os.OpenFile(filepath.Join(root, "private"), flag, 0)
			if fp != nil {
				fp.Close()
			}
			if !os.IsPermission(err) {
				t.Fatalf("expected actual access denial, got %v", err)
			}
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("isolated other-UID qualification requires root")
	}
	root, err := os.MkdirTemp("", "legacy-private-access-")
	permissionCheck(t, err)
	defer os.RemoveAll(root)
	permissionCheck(t, os.Chmod(root, 0755))
	exe, err := os.ReadFile(os.Args[0])
	permissionCheck(t, err)
	binary := filepath.Join(root, "permission-test")
	permissionCheck(t, os.WriteFile(binary, exe, 0755))
	permissionCheck(t, os.Chmod(binary, 0755))
	permissionCheck(t, os.WriteFile(filepath.Join(root, "control"), nil, 0666))
	permissionCheck(t, os.Chmod(filepath.Join(root, "control"), 0666))
	fp, err := journal.OpenBufFile(filepath.Join(root, "private"), 0)
	permissionCheck(t, err)
	fp.Close()
	for _, gid := range []uint32{0, 65534} {
		cmd := exec.Command(binary, "-test.run=^TestLegacyPrivateOtherUID$", "-test.v")
		cmd.Env = append(os.Environ(), "LEGACY_PRIVATE_ACCESS_ROOT="+root)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: gid, Groups: []uint32{gid}}}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("UID65534/GID%d: %v\n%s", gid, err, out)
		}
		if !strings.Contains(string(out), "PASS") {
			t.Fatal("child did not execute assertion")
		}
	}
}

func TestLegacyPrivateExplicitMigrationPreservesReplay(t *testing.T) {
	for _, gz := range []bool{false, true} {
		t.Run(fmt.Sprint(gz), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j, ctx := securityJournal(t, root, gz)
			dir := filepath.Join(root, "logs")
			backend, err := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithBufSizeByte(8192), journal.WithIsCompress(gz), journal.WithIsAggresiveGC(false))
			permissionCheck(t, err)
			permissionCheck(t, backend.Start(ctx))
			for _, id := range []int64{1, 2} {
				permissionCheck(t, backend.WriteData(&journal.Data{ID: id, Data: map[string]interface{}{"tag": "logs", "message": map[string]interface{}{"payload": fmt.Sprint("record-", id)}}}))
			}
			permissionCheck(t, backend.WriteId(2))
			permissionCheck(t, backend.Sync())
			backend.Close()
			files, err := os.ReadDir(dir)
			permissionCheck(t, err)
			original := map[string][]byte{}
			for _, file := range files {
				path := filepath.Join(dir, file.Name())
				data, err := os.ReadFile(path)
				permissionCheck(t, err)
				original[file.Name()] = data
				permissionCheck(t, os.Chmod(path, 0664))
			}
			if err := j.createJournalRunner(ctx, "logs"); err == nil {
				t.Fatal("historical wide files accepted without explicit migration")
			}
			for name, data := range original {
				path := filepath.Join(dir, name)
				got, err := os.ReadFile(path)
				permissionCheck(t, err)
				if string(got) != string(data) {
					t.Fatal("refusal mutated durable bytes")
				}
				// Simulate an operator's explicit offline migration, not runtime chmod.
				permissionCheck(t, os.Chmod(path, 0600))
			}
			permissionCheck(t, j.createJournalRunner(ctx, "logs"))
			out := make(chan *library.FluentMsg, 8)
			_, err = j.ProcessLegacyMsg(out)
			permissionCheck(t, err)
			if len(out) != 1 {
				t.Fatalf("migration resurrected ACKed or lost unACKed work: %d", len(out))
			}
			msg := <-out
			if msg.ID != 1 || msg.JournalTag != "logs" || msg.Message["payload"] != "record-1" {
				t.Fatalf("migration changed ownership/payload: %+v", msg)
			}
		})
	}
}

func TestLegacyPrivateForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated foreign-owner qualification requires root")
	}
	for _, kind := range []string{"root", "child", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "owned")
			permissionCheck(t, os.Mkdir(root, 0700))
			if kind == "root" {
				permissionCheck(t, os.Chown(root, 65534, 65534))
				j := &Journal{JournalCfg: &JournalCfg{BufDirPath: root}}
				if err := j.valid(); err == nil {
					t.Fatal("foreign root accepted")
				}
				return
			}
			j, ctx := securityJournal(t, root, false)
			dir := filepath.Join(root, "logs")
			permissionCheck(t, os.Mkdir(dir, 0700))
			path := dir
			if kind == "file" {
				path = filepath.Join(dir, "20260101_00000001.buf")
				permissionCheck(t, os.WriteFile(path, nil, 0600))
			}
			permissionCheck(t, os.Chown(path, 65534, 65534))
			if err := j.createJournalRunner(ctx, "logs"); err == nil {
				t.Fatal("foreign storage owner accepted")
			}
		})
	}
}

func TestLegacyPrivateRootChangedBeforeAdmission(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, ctx := securityJournal(t, root, false)
	permissionCheck(t, os.Chmod(root, 0755))
	if err := j.createJournalRunner(ctx, "logs"); err == nil {
		t.Fatal("changed parent accepted")
	}
	entries, err := os.ReadDir(root)
	permissionCheck(t, err)
	if len(entries) != 0 {
		t.Fatal("unsafe parent created child")
	}
}

// Both startup recovery and live durable admission must apply the same policy.
// Neither refusal may create a backend, acknowledge success or forward a record.
func TestLegacyPrivateRefusalOwnership(t *testing.T) {
	for _, startup := range []bool{false, true} {
		t.Run(fmt.Sprint("startup=", startup), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			j, ctx := securityJournal(t, root, false)
			dir := filepath.Join(root, "logs")
			permissionCheck(t, os.Mkdir(dir, 0700))
			file := filepath.Join(dir, "historical.buf")
			permissionCheck(t, os.WriteFile(file, []byte("retained-evidence"), 0600))
			permissionCheck(t, os.Chmod(file, 0664))
			if startup {
				if err := j.initLegacyJJ(ctx); err == nil {
					t.Fatal("unsafe startup tree accepted")
				}
			} else {
				dump, skip := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg)
				out := j.DumpMsgFlow(ctx, j.MsgPool, dump, skip)
				accepted := make(chan error, 1)
				dump <- &library.FluentMsg{Tag: "logs", ID: 71, Message: map[string]interface{}{"payload": "synthetic"}, DurableAck: accepted}
				select {
				case err := <-accepted:
					if err == nil {
						t.Fatal("unsafe storage acknowledged as durable")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("refusal did not complete receipt")
				}
				select {
				case <-out:
					t.Fatal("refused message forwarded")
				default:
				}
				close(dump)
				close(skip)
			}
			if _, ok := j.tag2JMap.Load("logs"); ok {
				t.Fatal("unsafe storage registered a backend")
			}
			entries, err := os.ReadDir(dir)
			permissionCheck(t, err)
			if len(entries) != 1 {
				t.Fatal("refusal changed the retained file set")
			}
			got, err := os.ReadFile(file)
			permissionCheck(t, err)
			if string(got) != "retained-evidence" {
				t.Fatal("refusal changed retained bytes")
			}
		})
	}
}
