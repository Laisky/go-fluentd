package senders

import (
	"context"
	"gofluentd/library"
	"time"
)

// runBatchWorker owns its batch and never closes caller-owned channels. A normal
// input close flushes pending records; cancellation does not acknowledge them.
func runBatchWorker(ctx context.Context, in <-chan *library.FluentMsg, size int, wait time.Duration, deliver func([]*library.FluentMsg) bool) {
	ticker := time.NewTicker(wait)
	defer ticker.Stop()
	batch := make([]*library.FluentMsg, 0, size)
	var lastFlush time.Time
	flush := func() bool {
		if len(batch) == 0 {
			return true
		}
		if !deliver(batch) {
			return false
		}
		clear(batch)
		batch = batch[:0]
		lastFlush = time.Now()
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-in:
			if !ok {
				flush()
				return
			}
			batch = append(batch, msg)
			// Preserve the existing immediate first delivery, then batch by size/time.
			if len(batch) == size || lastFlush.IsZero() || time.Since(lastFlush) >= wait {
				if !flush() {
					return
				}
			}
		case <-ticker.C:
			if !flush() {
				return
			}
		}
	}
}

// reportBatch transfers ownership; callers must not access a message afterwards.
func (s *BaseSender) reportBatch(ctx context.Context, msgs []*library.FluentMsg, success bool) bool {
	out := s.failedChan
	if success {
		out = s.successedChan
	}
	for _, msg := range msgs {
		if ctx.Err() != nil {
			return false
		}
		select {
		case out <- msg:
		case <-ctx.Done():
			return false
		}
	}
	return true
}
