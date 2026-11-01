package alerts

import (
	"context"
	"time"
)

func wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// run owns the delivery worker and the shutdown sequence. Parsing stays on its
// own goroutine so that Telegram backoff can never stall OTLP decoding.
func (o *Observer) run(ctx context.Context) {
	parserDone := make(chan struct{})
	go func() {
		defer close(parserDone)
		o.runParser(ctx)
	}()
	o.deliveryAlive.Store(true)
	for o.deliverNext(ctx) {
	}
	o.deliveryAlive.Store(false)
	o.lifecycle.Lock()
	o.stopped = true
	o.lifecycle.Unlock()
	<-parserDone
	o.discardQueued()
	close(o.done)
}

// runParser decodes admitted requests and reports silent sources. Both belong
// here because this worker, unlike delivery, never waits for the network.
func (o *Observer) runParser(ctx context.Context) {
	o.parserAlive.Store(true)
	defer o.parserAlive.Store(false)
	ticker := time.NewTicker(o.cfg.DedupWindow)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			o.flushSummaries(now)
		case r := <-o.otlp:
			o.parseOTLP(r)
			<-o.otlpSlots
		}
	}
}

// deliverNext reports whether the worker should continue.
func (o *Observer) deliverNext(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case e := <-o.queue:
		return o.deliver(ctx, notificationText(e, o.cfg.IncludeMessage))
	}
}

// deliver spaces and retries one notification. It returns false when shutdown
// interrupted the attempt.
func (o *Observer) deliver(ctx context.Context, text string) bool {
	for attempt := 0; attempt < o.cfg.MaxAttempts; attempt++ {
		if !wait(ctx, o.cfg.SendInterval) {
			o.shutdownEvents.Add(1)
			return false
		}
		retry, delay := o.send(ctx, text)
		if ctx.Err() != nil {
			if retry {
				o.shutdownEvents.Add(1)
			}
			return false
		}
		if !retry {
			return true
		}
		if attempt == o.cfg.MaxAttempts-1 {
			o.failed.Add(1)
			return true
		}
		o.retries.Add(1)
		if backoff := time.Duration(1<<attempt) * time.Second; delay < backoff {
			delay = backoff
		}
		if !wait(ctx, delay) {
			o.shutdownEvents.Add(1)
			return false
		}
	}
	return true
}

func (o *Observer) send(ctx context.Context, text string) (bool, time.Duration) {
	result := o.notifier.Send(ctx, text)
	if result.Delivered {
		o.sent.Add(1)
	} else if !result.Retry {
		o.failed.Add(1)
	}
	return result.Retry, result.Delay
}

// discardQueued accounts for work abandoned at shutdown.
func (o *Observer) discardQueued() {
	for {
		select {
		case <-o.queue:
			o.shutdownEvents.Add(1)
		case <-o.otlp:
			o.shutdownRequests.Add(1)
		default:
			return
		}
	}
}
