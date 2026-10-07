package recvs

import (
	"context"
	"fmt"
	"gofluentd/library"
	"regexp"
	"testing"
	"time"
)

func TestRegressionReceiverConcatPendingBound(t *testing.T) {
	r := newComponentFluent(t)
	r.ConcatorWait = time.Hour
	r.concatTagCfg = map[string]*concatCfg{"a": {headRegexp: regexp.MustCompile("^HEAD"), msgKey: "log", identifierKey: "source"}}
	in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg, 1100)
	r.SetAsyncOutChan(out)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); r.runConcator(ctx, 0, in) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 1025; i++ {
		in <- &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"source": fmt.Sprint(i), "log": "HEAD x"}}
	}
	in <- &library.FluentMsg{Tag: "a", Message: map[string]interface{}{"marker": "barrier"}}
	heads := 0
	seen := map[string]bool{}
	for {
		select {
		case m := <-out:
			if m.Message["marker"] == "barrier" {
				goto observed
			}
			heads++
			seen[m.Message["source"].(string)] = true
		case <-time.After(3 * time.Second):
			t.Fatal("barrier timeout")
		}
	}
observed:
	if heads == 0 {
		t.Error("CONCAT_BUDGET_REGRESSION: more than 1024 heads retained before forwarding barrier")
	}
	close(in)
	<-done
	// Avoid waiting for done twice in cleanup: receiving from a closed channel is safe.
	for len(out) > 0 {
		m := <-out
		heads++
		seen[m.Message["source"].(string)] = true
	}
	if heads != 1025 || len(seen) != 1025 {
		t.Fatalf("lost or duplicated heads: %d", heads)
	}
}
