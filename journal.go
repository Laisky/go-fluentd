package concator

import (
	"fmt"
	"io"
	"io/ioutil"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/Laisky/go-fluentd/libs"
	"github.com/Laisky/go-fluentd/monitor"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/go-utils/journal"
	"github.com/Laisky/zap"
)

const (
	defaultInnerJournalDataChanLen = 1000
	defaultInnerJournalIDChanLen   = 1000
	minimalBufSizeByte             = 10485760 // 10 MB
	intervalToStartingLegacy       = 3 * time.Second
	intervalForceGC                = 1 * time.Minute
)

type JournalCfg struct {
	BufDirPath                         string
	BufSizeBytes                       int64
	JournalOutChanLen, CommitIdChanLen int
	IsCompress                         bool
	MsgPool                            *sync.Pool
}

// Journal dumps all messages to files,
// then check every msg with committed id to make sure no msg lost
type Journal struct {
	*JournalCfg
	legacyLock *utils.Mutex

	outChan    chan *libs.FluentMsg
	commitChan chan *libs.FluentMsg

	baseJournalDir      string
	baseJournalCfg      *journal.JournalConfig
	jjLock              *sync.Mutex
	tag2JMap            *sync.Map // map[string]*journal.Journal
	tag2JJInchanMap     *sync.Map // map[string]chan *libs.FluentMsg
	tag2JJCommitChanMap *sync.Map // map[string]chan *libs.FluentMsg
}

// NewJournal create new Journal with `bufDirPath` and `BufSizeBytes`
func NewJournal(cfg *JournalCfg) *Journal {
	utils.Logger.Info("create new journal",
		zap.String("filepath", cfg.BufDirPath),
		zap.Int64("size", cfg.BufSizeBytes))
	if cfg.BufSizeBytes < minimalBufSizeByte {
		utils.Logger.Warn("journal buf file size too small", zap.Int64("size", cfg.BufSizeBytes))
	}

	jcfg := journal.NewConfig()
	jcfg.BufDirPath = cfg.BufDirPath
	jcfg.BufSizeBytes = cfg.BufSizeBytes
	jcfg.IsCompress = cfg.IsCompress
	// jcfg.RotateDuration = 3 * time.Second // TODO

	j := &Journal{
		JournalCfg: cfg,
		legacyLock: &utils.Mutex{},

		commitChan: make(chan *libs.FluentMsg, cfg.CommitIdChanLen),
		outChan:    make(chan *libs.FluentMsg, cfg.JournalOutChanLen),

		jjLock:              &sync.Mutex{},
		baseJournalDir:      jcfg.BufDirPath,
		baseJournalCfg:      jcfg,
		tag2JMap:            &sync.Map{},
		tag2JJInchanMap:     &sync.Map{},
		tag2JJCommitChanMap: &sync.Map{},
	}
	j.initLegacyJJ()
	j.registerMonitor()
	j.startCommitRunner()
	return j
}

// initLegacyJJ process existed legacy data and ids
func (j *Journal) initLegacyJJ() {
	files, err := ioutil.ReadDir(j.baseJournalDir)
	if err != nil {
		utils.Logger.Warn("try to read dir of journal got error", zap.Error(err))
		return
	}

	for _, dir := range files {
		if dir.IsDir() {
			j.createJournalRunner(dir.Name())
		}
	}
}

// LoadMaxID load the max committed id from journal
func (j *Journal) LoadMaxID() (id int64, err error) {
	var (
		nid  int64
		tag  string
		jj   *journal.Journal
		err2 error
	)
	j.tag2JMap.Range(func(k, v interface{}) bool {
		tag = k.(string)
		jj = v.(*journal.Journal)
		if nid, err2 = jj.LoadMaxId(); err2 != nil {
			if nid > id {
				id = nid
			}
		} else {
			err = errors.Wrapf(err2, "try to load max id with tag `%v` got error", tag)
		}

		return true
	})

	return nid, err
}

func (j *Journal) ProcessLegacyMsg(dumpChan chan *libs.FluentMsg) (maxID int64, err2 error) {
	if !j.legacyLock.TryLock() {
		return 0, fmt.Errorf("another legacy is running")
	}
	defer j.legacyLock.ForceRealse()

	utils.Logger.Debug("starting to process legacy data...")
	var (
		wg = &sync.WaitGroup{}
		l  = &sync.Mutex{}
	)

	j.tag2JMap.Range(func(k, v interface{}) bool {
		wg.Add(1)
		go func(tag string, jj *journal.Journal) {
			defer wg.Done()
			var (
				innerMaxID int64
				err        error
				msg        *libs.FluentMsg
				data       = &journal.Data{Data: map[string]interface{}{}}
			)

			if !jj.LockLegacy() { // avoid rotate
				return
			}

			startTs := utils.Clock.GetUTCNow()
			for {
				msg = j.MsgPool.Get().(*libs.FluentMsg)
				data.Data["message"] = nil // alloc new map to avoid old data contaminate
				if err = jj.LoadLegacyBuf(data); err == io.EOF {
					utils.Logger.Debug("load legacy buf done",
						zap.Float64("sec", utils.Clock.GetUTCNow().Sub(startTs).Seconds()),
					)
					j.MsgPool.Put(msg)

					l.Lock()
					if innerMaxID > maxID {
						maxID = innerMaxID
					}
					l.Unlock()
					return
				} else if err != nil {
					utils.Logger.Error("load legacy data got error", zap.Error(err))
					j.MsgPool.Put(msg)
					if !jj.LockLegacy() {
						l.Lock()
						if innerMaxID > maxID {
							maxID = innerMaxID
						}
						err2 = err
						l.Unlock()
						return
					}
					continue
				}

				if data.Data["message"] == nil {
					utils.Logger.Warn("lost message")
					j.MsgPool.Put(msg)
					continue
				}

				msg.Id = data.ID
				msg.Tag = string(data.Data["tag"].(string))
				msg.Message = data.Data["message"].(map[string]interface{})
				if msg.Id > innerMaxID {
					innerMaxID = msg.Id
				}
				utils.Logger.Debug("load msg from legacy",
					zap.String("tag", msg.Tag),
					zap.Int64("id", msg.Id))

				// rewrite data into journal
				// only committed id can really remove a msg
				dumpChan <- msg
			}
		}(k.(string), v.(*journal.Journal))

		return true
	})

	wg.Wait()
	utils.Logger.Debug("process legacy done")
	return
}

// createJournalRunner create journal for a tag,
// and return commit channel and dump channel
func (j *Journal) createJournalRunner(tag string) {
	j.jjLock.Lock()
	defer j.jjLock.Unlock()

	var ok bool
	if _, ok = j.tag2JMap.Load(tag); ok {
		return // double check to prevent duplicate create jj runner
	}

	jcfg := journal.NewConfig()
	jcfg.BufDirPath = j.baseJournalCfg.BufDirPath
	jcfg.BufSizeBytes = j.baseJournalCfg.BufSizeBytes
	jcfg.IsCompress = j.baseJournalCfg.IsCompress
	jcfg.IsAggresiveGC = false
	jcfg.BufDirPath = filepath.Join(j.baseJournalDir, tag)

	utils.Logger.Info("createJournalRunner for tag", zap.String("tag", tag))
	if _, ok = j.tag2JMap.Load(tag); ok {
		utils.Logger.Panic("tag already exists in tag2JMap", zap.String("tag", tag))
	}
	utils.Logger.Info("create new journal.Journal", zap.String("tag", tag))
	jj := journal.NewJournal(jcfg)
	j.tag2JMap.Store(tag, jj)

	if _, ok = j.tag2JJInchanMap.Load(tag); ok {
		utils.Logger.Panic("tag already exists in tag2JJInchanMap", zap.String("tag", tag))
	}
	j.tag2JJInchanMap.Store(tag, make(chan *libs.FluentMsg, defaultInnerJournalDataChanLen))

	if _, ok = j.tag2JJCommitChanMap.Load(tag); ok {
		utils.Logger.Panic("tag already exists in tag2JJCommitChanMap", zap.String("tag", tag))
	}
	j.tag2JJCommitChanMap.Store(tag, make(chan *libs.FluentMsg, defaultInnerJournalIDChanLen))

	// create ids writer
	go func() {
		defer utils.Logger.Panic("journal ids writer quit")
		var (
			mid      int64
			err      error
			nRetry   = 0
			maxRetry = 2
		)

		chani, ok := j.tag2JJCommitChanMap.Load(tag)
		if !ok {
			utils.Logger.Panic("tag must in `j.tag2JJCommitChanMap`", zap.String("tag", tag))
		}

		for msg := range chani.(chan *libs.FluentMsg) {
			nRetry = 0
			for nRetry < maxRetry {
				if err = jj.WriteId(msg.Id); err != nil {
					nRetry++
				}
				break
			}
			if err != nil && nRetry == maxRetry {
				utils.Logger.Error("try to write id to journal got error", zap.Error(err))
			}

			if msg.ExtIds != nil {
				for _, mid = range msg.ExtIds {
					nRetry = 0
					for nRetry < maxRetry {
						if err = jj.WriteId(mid); err != nil {
							nRetry++
						}
						break
					}
					if err != nil && nRetry == maxRetry {
						utils.Logger.Error("try to write id to journal got error", zap.Error(err))
					}
				}
				msg.ExtIds = nil
			}

			j.MsgPool.Put(msg)
		}
	}()

	// create data writer
	go func() {
		defer utils.Logger.Panic("journal data writer quit")

		var (
			data     = &journal.Data{Data: map[string]interface{}{}}
			err      error
			nRetry   = 0
			maxRetry = 2
		)
		chani, ok := j.tag2JJInchanMap.Load(tag)
		if !ok {
			utils.Logger.Panic("tag should in `j.tag2JJInchanMap`", zap.String("tag", tag))
		}

		for msg := range chani.(chan *libs.FluentMsg) {
			data.ID = msg.Id
			data.Data["message"] = msg.Message
			data.Data["tag"] = msg.Tag
			nRetry = 0
			for nRetry < maxRetry {
				if err = jj.WriteData(data); err != nil {
					nRetry++
				}
				break
			}
			if err != nil && nRetry == maxRetry {
				utils.Logger.Error("try to write msg to journal got error",
					zap.Error(err),
					zap.String("tag", msg.Tag),
				)
			}

			select {
			case j.outChan <- msg:
			default:
				// msg will reproduce in legacy stage,
				// so you can discard msg without any side-effect.
				j.MsgPool.Put(msg)
			}
		}
	}()
}

func (j *Journal) GetOutChan() chan *libs.FluentMsg {
	return j.outChan
}

func (j *Journal) ConvertMsg2Buf(msg *libs.FluentMsg, data *map[string]interface{}) {
	(*data)["id"] = msg.Id
	(*data)["tag"] = msg.Tag
	(*data)["message"] = msg.Message
}

func (j *Journal) DumpMsgFlow(msgPool *sync.Pool, dumpChan, skipDumpChan chan *libs.FluentMsg) chan *libs.FluentMsg {
	// deal with legacy
	go func() {
		defer utils.Logger.Panic("legacy processor exit")
		var err error
		for { // try to starting legacy loading
			if _, err = j.ProcessLegacyMsg(dumpChan); err != nil {
				utils.Logger.Error("process legacy got error", zap.Error(err))
			}
			time.Sleep(intervalToStartingLegacy)
		}
	}()

	// start periodic gc
	go func() {
		defer utils.Logger.Panic("gc runner exit")
		for {
			utils.ForceGC()
			time.Sleep(intervalForceGC)
		}
	}()

	// deal with msgs that skip dump
	go func() {
		defer utils.Logger.Panic("skipDumpChan goroutine exit")
		for msg := range skipDumpChan {
			j.outChan <- msg
		}
	}()

	go func() {
		defer utils.Logger.Panic("legacy dumper exit")

		var (
			ok  bool
			jji interface{}
		)
		for msg := range dumpChan {
			utils.Logger.Debug("try to dump msg", zap.String("tag", msg.Tag))
			if jji, ok = j.tag2JJInchanMap.Load(msg.Tag); !ok {
				j.createJournalRunner(msg.Tag)
				jji, _ = j.tag2JJInchanMap.Load(msg.Tag)
			}

			select {
			case jji.(chan *libs.FluentMsg) <- msg:
			case j.outChan <- msg:
			default:
				utils.Logger.Error("discard msg since of journal & downstream busy",
					zap.String("tag", msg.Tag),
					zap.String("msg", fmt.Sprint(msg)),
				)
				j.MsgPool.Put(msg)
			}
		}
	}()

	return j.outChan
}

func (j *Journal) GetCommitChan() chan<- *libs.FluentMsg {
	return j.commitChan
}

func (j *Journal) startCommitRunner() {
	go func() {
		defer utils.Logger.Panic("id commitor exit")

		var (
			ok    bool
			chani interface{}
		)
		for msg := range j.commitChan {
			utils.Logger.Debug("try to commit msg",
				zap.String("tag", msg.Tag),
				zap.Int64("id", msg.Id))
			if chani, ok = j.tag2JJCommitChanMap.Load(msg.Tag); !ok {
				j.createJournalRunner(msg.Tag)
				chani, _ = j.tag2JJCommitChanMap.Load(msg.Tag)
			}

			select {
			case chani.(chan *libs.FluentMsg) <- msg:
			default:
				utils.Logger.Error("discard id without commit since of journal is busy",
					zap.String("tag", msg.Tag),
					zap.Int64("id", msg.Id),
				)
				j.MsgPool.Put(msg)
			}
		}
	}()
}

func (j *Journal) registerMonitor() {
	monitor.AddMetric("journal", func() map[string]interface{} {
		result := map[string]interface{}{}
		j.tag2JMap.Range(func(k, v interface{}) bool {
			result[k.(string)] = v.(*journal.Journal).GetMetric()
			return true
		})
		return result
	})
}
