package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"gofluentd/internal/recvs"
	"gofluentd/library"
)

func securityJournal(t *testing.T, root string, compress bool) (*Journal, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	j := NewJournal(ctx, &JournalCfg{BufDirPath: root, BufSizeBytes: 8192, JournalOutChanLen: 8, CommitIDChanLen: 8, ChildJournalDataInchanLen: 8, ChildJournalIDInchanLen: 8, IsCompress: compress, CommittedIDTTL: time.Minute, MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}})
	t.Cleanup(func() {
		cancel()
		j.tag2JMap.Range(func(_, v interface{}) bool { v.(*journal.Journal).Close(); return true })
		<-j.directoriesClosed
	})
	return j, ctx
}

func TestLegacyJournalSecurityClosesDirectoryAnchor(t *testing.T) {
	j, ctx := securityJournal(t, filepath.Join(t.TempDir(), "journal"), false)
	if err := j.createJournalRunner(ctx, "logs"); err != nil {
		t.Fatal(err)
	}
	value, exists := j.tag2Directory.Load("logs")
	if !exists {
		t.Fatal("missing owned directory handle")
	}
	directory := value.(*os.File)
	if err := j.CloseTag("logs"); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed tag retained its directory descriptor: %v", err)
	}
	if _, exists := j.tag2Directory.Load("logs"); exists {
		t.Fatal("closed tag retained directory registration")
	}
	if err := j.createJournalRunner(ctx, "logs"); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyJournalSecurityConfiguredRootSymlink(t *testing.T) {
	sandbox := t.TempDir()
	actual, configured := filepath.Join(sandbox, "actual"), filepath.Join(sandbox, "configured")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, configured); err != nil {
		t.Fatal(err)
	}
	j, ctx := securityJournal(t, configured, false)
	if err := j.createJournalRunner(ctx, "logs"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(actual, "logs")); err != nil || !info.IsDir() {
		t.Fatalf("configured root symlink lost its target: %v", err)
	}
}

func TestLegacyJournalSecurityRejectsEscapingTags(t *testing.T) {
	for _, tag := range []string{"../outside", "nested/../../outside", "", ".", "..", "nested/tag"} {
		t.Run(tag, func(t *testing.T) {
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "journal")
			j, ctx := securityJournal(t, root, false)
			if err := j.createJournalRunner(ctx, tag); err == nil {
				t.Errorf("unsafe tag %q was accepted", tag)
			}
			j.tag2JMap.Range(func(k, _ interface{}) bool { t.Errorf("unsafe tag registered journal %q", k); return true })
			entries, err := os.ReadDir(sandbox)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "journal" {
				t.Errorf("tag created state outside journal root: %v", entries)
			}
			entries, err = os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("rejected tag allocated files: %v", entries)
			}
		})
	}
}

func TestLegacyJournalSecurityRejectsSymlinkChild(t *testing.T) {
	sandbox := t.TempDir()
	outside := filepath.Join(sandbox, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	j, ctx := securityJournal(t, filepath.Join(sandbox, "journal"), false)
	if err := os.Symlink(outside, filepath.Join(j.BufDirPath, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := j.createJournalRunner(ctx, "linked"); err == nil {
		t.Error("symlink tag was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("symlink redirected journal writes: %v", entries)
	}
}

func TestLegacyJournalSecurityRejectsEffectiveRewrittenTag(t *testing.T) {
	sandbox := t.TempDir()
	j, ctx := securityJournal(t, filepath.Join(sandbox, "journal"), false)
	dump, skip := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
	out := j.DumpMsgFlow(ctx, j.MsgPool, dump, skip)
	accepted := make(chan error, 1)
	msg := &library.FluentMsg{Tag: "../rewritten", ID: 71, Message: map[string]interface{}{"value": "minimal"}, DurableAck: accepted}
	receiver := recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{Name: "security-rewrite", IsRewriteTagFromTagKey: true, OriginRewriteTagKey: "route"})
	receiver.SetMsgPool(j.MsgPool)
	receiver.SetCounter(utils.NewCounter())
	receiver.SetAsyncOutChan(dump)
	msg.Tag = "initial.safe"
	msg.Message["route"] = []byte("../rewritten")
	receiver.ProcessMsg(msg)
	select {
	case err := <-accepted:
		if err == nil {
			t.Error("effective tag received successful durable acceptance")
		}
	case <-time.After(time.Second):
		t.Fatal("rejected tag did not complete acceptance")
	}
	select {
	case <-out:
		t.Error("rejected effective tag was forwarded")
	default:
	}
	if _, err := os.Stat(filepath.Join(sandbox, "rewritten")); !os.IsNotExist(err) {
		t.Errorf("rewritten tag escaped journal root: %v", err)
	}
	close(dump)
	close(skip)
}

func TestLegacyJournalSecurityRotationKeepsDirectoryAnchor(t *testing.T) {
	sandbox := t.TempDir()
	root, outside := filepath.Join(sandbox, "journal"), filepath.Join(sandbox, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	j, ctx := securityJournal(t, root, false)
	if err := j.createJournalRunner(ctx, "tenant.app"); err != nil {
		t.Fatal(err)
	}
	value, _ := j.tag2JMap.Load("tenant.app")
	backend := value.(*journal.Journal)
	if err := os.Rename(filepath.Join(root, "tenant.app"), filepath.Join(root, "retained")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tenant.app")); err != nil {
		t.Fatal(err)
	}
	if err := backend.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := backend.Sync(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("child replacement redirected rotation: %v", entries)
	}
}

func TestLegacyJournalSecurityPreservesExistingReplay(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "gzip"}[compress], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			tag := "tenant.app"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend, err := journal.NewJournal(journal.WithBufDirPath(filepath.Join(root, tag)), journal.WithBufSizeByte(8192), journal.WithIsCompress(compress), journal.WithIsAggresiveGC(false))
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			if err := backend.WriteData(&journal.Data{ID: 42, Data: map[string]interface{}{"tag": tag, "message": map[string]interface{}{"payload": "existing"}}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := backend.Rotate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			backend.Close()
			cancel()
			j, _ := securityJournal(t, root, compress)
			if _, exists := j.tag2JMap.Load(tag); !exists {
				t.Fatal("startup missed valid existing tag directory")
			}
			out := make(chan *library.FluentMsg, 8)
			if _, err := j.ProcessLegacyMsg(out); err != nil {
				t.Fatal(err)
			}
			msg := componentRecv(t, out)
			if msg.ID != 42 || msg.Tag != tag || msg.JournalTag != tag || msg.Message["payload"] != "existing" {
				t.Fatalf("existing replay identity changed: %+v", msg)
			}
		})
	}
}

func TestLegacyJournalSecurityPreservesOrdinaryTagNames(t *testing.T) {
	for _, tag := range []string{"tenant.app", "a..b", "app-log_1", "tenant:app", "日志", "app logs", "..\\outside"} {
		t.Run(tag, func(t *testing.T) {
			j, ctx := securityJournal(t, filepath.Join(t.TempDir(), "journal"), false)
			if err := j.createJournalRunner(ctx, tag); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(filepath.Join(j.BufDirPath, tag)); err != nil || !info.IsDir() {
				t.Fatalf("valid tag layout changed: %v", err)
			}
			value, _ := j.tag2JMap.Load(tag)
			if err := value.(*journal.Journal).Sync(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
