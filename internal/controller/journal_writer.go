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

func (j *Journal) runDataWriter(ctx context.Context, tag string, writer journalDataWriter, in <-chan *library.FluentMsg, counter *utils.Counter) {
	defer log.Logger.Info("journal data writer exit", zap.String("tag", tag))
	data := &journal.Data{Data: make(map[string]interface{}, 2)}
	for {
		var msg *library.FluentMsg
		var ok bool
		select {
		case <-ctx.Done():
			return
		case msg, ok = <-in:
			if !ok {
				return
			}
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
		if err == nil && msg.DurableAck != nil {
			err = writer.Sync()
		}
		j.finishJournalWrite(msg, err)
	}
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
