package concator

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Laisky/go-concator/libs"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/journal"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

type JournalCfg struct {
	BufDirPath        string
	BufSizeBytes      int64
	CommitChanLen     int
	CommitChanBusyLen int
}

// Journal dumps all messages to files,
// then check every msg with commited id to make sure no msg lost
type Journal struct {
	*JournalCfg
	j          *journal.Journal
	legacyLock uint32
	outChan    chan *libs.FluentMsg
}

// NewJournal create new Journal with `bufDirPath` and `BufSizeBytes`
func NewJournal(cfg *JournalCfg) *Journal {
	utils.Logger.Info("create new journal",
		zap.String("filepath", cfg.BufDirPath),
		zap.Int64("size", cfg.BufSizeBytes))
	if cfg.BufSizeBytes < 10485760 { // 10MB
		utils.Logger.Warn("journal buf file size too small", zap.Int64("size", cfg.BufSizeBytes))
	}

	return &Journal{
		JournalCfg: cfg,
		j: journal.NewJournal(&journal.JournalConfig{
			BufDirPath:   cfg.BufDirPath,
			BufSizeBytes: cfg.BufSizeBytes,
		}),
		legacyLock: 0,
		outChan:    make(chan *libs.FluentMsg, cfg.CommitChanLen),
	}
}

// LoadMaxId load the max commited id from journal
func (j *Journal) LoadMaxId() (int64, error) {
	return j.j.LoadMaxId()
}

func (j *Journal) GetOutChan() chan *libs.FluentMsg {
	return j.outChan
}

func (j *Journal) ConvertMsg2Buf(msg *libs.FluentMsg, data *map[string]interface{}) {
	(*data)["id"] = msg.Id
	(*data)["tag"] = msg.Tag
	(*data)["message"] = msg.Message
}

func (j *Journal) ProcessLegacyMsg(msgPool *sync.Pool, msgChan chan *libs.FluentMsg) (maxId int64, err error) {
	utils.Logger.Info("starting to process legacy data...")
	if ok := atomic.CompareAndSwapUint32(&j.legacyLock, 0, 1); !ok {
		utils.Logger.Info("legacy data already in processing")
		return 0, nil
	}
	defer atomic.StoreUint32(&j.legacyLock, 0)

	var (
		i    = 0
		msg  *libs.FluentMsg
		data = map[string]interface{}{}
	)

	if !j.j.LockLegacy() { // avoid rotate
		return
	}

	startTs := time.Now()
	for {
		i++
		msg = msgPool.Get().(*libs.FluentMsg)
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

func (j *Journal) DumpMsgFlow(msgPool *sync.Pool, msgChan <-chan *libs.FluentMsg) chan *libs.FluentMsg {
	// deal with legacy
	go func() {
		var err error
		for {
			if _, err = j.ProcessLegacyMsg(msgPool, j.outChan); err != nil {
				utils.Logger.Error("process legacy got error", zap.Error(err))
			}
			time.Sleep(30 * time.Second)
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

			// give chan to legacy processing
			if j.j.IsLegacyRunning() && len(j.outChan) > j.CommitChanBusyLen {
				continue
			}

			select {
			case j.outChan <- msg:
			default:
			}
		}
	}()

	return j.outChan
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
