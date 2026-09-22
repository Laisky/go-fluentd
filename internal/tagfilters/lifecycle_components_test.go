package tagfilters

import (
	"context"
	"gofluentd/library"
	"testing"
	"testing/synctest"
	"time"
)

func TestComponentConcatorSpawnDrainsClosedInput(t *testing.T) {
	f, _, _ := componentConcator(t, 10000)
	f.SetDefaultIntervalChanSize(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan *library.FluentMsg, 8)
	in := f.Spawn(ctx, "a", out)
	head := componentLine("a", "HEAD last", 1)
	in <- head
	close(in)
	if got := componentTake(t, out); got != head {
		t.Fatal("spawned concatenator lost the final record")
	}
}
func TestComponentConcatorIdleTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, cfg, commits := componentConcator(t, 10000)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg, 2)
		done := make(chan struct{})
		go func() { defer close(done); f.StartNewConcator(ctx, cfg, out, in) }()
		head := componentLine("a", "HEAD timed", 1)
		in <- head
		synctest.Wait()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if len(out) != 0 {
			t.Fatal("flushed before timeout")
		}
		time.Sleep(1040 * time.Millisecond)
		synctest.Wait()
		if len(out) != 1 || <-out != head || len(commits) != 0 {
			t.Fatal("idle timeout failed or incorrectly acknowledged record")
		}
		cancel()
		<-done
	})
}
func TestComponentConcatorCancellationWhileOutputBlocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, cfg, commits := componentConcator(t, 10000)
		ctx, cancel := context.WithCancel(context.Background())
		in, out := make(chan *library.FluentMsg), make(chan *library.FluentMsg)
		done := make(chan struct{})
		go func() { defer close(done); f.StartNewConcator(ctx, cfg, out, in) }()
		in <- componentLine("a", "HEAD blocked", 1)
		close(in)
		synctest.Wait()
		cancel()
		<-done
		if len(commits) != 0 {
			t.Fatal("canceled output was acknowledged")
		}
	})
}
