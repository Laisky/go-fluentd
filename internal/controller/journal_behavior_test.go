package controller

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/library"
)

func TestBehaviorJournalDefaultBuffers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := NewJournal(ctx, &JournalCfg{BufDirPath: t.TempDir(), MsgPool: &sync.Pool{}})
	if cap(j.outChan) != j.JournalOutChanLen || cap(j.commitChan) != j.CommitIDChanLen {
		t.Fatalf("journal configured/actual capacities differ: %d/%d %d/%d", cap(j.outChan), j.JournalOutChanLen, cap(j.commitChan), j.CommitIDChanLen)
	}
	if j.CloseTag("missing") == nil {
		t.Fatal("missing tag accepted")
	}
}
func TestBehaviorJournalDataAndConstituentAcknowledgements(t *testing.T) {
	for _, compress := range []bool{false, true} {
		name := "plain"
		if compress {
			name = "gzip"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			j := NewJournal(ctx, &JournalCfg{BufDirPath: dir, IsCompress: compress, BufSizeBytes: 8192, JournalOutChanLen: 8, CommitIDChanLen: 8, ChildJournalDataInchanLen: 8, ChildJournalIDInchanLen: 8, CommittedIDTTL: time.Minute, MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}})
			j.createJournalRunner(ctx, "logs")
			backendI, _ := j.tag2JMap.Load("logs")
			backend := backendI.(*journal.Journal)
			defer backend.Close()
			inputI, _ := j.tag2JJInchanMap.Load("logs")
			input := inputI.(chan *library.FluentMsg)
			for _, id := range []int64{1, 2, 3} {
				m := &library.FluentMsg{Tag: "logs", ID: id, Message: map[string]interface{}{"value": id}}
				input <- m
				if behaviorReceive(t, j.outChan) != m {
					t.Fatal("journal changed identity")
				}
			}
			// Commit 1 and 2 using the actual constituent-ID writer; 3 must survive.
			ack := &library.FluentMsg{Tag: "logs", ID: 1, ExtIds: []int64{2}}
			j.GetCommitChan() <- ack
			// Drain and join the ID writer via channel closure, then wait until the
			// IDs are visible on disk. Never read an ACK object after handing it off.
			deadline := time.Now().Add(time.Second)
			suffix := "*.ids"
			if compress {
				suffix += ".gz"
			}
			for {
				if err := backend.Sync(); err != nil {
					t.Fatal(err)
				}
				files, err := filepath.Glob(filepath.Join(dir, "logs", suffix))
				if err != nil {
					t.Fatal(err)
				}
				seen := journal.NewInt64Set()
				for _, name := range files {
					fp, err := os.Open(name)
					if err != nil {
						t.Fatal(err)
					}
					dec, err := journal.NewIdsDecoder(fp, compress)
					if err == nil {
						err = dec.ReadAllToInt64Set(seen)
					}
					fp.Close()
					if err != nil && err != io.EOF {
						t.Fatal(err)
					}
				}
				if seen.CheckAndRemove(1) && seen.CheckAndRemove(2) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("constituent IDs not persisted")
				}
				time.Sleep(time.Millisecond)
			}
			for i := 0; i < 2; i++ {
				if err := backend.Rotate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			out := make(chan *library.FluentMsg, 8)
			if _, err := j.ProcessLegacyMsg(out); err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 {
				t.Fatalf("replay count=%d want 1", len(out))
			}
			m := <-out
			if m.ID != 3 || m.Tag != "logs" {
				t.Fatalf("incorrect replay: %+v", m)
			}
		})
	}
}
