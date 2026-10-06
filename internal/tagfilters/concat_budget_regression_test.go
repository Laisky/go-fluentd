package tagfilters

import (
	"context"
	"fmt"
	"gofluentd/library"
	"testing"
	"time"
)

func TestRegressionTagFilterConcatPendingBound(t *testing.T) {
	f, cfg, commits := componentConcator(t, 10000)
	in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg, 1100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); f.StartNewConcator(ctx, cfg, out, in) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 1025; i++ {
		in <- &library.FluentMsg{Tag: "a", ID: int64(i + 1), Message: map[string]interface{}{"source": fmt.Sprint(i), "log": "HEAD x"}}
	}
	in <- &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"marker": "barrier"}}
	heads := 0
	seen := map[int64]bool{}
	for {
		select {
		case m := <-out:
			if m.Message["marker"] == "barrier" {
				goto observed
			}
			heads++
			seen[m.ID] = true
		case <-time.After(3 * time.Second):
			t.Fatal("barrier timeout")
		}
	}
observed:
	if heads == 0 {
		t.Error("CONCAT_BUDGET_REGRESSION: more than 1024 journaled heads retained before forwarding barrier")
	}
	close(in)
	<-done
	for len(out) > 0 {
		m := <-out
		heads++
		seen[m.ID] = true
	}
	if heads != 1025 || len(seen) != 1025 || len(commits) != 0 {
		t.Fatalf("delivery/ACK mismatch heads=%d unique=%d commits=%d", heads, len(seen), len(commits))
	}
}
