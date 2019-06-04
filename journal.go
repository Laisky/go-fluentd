package concator

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/journal"
	"github.com/Laisky/zap"
)

type JournalCfg struct {
	BufDirPath                         string
	BufSizeBytes                       int64
	JournalOutChanLen, CommitIdChanLen int
	MsgPool                            *sync.Pool
}

// Journal dumps all messages to files,
// then check every msg with committed id to make sure no msg lost
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

	jcfg := journal.NewConfig()
	jcfg.BufDirPath = cfg.BufDirPath
	jcfg.BufSizeBytes = cfg.BufSizeBytes

	j := &Journal{
		JournalCfg: cfg,
		j:          journal.NewJournal(jcfg),
		legacyLock: 0,
		outChan:    make(chan *libs.FluentMsg, cfg.JournalOutChanLen),
	}
	j.registerMonitor()
	return j
}

// LoadMaxID load the max committed id from journal
func (j *Journal) LoadMaxID() (int64, error) {
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

func (j *Journal) ProcessLegacyMsg(msgPool *sync.Pool, msgChan, dumpChan chan *libs.FluentMsg) (maxId int64, err error) {
	utils.Logger.Debug("starting to process legacy data...")
	if ok := atomic.CompareAndSwapUint32(&j.legacyLock, 0, 1); !ok {
		utils.Logger.Debug("legacy data already in processing")
		return 0, nil
	}
	defer atomic.StoreUint32(&j.legacyLock, 0)

	var (
		i    = 0
		msg  *libs.FluentMsg
		data = &journal.Data{Data: map[string]interface{}{}}
	)

	if !j.j.LockLegacy() { // avoid rotate
		return
	}

	startTs := utils.Clock.GetUTCNow()
	for {
		i++
		msg = msgPool.Get().(*libs.FluentMsg)
		// utils.Logger.Info(fmt.Sprintf("got %p", msg))
		data.Data["message"] = nil // alloc new map to avoid old data contaminate

		if err = j.j.LoadLegacyBuf(data); err == io.EOF {
			utils.Logger.Debug("load legacy buf done", zap.Int("n", i), zap.Float64("sec", utils.Clock.GetUTCNow().Sub(startTs).Seconds()))
			msgPool.Put(msg)
			// utils.Logger.Info(fmt.Sprintf("recycle: %p", msg))
			return maxId, nil
		} else if err != nil {
			utils.Logger.Error("load legacy data got error", zap.Error(err))
			msgPool.Put(msg)
			// utils.Logger.Info(fmt.Sprintf("recycle: %p", msg))
			continue
		}

		if data.Data["message"] == nil {
			utils.Logger.Warn("lost message")
			msgPool.Put(msg)
			// utils.Logger.Info(fmt.Sprintf("recycle: %p, %v", msg))
			continue
		}

		msg.Id = data.ID
		msg.Tag = string(data.Data["tag"].(string))
		msg.Message = data.Data["message"].(map[string]interface{})
		if msg.Id > maxId {
			maxId = msg.Id
		}

		// rewrite data into journal
		// only committed id can really remove a msg
		dumpChan <- msg
	}
}

// IntervalToStartingLegacy interval to restarting legacy loading
const IntervalToStartingLegacy = 3 * time.Second // arbitary

func (j *Journal) DumpMsgFlow(msgPool *sync.Pool, dumpChan, skipDumpChan chan *libs.FluentMsg) chan *libs.FluentMsg {
	// deal with legacy
	go func() {
		defer utils.Logger.Panic("legacy processor exit")
		var err error
		for { // try to starting legacy loading
			if _, err = j.ProcessLegacyMsg(msgPool, j.outChan, dumpChan); err != nil {
				utils.Logger.Error("process legacy got error", zap.Error(err))
			}
			time.Sleep(IntervalToStartingLegacy)
		}
	}()

	go func() {
		defer utils.Logger.Panic("skipDumpChan goroutine exit")
		for msg := range skipDumpChan {
			j.outChan <- msg
		}
	}()

	go func() {
		var (
			err      error
			data     = &journal.Data{Data: map[string]interface{}{}}
			nRetry   = 0
			maxRetry = 5
			msg      *libs.FluentMsg
		)
		defer utils.Logger.Panic("legacy dumper exit", zap.String("msg", fmt.Sprint(msg)))

		for msg = range dumpChan {
			data.ID = msg.Id
			data.Data["message"] = msg.Message
			data.Data["tag"] = msg.Tag
			// utils.Logger.Debug("got new message", zap.Int64("id", msg.Id), zap.String("tag", msg.Tag))
			for {
				if err = j.j.WriteData(data); err != nil {
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

			// utils.Logger.Debug("success write data journal", zap.String("tag", msg.Tag), zap.Int64("id", msg.Id))

			select {
			case j.outChan <- msg:
			default:
				// utils.Logger.Info(fmt.Sprintf("recycle: %p, %v", msg))
				j.MsgPool.Put(msg)
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
		commitChan = make(chan int64, j.CommitIdChanLen)
	)
	go func() {
		defer utils.Logger.Panic("id commitor exit")

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

func (j *Journal) registerMonitor() {
	monitor.AddMetric("journal", func() map[string]interface{} {
		return j.j.GetMetric()
	})
}
