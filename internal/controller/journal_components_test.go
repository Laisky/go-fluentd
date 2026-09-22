package controller

import (
	"context"
	journal "github.com/Laisky/go-journal"
	"gofluentd/library"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func componentIDOnDisk(dir string, id int64) bool {
	names, _ := filepath.Glob(filepath.Join(dir, "*.ids*"))
	for _, name := range names {
		fp, err := os.Open(name)
		if err != nil {
			continue
		}
		dec, err := journal.NewIdsDecoder(fp, strings.HasSuffix(name, ".gz"))
		if err == nil {
			max, readErr := dec.LoadMaxId()
			fp.Close()
			if readErr == nil && max == id {
				return true
			}
		} else {
			fp.Close()
		}
	}
	return false
}
func TestComponentJournalAcknowledgesOriginalTagAfterRetag(t *testing.T) {
	for _, compress := range []bool{false, true} {
		for _, retag := range []bool{false, true} {
			name := map[bool]string{false: "plain", true: "gzip"}[compress] + "/" + map[bool]string{false: "same_tag", true: "rewritten_tag"}[retag]
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				dir := t.TempDir()
				j := NewJournal(ctx, &JournalCfg{BufDirPath: dir, BufSizeBytes: 8192, JournalOutChanLen: 8, CommitIDChanLen: 8, ChildJournalDataInchanLen: 8, ChildJournalIDInchanLen: 8, IsCompress: compress, CommittedIDTTL: time.Minute, MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}})
				defer func() {
					cancel()
					j.tag2JMap.Range(func(k, v interface{}) bool { v.(*journal.Journal).Close(); return true })
				}()
				j.createJournalRunner(ctx, "original")
				channel, _ := j.tag2JJInchanMap.Load("original")
				m := &library.FluentMsg{Tag: "original", ID: 42, ExtIds: []int64{43}, Message: map[string]interface{}{"payload": "keep"}}
				channel.(chan *library.FluentMsg) <- m
				if componentRecv(t, j.GetOutChan()) != m {
					t.Fatal("journal changed message identity")
				}
				if retag {
					m.Tag = "destination"
				}
				j.GetCommitChan() <- m
				deadline := time.Now().Add(time.Second)
				for {
					j.tag2JMap.Range(func(_, value interface{}) bool {
						if err := value.(*journal.Journal).Sync(); err != nil {
							t.Fatal(err)
						}
						return true
					})
					if componentIDOnDisk(filepath.Join(dir, "original"), 43) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("acknowledgement was not written to the source journal after retagging")
					}
					time.Sleep(time.Millisecond)
				}
				if retag {
					if _, exists := j.tag2JMap.Load("destination"); exists {
						t.Fatal("routing tag incorrectly created a separate acknowledgement journal")
					}
				}
				backend, _ := j.tag2JMap.Load("original")
				jj := backend.(*journal.Journal)
				if err := jj.Sync(); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if err := jj.Rotate(ctx); err != nil {
						t.Fatal(err)
					}
				}
				max, err := j.LoadMaxID()
				if err != nil || max != 43 {
					t.Fatalf("recovered max ID=%d error=%v", max, err)
				}
			})
		}
	}
}

func TestComponentJournalReplayRestoresAcknowledgementOwner(t *testing.T) {
	j, _, _ := regressionReplayJournal(t, false)
	out := make(chan *library.FluentMsg, 8)
	if _, err := j.ProcessLegacyMsg(out); err != nil {
		t.Fatal(err)
	}
	msg := componentRecv(t, out)
	if msg.JournalTag != "logs" {
		t.Fatalf("replayed message lost its journal owner: %q", msg.JournalTag)
	}
	msg.Tag = "new-route"
	if msg.JournalTag != "logs" {
		t.Fatal("routing changed acknowledgement provenance")
	}
}

func TestComponentJournalFlowPersistsOrExplicitlyBypasses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	pool := &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}
	j := NewJournal(ctx, &JournalCfg{BufDirPath: dir, BufSizeBytes: 8192, JournalOutChanLen: 8, CommitIDChanLen: 8, ChildJournalDataInchanLen: 8, ChildJournalIDInchanLen: 8, MsgPool: pool, CommittedIDTTL: time.Minute})
	defer func() {
		cancel()
		j.tag2JMap.Range(func(_, v interface{}) bool { v.(*journal.Journal).Close(); return true })
	}()
	dump, skip := make(chan *library.FluentMsg, 4), make(chan *library.FluentMsg, 4)
	out := j.DumpMsgFlow(ctx, pool, dump, skip)
	persisted := &library.FluentMsg{ID: 42, Tag: "durable", Message: map[string]interface{}{"value": "on-disk"}}
	dump <- persisted
	if m := componentRecv(t, out); m != persisted || m.JournalTag != "durable" {
		t.Fatal("journal flow failed to persist/forward with its owner")
	}
	backend, exists := j.tag2JMap.Load("durable")
	if !exists {
		t.Fatal("journal was not created for a persisted tag")
	}
	if err := backend.(*journal.Journal).Sync(); err != nil {
		t.Fatal(err)
	}
	if !regressionDiskHasMessage(t, filepath.Join(dir, "durable")) {
		t.Fatal("persistent flow forwarded without a disk record")
	}
	bypassed := &library.FluentMsg{ID: 43, Tag: "explicit-bypass", Message: map[string]interface{}{"value": "memory-only"}}
	skip <- bypassed
	if m := componentRecv(t, out); m != bypassed || m.JournalTag != "" {
		t.Fatal("explicit bypass identity/semantics changed")
	}
	if _, exists := j.tag2JMap.Load("explicit-bypass"); exists {
		t.Fatal("explicit bypass was unexpectedly journaled")
	}
	close(dump)
	close(skip)
}
