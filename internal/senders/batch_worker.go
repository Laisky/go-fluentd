package senders

import (
	"context"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"gofluentd/library"
	"gofluentd/library/log"
	"time"
)

// runBatches is owned by one worker. Closing input flushes its partial batch;
// cancellation stops promptly without claiming delivery of uncertain work.
// The first record is sent immediately, preserving the existing latency policy.
func (s *BaseSender) runBatches(ctx context.Context, in <-chan *library.FluentMsg, size int, maxWait time.Duration, send func(context.Context, []*library.FluentMsg) error) {
	batch := make([]*library.FluentMsg, 0, size)
	timer := time.NewTimer(maxWait)
	timer.Stop()
	defer timer.Stop()
	var tick <-chan time.Time
	var lastFlush time.Time
	flush := func() bool {
		timer.Stop()
		tick = nil
		if len(batch) == 0 {
			return true
		}
		var err error
		for attempt := 0; attempt < 4; attempt++ {
			if ctx.Err() != nil {
				return false
			}
			if utils.Settings.GetBool("dry") {
				err = nil
				break
			}
			err = send(ctx, batch)
			if err == nil {
				break
			}
		}
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			log.Logger.Warn("batch delivery failed", zap.Error(err), zap.Int("records", len(batch)))
		}
		result := s.successedChan
		if err != nil {
			result = s.failedChan
		}
		for _, msg := range batch {
			select {
			case result <- msg:
			case <-ctx.Done():
				return false
			}
		}
		// All ownership has been transferred; only clear our slice references.
		clear(batch)
		batch = batch[:0]
		lastFlush = time.Now()
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			if !flush() {
				return
			}
		case msg, ok := <-in:
			if !ok {
				flush()
				return
			}
			if msg == nil {
				continue
			}
			batch = append(batch, msg)
			if len(batch) == size || lastFlush.IsZero() || time.Since(lastFlush) >= maxWait {
				if !flush() {
					return
				}
			} else if tick == nil {
				timer.Reset(maxWait - time.Since(lastFlush))
				tick = timer.C
			}
		}
	}
}
