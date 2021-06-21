package controller

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gofluentd/internal/monitor"
	"gofluentd/library"

	"github.com/Laisky/go-journal"
	utils "github.com/Laisky/go-utils"
	"github.com/Laisky/zap"
	"github.com/pkg/errors"
)

const (
	minimalBufSizeByte        = 10485760  // 10 MB
	defaultBufSizeByte        = 104857600 // 100 MB
	intervalToStartingLegacy  = 3 * time.Second
	defaultJournalLegacyWait  = 1 * time.Second
	defaultIntervalSecForceGC = 1 * time.Minute
)

type JournalCfg struct {
	BufDirPath   string
	BufSizeBytes int64
	JournalOutChanLen,
	CommitIDChanLen,
	ChildJournalDataInchanLen,
	ChildJournalIDInchanLen int
	GCIntervalSec  time.Duration
	IsCompress     bool
	MsgPool        *sync.Pool
	CommittedIDTTL time.Duration
}

// Journal dumps all messages to files,
// then check every msg with committed id to make sure no msg lost
type Journal struct {
	*JournalCfg
	legacyLock *utils.Mutex

	outChan    chan *library.FluentMsg
	commitChan chan *library.FluentMsg

	jjLock    *sync.Mutex
	tag2JMap, // map[string]*journal.Journal
	tag2JJInchanMap, // map[string]chan *library.FluentMsg
	tag2JJCommitChanMap, // map[string]chan *library.FluentMsg
	tag2IDsCounter,
	tag2DataCounter *sync.Map
}

// NewJournal create new Journal with `bufDirPath` and `BufSizeBytes`
func NewJournal(ctx context.Context, cfg *JournalCfg) *Journal {
	j := &Journal{
		JournalCfg: cfg,
		legacyLock: &utils.Mutex{},

		jjLock:              &sync.Mutex{},
		tag2JMap:            &sync.Map{},
		tag2JJInchanMap:     &sync.Map{},
		tag2JJCommitChanMap: &sync.Map{},
		tag2IDsCounter:      &sync.Map{},
		tag2DataCounter:     &sync.Map{},
	}
	j.commitChan = make(chan *library.FluentMsg, cfg.CommitIDChanLen)
	j.outChan = make(chan *library.FluentMsg, cfg.JournalOutChanLen)
	if err := j.valid(); err != nil {
		library.Logger.Panic("invalid", zap.Error(err))
	}

	j.initLegacyJJ(ctx)
	j.registerMonitor()
	j.startCommitRunner(ctx)

	library.Logger.Info("new journal",
		zap.String("buf_dir_path", j.BufDirPath),
		zap.Int64("buf_file_bytes", j.BufSizeBytes),
		zap.Duration("gc_inteval_sec", j.GCIntervalSec),
		zap.Int("journal_out_chan_len", j.JournalOutChanLen),
		zap.Int("commit_id_chan_len", j.CommitIDChanLen),
		zap.Int("child_data_chan_len", j.ChildJournalDataInchanLen),
		zap.Int("child_id_chan_len", j.ChildJournalIDInchanLen),
	)
	return j
}

func (j *Journal) valid() error {
	if j.BufSizeBytes <= 0 {
		j.BufSizeBytes = defaultBufSizeByte
		library.Logger.Info("reset buf_file_bytes", zap.Int64("buf_file_bytes", j.BufSizeBytes))
	} else if j.BufSizeBytes < minimalBufSizeByte {
		library.Logger.Warn("journal buf file size too small", zap.Int64("size", j.BufSizeBytes))
	}

	if j.GCIntervalSec <= 0 {
		j.GCIntervalSec = defaultIntervalSecForceGC
		library.Logger.Info("reset gc_inteval_sec", zap.Duration("gc_inteval_sec", j.GCIntervalSec))
	}

	if j.JournalOutChanLen <= 0 {
		j.JournalOutChanLen = 10000
		library.Logger.Info("reset journal_out_chan_len", zap.Int("journal_out_chan_len", j.JournalOutChanLen))
	}

	if j.CommitIDChanLen <= 0 {
		j.CommitIDChanLen = 50000
		library.Logger.Info("reset commit_id_chan_len", zap.Int("commit_id_chan_len", j.CommitIDChanLen))
	}

	if j.ChildJournalDataInchanLen <= 0 {
		j.ChildJournalDataInchanLen = j.JournalOutChanLen
		library.Logger.Info("reset child_data_chan_len", zap.Int("child_data_chan_len", j.ChildJournalDataInchanLen))
	}

	if j.ChildJournalIDInchanLen <= 0 {
		j.ChildJournalIDInchanLen = j.CommitIDChanLen
		library.Logger.Info("reset child_id_chan_len", zap.Int("child_id_chan_len", j.ChildJournalIDInchanLen))
	}

	if err := os.MkdirAll(j.BufDirPath, os.ModePerm); err != nil {
		return errors.Wrapf(err, "create directory `%s` for buf", j.BufDirPath)
	}

	return nil
}

func (j *Journal) CloseTag(tag string) error {
	j.jjLock.Lock()
	defer j.jjLock.Unlock()

	jj, ok := j.tag2JMap.Load(tag)
	if !ok {
		return fmt.Errorf("tag %v not exists in tag2CtxCancelMap", tag)
	}

	jj.(*journal.Journal).Close()
	j.tag2JMap.Delete(tag)
	j.tag2IDsCounter.Delete(tag)
	j.tag2DataCounter.Delete(tag)

	if inchan, ok := j.tag2JJInchanMap.Load(tag); !ok {
		library.Logger.Panic("tag must exists", zap.String("tag", tag))
	} else {
		close(inchan.(chan *library.FluentMsg))
		j.tag2JJInchanMap.Delete(tag)
	}

	if inchan, ok := j.tag2JJCommitChanMap.Load(tag); !ok {
		library.Logger.Panic("tag must exists", zap.String("tag", tag))
	} else {
		close(inchan.(chan *library.FluentMsg))
		j.tag2JJCommitChanMap.Delete(tag)
	}

	library.Logger.Info("delete journal tag", zap.String("tag", tag))
	return nil
}

// initLegacyJJ process existed legacy data and ids
func (j *Journal) initLegacyJJ(ctx context.Context) {
	files, err := ioutil.ReadDir(j.BufDirPath)
	if err != nil {
		library.Logger.Error("try to read dir of journal",
			zap.String("directory", j.BufDirPath),
			zap.Error(err))
		return
	}

	for _, dir := range files {
		if dir.IsDir() {
			j.createJournalRunner(ctx, dir.Name())
		}
	}
}

// LoadMaxID load the max committed id from journal
func (j *Journal) LoadMaxID() (maxID int64, err error) {
	var (
		tag string
		jj  *journal.Journal
		id  int64
	)
	j.tag2JMap.Range(func(k, v interface{}) bool {
		tag = k.(string)
		jj = v.(*journal.Journal)
		if id, err = jj.LoadMaxId(); err != nil {
			err = errors.Wrapf(err, "load max id with tag `%s`;", tag)
			return false
		}

		if id > maxID {
			maxID = id
		}

		return true
	})

	return maxID, err
}

func (j *Journal) ProcessLegacyMsg(dumpChan chan *library.FluentMsg) (maxID int64, err2 error) {
	if !j.legacyLock.TryLock() {
		return 0, fmt.Errorf("another legacy is running")
	}
	defer j.legacyLock.ForceRelease()

	library.Logger.Debug("starting to process legacy data...")
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
				msg        *library.FluentMsg
				data       = &journal.Data{Data: map[string]interface{}{}}
			)

			if !jj.LockLegacy() { // avoid rotate
				return
			}

			startTs := utils.Clock.GetUTCNow()
		NEXT_LEGACY_MSG:
			for {
				// msgp will overwrite new data to old map without
				// create new map to avoid old data contaminate
				msg = j.MsgPool.Get().(*library.FluentMsg)
				data.Data["message"] = nil
				if err = jj.LoadLegacyBuf(data); err == io.EOF {
					library.Logger.Debug("load legacy buf done",
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
					library.Logger.Error("load legacy data got error", zap.Error(err))
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
					library.Logger.Warn("lost message")
					j.MsgPool.Put(msg)
					continue
				}

				msg.ID = data.ID
				msg.Tag = string(data.Data["tag"].(string))
				msg.Message = data.Data["message"].(map[string]interface{})
				if msg.ID > innerMaxID {
					innerMaxID = msg.ID
				}
				library.Logger.Debug("load msg from legacy",
					zap.String("tag", msg.Tag),
					zap.Int64("id", msg.ID))

				// rewrite data into journal
				// only committed id can really remove a msg
				for {
					select {
					case dumpChan <- msg:
						continue NEXT_LEGACY_MSG
					default:
						// do not block dumpchan
						time.Sleep(defaultJournalLegacyWait)
					}
				}
			}
		}(k.(string), v.(*journal.Journal))

		return true
	})

	wg.Wait()
	library.Logger.Debug("process legacy done")
	return
}

// createJournalRunner create journal for a tag,
// and return commit channel and dump channel
func (j *Journal) createJournalRunner(ctx context.Context, tag string) {
	j.jjLock.Lock()
	defer j.jjLock.Unlock()

	var ok bool
	if _, ok = j.tag2JMap.Load(tag); ok {
		return // double check to prevent duplicate create jj runner
	}

	library.Logger.Info("create new journal.Journal", zap.String("tag", tag))
	jj, err := journal.NewJournal(
		journal.WithLogger(library.Logger.Named("journal."+tag)),
		journal.WithBufDirPath(filepath.Join(j.BufDirPath, tag)),
		journal.WithBufSizeByte(j.BufSizeBytes),
		journal.WithIsCompress(j.IsCompress),
		journal.WithCommitIDTTL(j.CommittedIDTTL),
		journal.WithIsAggresiveGC(false),
	)
	if err != nil {
		library.Logger.Panic("new journal", zap.Error(err))
	}
	if err = jj.Start(ctx); err != nil {
		library.Logger.Panic("run journal", zap.Error(err))
	}

	if _, ok = j.tag2JMap.LoadOrStore(tag, jj); ok {
		library.Logger.Panic("tag already exists in tag2JMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2JJCommitChanMap.LoadOrStore(tag, make(chan *library.FluentMsg, j.ChildJournalIDInchanLen)); ok {
		library.Logger.Panic("tag already exists in tag2JJCommitChanMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2JJInchanMap.LoadOrStore(tag, make(chan *library.FluentMsg, j.ChildJournalDataInchanLen)); ok {
		library.Logger.Panic("tag already exists in tag2JJInchanMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2IDsCounter.LoadOrStore(tag, utils.NewCounter()); ok {
		library.Logger.Panic("tag already exists in tag2IDsCounter", zap.String("tag", tag))
	}
	if _, ok = j.tag2DataCounter.LoadOrStore(tag, utils.NewCounter()); ok {
		library.Logger.Panic("tag already exists in tag2DataCounter", zap.String("tag", tag))
	}

	// create ids writer
	go func() {
		var (
			mid             int64
			err             error
			nRetry          int
			maxRetry        = 2
			msg             *library.FluentMsg
			ok              bool
			chani, counteri interface{}
			msgChan         chan *library.FluentMsg
			counter         *utils.Counter
		)

		if chani, ok = j.tag2JJCommitChanMap.Load(tag); !ok {
			library.Logger.Panic("tag must in `j.tag2JJCommitChanMap`", zap.String("tag", tag))
		}
		msgChan = chani.(chan *library.FluentMsg)
		if counteri, ok = j.tag2IDsCounter.Load(tag); !ok {
			library.Logger.Panic("tag must in `j.tag2IDsCounter`", zap.String("tag", tag))
		}
		counter = counteri.(*utils.Counter)

		defer library.Logger.Info("journal ids writer exit")
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-msgChan:
				if !ok {
					library.Logger.Info("tag2JJCommitChan closed", zap.String("tag", tag))
					return
				}
			}

			counter.Count()
			nRetry = 0
			for nRetry < maxRetry {
				if err = jj.WriteId(msg.ID); err != nil {
					nRetry++
				}
				break
			}
			if err != nil && nRetry == maxRetry {
				library.Logger.Error("try to write id to journal got error", zap.Error(err))
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
					counter.Count()
					if err != nil && nRetry == maxRetry {
						library.Logger.Error("try to write id to journal got error", zap.Error(err))
					}
				}
				msg.ExtIds = nil
			}

			j.MsgPool.Put(msg)
		}
	}()

	// create data writer
	go func() {
		var (
			data            = &journal.Data{Data: map[string]interface{}{}}
			err             error
			nRetry          int
			maxRetry        = 2
			ok              bool
			msg             *library.FluentMsg
			chani, counteri interface{}
			msgChan         chan *library.FluentMsg
			counter         *utils.Counter
		)
		if chani, ok = j.tag2JJInchanMap.Load(tag); !ok {
			library.Logger.Panic("tag should in `j.tag2JJInchanMap`", zap.String("tag", tag))
		}
		msgChan = chani.(chan *library.FluentMsg)
		if counteri, ok = j.tag2DataCounter.Load(tag); !ok {
			library.Logger.Panic("tag should in `j.tag2DataCounter`", zap.String("tag", tag))
		}
		counter = counteri.(*utils.Counter)

		defer library.Logger.Info("journal data writer exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-msgChan:
				if !ok {
					library.Logger.Info("tag2JJInchan closed", zap.String("tag", tag))
					return
				}
			}

			data.ID = msg.ID
			data.Data["message"] = msg.Message
			data.Data["tag"] = msg.Tag
			nRetry = 0
			counter.Count()
			for nRetry < maxRetry {
				if err = jj.WriteData(data); err != nil {
					nRetry++
					continue
				}
				break
			}

			if err != nil && nRetry == maxRetry {
				library.Logger.Error("try to write msg to journal got error",
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

func (j *Journal) GetOutChan() chan *library.FluentMsg {
	return j.outChan
}

func (j *Journal) ConvertMsg2Buf(msg *library.FluentMsg, data *map[string]interface{}) {
	(*data)["id"] = msg.ID
	(*data)["tag"] = msg.Tag
	(*data)["message"] = msg.Message
}

func (j *Journal) DumpMsgFlow(ctx context.Context, msgPool *sync.Pool, dumpChan, skipDumpChan chan *library.FluentMsg) chan *library.FluentMsg {
	// deal with legacy
	go func() {
		defer library.Logger.Info("legacy processor exit")
		var err error
		for { // try to starting legacy loading
			select {
			case <-ctx.Done():
				return
			default:
				if _, err = j.ProcessLegacyMsg(dumpChan); err != nil {
					library.Logger.Error("process legacy got error", zap.Error(err))
				}
				time.Sleep(intervalToStartingLegacy)
			}
		}
	}()

	// start periodic gc
	go func() {
		defer library.Logger.Info("gc runner exit")
		for {
			select {
			case <-ctx.Done():
				return
			default:
				utils.ForceGCBlocking()
				time.Sleep(j.GCIntervalSec)
			}
		}
	}()

	// deal with msgs that skip dump
	go func() {
		var (
			msg *library.FluentMsg
			ok  bool
		)
		defer library.Logger.Info("skipDumpChan goroutine exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-skipDumpChan:
				if !ok {
					library.Logger.Info("skipDumpChan closed")
					return
				}

				j.outChan <- msg
			}
		}
	}()

	// deal with msgs that need dump
	go func() {
		var (
			ok  bool
			jji interface{}
			msg *library.FluentMsg
		)
		defer library.Logger.Info("legacy dumper exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-dumpChan:
				if !ok {
					library.Logger.Info("dumpChan closed")
					return
				}
			}

			library.Logger.Debug("try to dump msg", zap.String("tag", msg.Tag))
			if jji, ok = j.tag2JJInchanMap.Load(msg.Tag); !ok {
				j.createJournalRunner(ctx, msg.Tag)
				jji, _ = j.tag2JJInchanMap.Load(msg.Tag)
			}

			select {
			case jji.(chan *library.FluentMsg) <- msg:
			default:
				select {
				case jji.(chan *library.FluentMsg) <- msg:
				default:
					select {
					case j.outChan <- msg:
						library.Logger.Warn("skip dump since journal is busy", zap.String("tag", msg.Tag))
					default:
						library.Logger.Error("discard log since of journal & downstream busy",
							zap.String("tag", msg.Tag),
							zap.String("msg", fmt.Sprint(msg)),
						)
						j.MsgPool.Put(msg)
					}
				}
			}
		}
	}()

	return j.outChan
}

func (j *Journal) GetCommitChan() chan<- *library.FluentMsg {
	return j.commitChan
}

func (j *Journal) startCommitRunner(ctx context.Context) {
	go func() {
		var (
			ok    bool
			chani interface{}
			msg   *library.FluentMsg
		)
		defer library.Logger.Info("id commitor exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-j.commitChan:
				if !ok {
					library.Logger.Info("commitChan closed")
					return
				}
			}

			library.Logger.Debug("try to commit msg",
				zap.String("tag", msg.Tag),
				zap.Int64("id", msg.ID))
			if chani, ok = j.tag2JJCommitChanMap.Load(msg.Tag); !ok {
				j.createJournalRunner(ctx, msg.Tag)
				chani, _ = j.tag2JJCommitChanMap.Load(msg.Tag)
			}

			select {
			case chani.(chan *library.FluentMsg) <- msg:
			default:
				select {
				case j.commitChan <- msg:
					library.Logger.Warn("reset committed msg",
						zap.String("tag", msg.Tag),
						zap.Int64("id", msg.ID),
					)
				default:
					library.Logger.Error("discard committed msg because commitChan is busy",
						zap.String("tag", msg.Tag),
						zap.Int64("id", msg.ID),
					)
					j.MsgPool.Put(msg)
				}
			}
		}
	}()
}

func (j *Journal) registerMonitor() {
	monitor.AddMetric("journal", func() map[string]interface{} {
		result := map[string]interface{}{
			"config": map[string]interface{}{
				"compress":             j.IsCompress,
				"buf_dir_path":         j.BufDirPath,
				"buf_file_bytes":       j.BufSizeBytes,
				"gc_inteval_sec":       j.GCIntervalSec / time.Second,
				"journal_out_chan_len": j.JournalOutChanLen,
				"commit_id_chan_len":   j.CommitIDChanLen,
				"child_data_chan_len":  j.ChildJournalDataInchanLen,
				"child_id_chan_len":    j.ChildJournalIDInchanLen,
			},
		}
		j.tag2JMap.Range(func(k, v interface{}) bool {
			result[k.(string)+".journal"] = v.(*journal.Journal).GetMetric()
			return true
		})
		j.tag2IDsCounter.Range(func(k, v interface{}) bool {
			result[k.(string)+".ids.msgTotal"] = v.(*utils.Counter).Get()
			result[k.(string)+".ids.msgPerSec"] = v.(*utils.Counter).GetSpeed()
			return true
		})
		j.tag2DataCounter.Range(func(k, v interface{}) bool {
			result[k.(string)+".data.msgTotal"] = v.(*utils.Counter).Get()
			result[k.(string)+".data.msgPerSec"] = v.(*utils.Counter).GetSpeed()
			return true
		})
		j.tag2JJInchanMap.Range(func(k, v interface{}) bool {
			result[k.(string)+".chanLen"] = len(v.(chan *library.FluentMsg))
			result[k.(string)+".chanCap"] = cap(v.(chan *library.FluentMsg))
			return true
		})

		var err error
		if result["bufSize"], err = utils.DirSize(j.BufDirPath); err != nil {
			library.Logger.Error("load journal dir size", zap.Error(err), zap.String("dir", j.BufDirPath))
		}
		return result
	})
}
