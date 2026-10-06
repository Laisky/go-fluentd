package controller

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Laisky/go-journal"
	"github.com/coreos/etcd/pkg/fileutil"
	"gofluentd/library"
)

// The unavailable-proc control runs in a task-owned chroot inside an isolated
// Linux container. It never changes host/container mounts or live journal state.
func TestLegacyJournalLinuxProcContract(t *testing.T) {
	unavailable := os.Getenv("GO_FLUENTD_TEST_PROC_UNAVAILABLE") == "1"
	_, procErr := os.Stat("/proc/self/fd")
	if unavailable && procErr == nil {
		t.Fatal("negative fixture still has proc descriptor paths")
	}
	if !unavailable && procErr != nil {
		t.Fatalf("Linux deployment requires mounted proc: %v", procErr)
	}
	root := filepath.Join(t.TempDir(), "journal")
	j, ctx := securityJournal(t, root, false)
	dump, skip := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
	out := j.DumpMsgFlow(ctx, j.MsgPool, dump, skip)
	accepted := make(chan error, 1)
	dump <- &library.FluentMsg{ID: 81, Tag: "tenant.app", Message: map[string]interface{}{"value": "proc contract"}, DurableAck: accepted}
	select {
	case err := <-accepted:
		if unavailable && err == nil {
			t.Fatal("unavailable proc falsely completed durable acceptance")
		}
		if !unavailable && err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acceptance did not complete")
	}
	if unavailable {
		select {
		case <-out:
			t.Fatal("unavailable-proc event was forwarded")
		default:
		}
		if _, ok := j.tag2JMap.Load("tenant.app"); ok {
			t.Fatal("failed admission installed backend")
		}
		if _, ok := j.tag2Directory.Load("tenant.app"); ok {
			t.Fatal("failed admission retained anchor")
		}
		entries, err := os.ReadDir(filepath.Join(root, "tenant.app"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("unavailable proc created journal state: %v", entries)
		}
	} else {
		msg := componentRecv(t, out)
		if msg.ID != 81 || msg.Tag != "tenant.app" || msg.JournalTag != "tenant.app" {
			t.Fatalf("forwarded identity changed: %+v", msg)
		}
		competitor, err := journal.NewJournal(journal.WithBufDirPath(filepath.Join(root, "tenant.app")), journal.WithBufSizeByte(8192))
		if err != nil {
			t.Fatal(err)
		}
		defer competitor.Close()
		if err := competitor.Start(ctx); !errors.Is(err, fileutil.ErrLocked) {
			t.Fatalf("physical path bypassed descriptor-path lock: %v", err)
		}
	}
	close(dump)
	close(skip)
}
