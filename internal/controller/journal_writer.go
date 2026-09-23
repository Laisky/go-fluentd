package controller

import (
	"context"

	"gofluentd/library"
	"gofluentd/library/log"

	journal "github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
)

// journalDataWriter is the existing storage boundary, not a second WAL protocol.
// Sync must make all completed writes durable, including segments sealed by rotation.
type journalDataWriter interface {
	WriteData(*journal.Data) error
	Sync() error
}

const defaultGroupCommitMaxMessages = 64
const maximumGroupCommitMaxMessages = 1024

// Groups are formed only from immediately available messages. There is no
// batching timer, so an idle writer never waits for a second arrival. The queue
// accumulates the next group naturally while the current Sync is in progress.
func (j *Journal) runDataWriter(ctx context.Context, tag string, writer journalDataWriter, in <-chan *library.FluentMsg, counter *utils.Counter) {
	defer log.Logger.Info("journal data writer exit", zap.String("tag", tag))
	limit := j.GroupCommitMaxMessages
	if limit <= 0 {
		limit = defaultGroupCommitMaxMessages
	}
	batch := make([]*library.FluentMsg, 0, limit)
	data := &journal.Data{Data: make(map[string]interface{}, 2)}
	for {
		if ctx.Err() != nil {
			return
		}
		var first *library.FluentMsg
		var ok bool
		select {
		case <-ctx.Done():
			return
		case first, ok = <-in:
			if !ok {
				return
			}
		}
		batch = append(batch[:0], first)
		closed := false
		// Preserve the unsynchronized best-effort path. A reliable group may
		// include best-effort messages interleaved on this same tag's queue.
		if first.DurableAck != nil {
		DRAIN:
			for len(batch) < limit {
				select {
				case next, open := <-in:
					if !open {
						closed = true
						break DRAIN
					}
					batch = append(batch, next)
				default:
					break DRAIN
				}
			}
		}
		err := j.persistGroup(ctx, tag, writer, data, batch, counter)
		for i, msg := range batch {
			batch[i] = nil // never retain or inspect a relinquished message
			j.finishJournalWrite(msg, err)
		}
		if closed {
			return
		}
	}
}

// Fail the entire owned group on an unrecovered append or Sync error. Some
// bytes may already exist: an error response is an unknown outcome, not proof
// of absence. No live copy or success receipt may escape the failed barrier.
func (j *Journal) persistGroup(ctx context.Context, tag string, writer journalDataWriter, data *journal.Data, batch []*library.FluentMsg, counter *utils.Counter) error {
	needsSync := false
	for _, msg := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg.JournalTag = tag
		data.ID = msg.ID
		data.Data["message"], data.Data["tag"] = msg.Message, msg.Tag
		counter.Count()
		var err error
		for attempt := 0; attempt < 2; attempt++ {
			if err = writer.WriteData(data); err == nil {
				break
			}
		}
		if err != nil {
			return err
		}
		needsSync = needsSync || msg.DurableAck != nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if needsSync {
		return writer.Sync()
	}
	return nil
}

// The worker relinquishes msg after this call. A receipt belongs to the HTTP
// request, while the message may be recycled as soon as it is published.
func (j *Journal) finishJournalWrite(msg *library.FluentMsg, err error) {
	if err != nil {
		log.Logger.Error("persist message", zap.Error(err), zap.String("tag", msg.Tag))
		msg.CompleteAcceptance(err)
		j.MsgPool.Put(msg)
		return
	}
	msg.CompleteAcceptance(nil)
	select {
	case j.outChan <- msg:
	default:
		// The retained journal, not this in-memory copy, owns retry delivery.
		j.MsgPool.Put(msg)
	}
}
