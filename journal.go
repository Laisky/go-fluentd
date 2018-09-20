package concator

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/journal"
	"github.com/pkg/errors"
)

type Journal struct {
	bufDirPath string
	j          *journal.Journal
	legacyLock uint32
}

func NewJournal(bufDirPath string, bufFileSize int64) *Journal {
	cfg := &journal.JournalConfig{
		BufDirPath:   bufDirPath,
		BufSizeBytes: bufFileSize,
	}

	return &Journal{
		bufDirPath: bufDirPath,
		j:          journal.NewJournal(cfg),
		legacyLock: 0,
	}
}

func (j *Journal) ConvertMsg2Buf(msg *FluentMsg, data *map[string]interface{}) {
	(*data)["id"] = msg.Id
	(*data)["tag"] = msg.Tag
	(*data)["message"] = msg.Message
}

func (j *Journal) ProcessLegacyMsg(msgPool *sync.Pool, msgChan chan *FluentMsg) (maxId int64, err error) {
	utils.Logger.Info("starting to process legacy data...")
	if ok := atomic.CompareAndSwapUint32(&j.legacyLock, 0, 1); !ok {
		utils.Logger.Info("legacy data already in processing")
		return 0, nil
	}
	defer atomic.StoreUint32(&j.legacyLock, 0)

	var (
		i    = 0
		msg  *FluentMsg
		data = map[string]interface{}{}
	)
	for {
		if j.j.LockLegacy() { // avoid rotate
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	startTs := time.Now()
	for {
		i++
		msg = msgPool.Get().(*FluentMsg)
		data["message"] = nil // alloc new map to avoid old data contaminate

		if err = j.j.LoadLegacyBuf(&data); err == io.EOF {
			utils.Logger.Info("load legacy buf done", zap.Int("n", i), zap.Float64("sec", time.Now().Sub(startTs).Seconds()))
			return maxId, nil
		} else if err != nil {
			return 0, errors.Wrap(err, "try to load legacy data got error")
		}

		if data["message"] == nil {
			utils.Logger.Warn("lost message")
			continue
		}

		msg.Id = data["id"].(int64)
		msg.Tag = string(data["tag"].([]byte))
		msg.Message = data["message"].(map[string]interface{})
		if msg.Id > maxId {
			maxId = msg.Id
		}

		msgChan <- msg
	}
}

func (j *Journal) DumpMsgFlow(msgPool *sync.Pool, msgChan <-chan *FluentMsg) chan *FluentMsg {
	var (
		newChan = make(chan *FluentMsg, 5000)
	)

	// deal with legacy
	go func() {
		var err error
		for {
			if _, err = j.ProcessLegacyMsg(msgPool, newChan); err != nil {
				utils.Logger.Error("process legacy got error", zap.Error(err))
			}
			time.Sleep(1 * time.Minute) // per minute
		}
	}()

	go func() {
		var (
			err      error
			data     = map[string]interface{}{}
			nRetry   = 0
			maxRetry = 5
		)
		for msg := range msgChan {
			data["id"] = msg.Id
			data["tag"] = msg.Tag
			data["message"] = msg.Message
			utils.Logger.Debug("got new message", zap.Int64("id", msg.Id), zap.String("tag", msg.Tag))
			for {
				if err = j.j.WriteData(&data); err != nil {
					nRetry++
					if nRetry < maxRetry {
						utils.Logger.Warn("try to write data got error", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id), zap.Error(err))
						continue
					}

					nRetry = 0
					utils.Logger.Error("try to write data got error", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id), zap.Error(err))
					break
				}

				break
			}

			utils.Logger.Debug("success write data journal", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))
			newChan <- msg
		}
	}()

	return newChan
}

func (j *Journal) GetCommitChan() chan<- int64 {
	var (
		err        error
		nRetry     = 0
		maxRetry   = 5
		commitChan = make(chan int64, 5000)
	)
	go func() {
		for id := range commitChan {
			for {
				if err = j.j.WriteId(id); err != nil {
					utils.Logger.Warn("write id got error", zap.Int64("id", id), zap.Error(err))
					nRetry++
					if nRetry < maxRetry {
						continue
					}

					nRetry = 0
					utils.Logger.Error("try to write id got error", zap.Int64("id", id))
					break
				}

				break
			}
		}
	}()

	return commitChan
}
