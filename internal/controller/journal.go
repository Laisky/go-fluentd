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
	"gofluentd/library/log"
	"gofluentd/library/streamformat"

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
	// GroupCommitMaxMessages bounds reliable groups; 0 or 1 keeps per-record Sync.
	GroupCommitMaxMessages int
	BufDirPath             string
	BufSizeBytes           int64
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
	if err := j.valid(); err != nil {
		log.Logger.Panic("invalid", zap.Error(err))
	}

	j.commitChan = make(chan *library.FluentMsg, cfg.CommitIDChanLen)
	j.outChan = make(chan *library.FluentMsg, cfg.JournalOutChanLen)

	j.initLegacyJJ(ctx)
	j.registerMonitor()
	j.startCommitRunner(ctx)

	log.Logger.Info("new journal",
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
	if j.GroupCommitMaxMessages < 0 || j.GroupCommitMaxMessages > maximumGroupCommitMaxMessages {
		return fmt.Errorf("group_commit_max_messages must be between 0 and %d", maximumGroupCommitMaxMessages)
	}
	if j.GroupCommitMaxMessages == 0 {
		j.GroupCommitMaxMessages = defaultGroupCommitMaxMessages
	}

	if j.BufSizeBytes <= 0 {
		j.BufSizeBytes = defaultBufSizeByte
		log.Logger.Info("reset buf_file_bytes", zap.Int64("buf_file_bytes", j.BufSizeBytes))
	} else if j.BufSizeBytes < minimalBufSizeByte {
		log.Logger.Warn("journal buf file size too small", zap.Int64("size", j.BufSizeBytes))
	}

	if j.GCIntervalSec <= 0 {
		j.GCIntervalSec = defaultIntervalSecForceGC
		log.Logger.Info("reset gc_inteval_sec", zap.Duration("gc_inteval_sec", j.GCIntervalSec))
	}

	if j.JournalOutChanLen <= 0 {
		j.JournalOutChanLen = 10000
		log.Logger.Info("reset journal_out_chan_len", zap.Int("journal_out_chan_len", j.JournalOutChanLen))
	}

	if j.CommitIDChanLen <= 0 {
		j.CommitIDChanLen = 50000
		log.Logger.Info("reset commit_id_chan_len", zap.Int("commit_id_chan_len", j.CommitIDChanLen))
	}

	if j.ChildJournalDataInchanLen <= 0 {
		j.ChildJournalDataInchanLen = j.JournalOutChanLen
		log.Logger.Info("reset child_data_chan_len", zap.Int("child_data_chan_len", j.ChildJournalDataInchanLen))
	}

	if j.ChildJournalIDInchanLen <= 0 {
		j.ChildJournalIDInchanLen = j.CommitIDChanLen
		log.Logger.Info("reset child_id_chan_len", zap.Int("child_id_chan_len", j.ChildJournalIDInchanLen))
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
		log.Logger.Panic("tag must exists", zap.String("tag", tag))
	} else {
		close(inchan.(chan *library.FluentMsg))
		j.tag2JJInchanMap.Delete(tag)
	}

	if inchan, ok := j.tag2JJCommitChanMap.Load(tag); !ok {
		log.Logger.Panic("tag must exists", zap.String("tag", tag))
	} else {
		close(inchan.(chan *library.FluentMsg))
		j.tag2JJCommitChanMap.Delete(tag)
	}

	log.Logger.Info("delete journal tag", zap.String("tag", tag))
	return nil
}

// initLegacyJJ process existed legacy data and ids
func (j *Journal) initLegacyJJ(ctx context.Context) {
	files, err := ioutil.ReadDir(j.BufDirPath)
	if err != nil {
		log.Logger.Error("try to read dir of journal",
			zap.String("directory", j.BufDirPath),
			zap.Error(err))
		return
	}

	for _, dir := range files {
		if dir.IsDir() {
			if err := j.createJournalRunner(ctx, dir.Name()); err != nil {
				log.Logger.Panic("open retained journal", zap.Error(err))
			}
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

func (j *Journal) ProcessLegacyMsg(dumpChan chan *library.FluentMsg) (int64, error) {
	return j.processLegacyMsg(context.Background(), dumpChan)
}

// processLegacyMsg writes each retained record before publishing its in-memory
// copy. go-journal synchronizes replacement files before legacy cleanup.
// LoadLegacyBuf releases the legacy lock itself on EOF or error.
type legacyJournal interface {
	LockLegacy() bool
	UnLockLegacy() bool
	LoadLegacyBuf(*journal.Data) error
	WriteData(*journal.Data) error
}

func (j *Journal) processLegacyMsg(ctx context.Context, out chan *library.FluentMsg) (maxID int64, resultErr error) {
	if !j.legacyLock.TryLock() {
		return 0, fmt.Errorf("another legacy is running")
	}
	defer j.legacyLock.ForceRelease()
	var wg sync.WaitGroup
	var mu sync.Mutex
	j.tag2JMap.Range(func(k, v interface{}) bool {
		wg.Add(1)
		go func(tag string, jj legacyJournal) {
			defer wg.Done()
			var innerMax int64
			var replayErr error
			defer func() {
				mu.Lock()
				defer mu.Unlock()
				if innerMax > maxID {
					maxID = innerMax
				}
				if replayErr != nil && resultErr == nil {
					resultErr = errors.Wrapf(replayErr, "replay tag %s", tag)
				}
			}()
			if !jj.LockLegacy() {
				return
			}
			ownsLegacy := true
			defer func() {
				if ownsLegacy {
					jj.UnLockLegacy()
				}
			}()
			for {
				if replayErr = ctx.Err(); replayErr != nil {
					return
				}
				data := &journal.Data{Data: map[string]interface{}{}}
				if err := jj.LoadLegacyBuf(data); err != nil {
					// The backend has already released our lock. A deferred
					// unlock here could steal a concurrent rotation's lock.
					ownsLegacy = false
					if err != io.EOF {
						replayErr = err
					}
					return
				}
				// Preserve even malformed records before rejecting them. The
				// reader has advanced, so a later replay may reach cleanup.
				if replayErr = jj.WriteData(data); replayErr != nil {
					return
				}
				storedTag, tagOK := data.Data["tag"].(string)
				message, messageOK := data.Data["message"].(map[string]interface{})
				if !tagOK || !messageOK {
					replayErr = fmt.Errorf("invalid persisted message %d", data.ID)
					return
				}
				if data.ID > innerMax {
					innerMax = data.ID
				}
				format := ""
				if raw, exists := data.Data["source_format"]; exists {
					var ok bool
					format, ok = raw.(string)
					if !ok || !streamformat.ValidFormat(format) {
						replayErr = fmt.Errorf("invalid persisted source format for message %d", data.ID)
						return
					}
				}
				msg := j.MsgPool.Get().(*library.FluentMsg)
				*msg = library.FluentMsg{ID: data.ID, Tag: storedTag, JournalTag: tag, Message: message, SourceFormat: format}
				select {
				case out <- msg:
				case <-ctx.Done():
					j.MsgPool.Put(msg)
					replayErr = ctx.Err()
					return
				}
			}
		}(k.(string), v.(legacyJournal))
		return true
	})
	wg.Wait()
	return maxID, resultErr
}

// createJournalRunner create journal for a tag,
// and return commit channel and dump channel
func (j *Journal) createJournalRunner(ctx context.Context, tag string) error {
	j.jjLock.Lock()
	defer j.jjLock.Unlock()

	var ok bool
	if _, ok = j.tag2JMap.Load(tag); ok {
		return nil // double check to prevent duplicate create jj runner
	}

	log.Logger.Info("create new journal.Journal", zap.String("tag", tag))
	jj, err := journal.NewJournal(
		journal.WithLogger(log.Logger.Named("journal."+tag)),
		journal.WithBufDirPath(filepath.Join(j.BufDirPath, tag)),
		journal.WithBufSizeByte(j.BufSizeBytes),
		journal.WithIsCompress(j.IsCompress),
		journal.WithCommitIDTTL(j.CommittedIDTTL),
		journal.WithIsAggresiveGC(false),
	)
	if err != nil {
		return errors.Wrap(err, "new journal")
	}
	if err = jj.Start(ctx); err != nil {
		jj.Close()
		return errors.Wrap(err, "run journal")
	}

	if _, ok = j.tag2JMap.LoadOrStore(tag, jj); ok {
		log.Logger.Panic("tag already exists in tag2JMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2JJCommitChanMap.LoadOrStore(tag, make(chan *library.FluentMsg, j.ChildJournalIDInchanLen)); ok {
		log.Logger.Panic("tag already exists in tag2JJCommitChanMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2JJInchanMap.LoadOrStore(tag, make(chan *library.FluentMsg, j.ChildJournalDataInchanLen)); ok {
		log.Logger.Panic("tag already exists in tag2JJInchanMap", zap.String("tag", tag))
	}
	if _, ok = j.tag2IDsCounter.LoadOrStore(tag, utils.NewCounter()); ok {
		log.Logger.Panic("tag already exists in tag2IDsCounter", zap.String("tag", tag))
	}
	if _, ok = j.tag2DataCounter.LoadOrStore(tag, utils.NewCounter()); ok {
		log.Logger.Panic("tag already exists in tag2DataCounter", zap.String("tag", tag))
	}

	// create ids writer
	go func() {
		var (
			mid             int64
			err             error
			msg             *library.FluentMsg
			ok              bool
			chani, counteri interface{}
			msgChan         chan *library.FluentMsg
			counter         *utils.Counter
		)

		if chani, ok = j.tag2JJCommitChanMap.Load(tag); !ok {
			log.Logger.Panic("tag must in `j.tag2JJCommitChanMap`", zap.String("tag", tag))
		}
		msgChan = chani.(chan *library.FluentMsg)
		if counteri, ok = j.tag2IDsCounter.Load(tag); !ok {
			log.Logger.Panic("tag must in `j.tag2IDsCounter`", zap.String("tag", tag))
		}
		counter = counteri.(*utils.Counter)

		defer log.Logger.Info("journal ids writer exit")
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-msgChan:
				if !ok {
					log.Logger.Info("tag2JJCommitChan closed", zap.String("tag", tag))
					return
				}
			}

			counter.Count()
			err = writeCommittedID(jj.WriteId, msg.ID)
			if err != nil {
				log.Logger.Error("try to write id to journal got error", zap.Error(err))
			}

			if msg.ExtIds != nil {
				for _, mid = range msg.ExtIds {
					err = writeCommittedID(jj.WriteId, mid)
					counter.Count()
					if err != nil {
						log.Logger.Error("try to write id to journal got error", zap.Error(err))
					}
				}
				msg.ExtIds = nil
			}

			j.MsgPool.Put(msg)
		}
	}()

	// A single worker owns each tag's data writes and acceptance receipts.
	dataChan, _ := j.tag2JJInchanMap.Load(tag)
	dataCounter, _ := j.tag2DataCounter.Load(tag)
	go j.runDataWriter(ctx, tag, jj, dataChan.(chan *library.FluentMsg), dataCounter.(*utils.Counter))

	return nil
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
		defer log.Logger.Info("legacy processor exit")
		var err error
		for { // try to starting legacy loading
			select {
			case <-ctx.Done():
				return
			default:
				if _, err = j.processLegacyMsg(ctx, j.outChan); err != nil {
					log.Logger.Error("process legacy got error", zap.Error(err))
				}
				time.Sleep(intervalToStartingLegacy)
			}
		}
	}()

	// start periodic gc
	go func() {
		defer log.Logger.Info("gc runner exit")
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
		defer log.Logger.Info("skipDumpChan goroutine exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-skipDumpChan:
				if !ok {
					log.Logger.Info("skipDumpChan closed")
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
		defer log.Logger.Info("legacy dumper exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-dumpChan:
				if !ok {
					log.Logger.Info("dumpChan closed")
					return
				}
			}

			log.Logger.Debug("try to dump msg", zap.String("tag", msg.Tag))
			if jji, ok = j.tag2JJInchanMap.Load(msg.Tag); !ok {
				if err := j.createJournalRunner(ctx, msg.Tag); err != nil {
					log.Logger.Error("create message journal", zap.Error(err))
					msg.CompleteAcceptance(err)
					j.MsgPool.Put(msg)
					continue
				}
				jji, _ = j.tag2JJInchanMap.Load(msg.Tag)
			}
			if msg.DurableAck != nil {
				select {
				case jji.(chan *library.FluentMsg) <- msg:
				case <-ctx.Done():
					msg.CompleteAcceptance(ctx.Err())
					j.MsgPool.Put(msg)
					return
				}
				continue
			}

			select {
			case jji.(chan *library.FluentMsg) <- msg:
			default:
				select {
				case jji.(chan *library.FluentMsg) <- msg:
				default:
					select {
					case j.outChan <- msg:
						log.Logger.Warn("skip dump since journal is busy", zap.String("tag", msg.Tag))
					default:
						log.Logger.Error("discard log since of journal & downstream busy",
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
		defer log.Logger.Info("id commitor exit", zap.String("msg", fmt.Sprint(msg)))
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok = <-j.commitChan:
				if !ok {
					log.Logger.Info("commitChan closed")
					return
				}
			}

			log.Logger.Debug("try to commit msg",
				zap.String("tag", msg.Tag),
				zap.Int64("id", msg.ID))
			commitTag := msg.JournalTag
			if commitTag == "" {
				// Explicit skip-dump paths have no persisted journal owner.
				commitTag = msg.Tag
			}
			if chani, ok = j.tag2JJCommitChanMap.Load(commitTag); !ok {
				if err := j.createJournalRunner(ctx, commitTag); err != nil {
					log.Logger.Error("create acknowledgement journal", zap.Error(err))
					j.MsgPool.Put(msg)
					continue
				}
				chani, _ = j.tag2JJCommitChanMap.Load(commitTag)
			}

			select {
			case chani.(chan *library.FluentMsg) <- msg:
			default:
				select {
				case j.commitChan <- msg:
					log.Logger.Warn("reset committed msg",
						zap.String("tag", msg.Tag),
						zap.Int64("id", msg.ID),
					)
				default:
					log.Logger.Error("discard committed msg because commitChan is busy",
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
			log.Logger.Error("load journal dir size", zap.Error(err), zap.String("dir", j.BufDirPath))
		}
		return result
	})
}

// writeCommittedID centralizes acknowledgement-write retry handling.
func writeCommittedID(write func(int64) error, id int64) (err error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err = write(id); err == nil {
			return nil
		}
	}
	return err
}
