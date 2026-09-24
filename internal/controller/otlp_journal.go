package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

var (
	ErrOTLPJournalClosed   = errors.New("OTLP journal closed")
	ErrOTLPJournalCapacity = errors.New("OTLP journal admission byte limit reached")
	ErrOTLPJournalFault    = errors.New("OTLP journal stopped after storage or identity failure")
)

// OTLPJournalConfig requires an existing, private persistent directory. The
// directory is an ownership domain: generation, WAL and receipts stay together.
// MaxWALBytes is an admission threshold for WAL file bytes, NOT a filesystem or
// receipt quota. Recovery needs extra space for copying pending records.
type OTLPJournalConfig struct {
	Directory      string
	Compress       bool
	Limits         otlpstate.Limits
	MaxWALBytes    int64
	ReplayBatch    int
	ReplayInterval time.Duration
}

// OTLPBatchReport counts envelopes in this pass, not telemetry data points.
// Complete means this frozen replay snapshot reached EOF, not all delivery done.
type OTLPBatchReport struct {
	Seen, Released, Delivered, Quarantined, Pending int
	Complete                                        bool
}

// OTLPJournal owns the dedicated WAL, disposition store and producer. It does
// not mount a listener or touch the legacy log journal. Admit is suitable for
// otlphttp.Admission. Run drives bounded replay on a fixed cadence; ReplayBatch
// is also available to explicit schedulers. Close cancels active operations and
// waits for them. Destination callbacks must honor their context.
type OTLPJournal struct {
	life                       sync.RWMutex
	closed                     bool
	ctx                        context.Context
	cancel                     context.CancelFunc
	cfg                        OTLPJournalConfig
	namespace, walDir          string
	wal                        *journal.Journal
	store                      *otlpstate.Store
	producer                   *OTLPProducer
	walGate, passGate, runGate chan struct{}
	frontier                   int64 // protected by walGate; recovered from data AND ACK segments
	replayOpen                 bool  // protected by passGate; never rotate a partially read snapshot
	faultMu                    sync.Mutex
	fault                      error
	// Narrow instance seams supplement real filesystem/crash behavior tests.
	syncWAL  func() error
	writeWAL func(*journal.Data) error
}

// OpenOTLPJournal creates metadata only in an empty provisioned directory. An
// existing directory without valid generation metadata is never adopted as a
// fresh journal. A partially initialized directory must be inspected, not reset.
func OpenOTLPJournal(ctx context.Context, cfg OTLPJournalConfig, peers []OTLPDestination) (_ *OTLPJournal, err error) {
	if ctx == nil {
		return nil, errors.New("nil OTLP journal context")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Limits == (otlpstate.Limits{}) {
		cfg.Limits = otlpstate.DefaultLimits()
	}
	if cfg.MaxWALBytes == 0 {
		cfg.MaxWALBytes = 256 << 20
	}
	if cfg.ReplayBatch == 0 {
		cfg.ReplayBatch = 64
	}
	if cfg.ReplayInterval == 0 {
		cfg.ReplayInterval = time.Second
	}
	if cfg.MaxWALBytes < 4096 || cfg.MaxWALBytes > 1<<50 || cfg.ReplayBatch < 1 || cfg.ReplayBatch > 1024 || cfg.ReplayInterval < 10*time.Millisecond || cfg.ReplayInterval > time.Hour {
		return nil, errors.New("invalid OTLP journal admission or replay limits")
	}
	store, err := otlpstate.Open(cfg.Directory, cfg.Limits)
	if err != nil {
		return nil, err
	}
	p := &OTLPJournal{cfg: cfg, store: store, walGate: make(chan struct{}, 1), passGate: make(chan struct{}, 1), runGate: make(chan struct{}, 1)}
	p.ctx, p.cancel = context.WithCancel(ctx)
	defer func() {
		if err != nil {
			p.cancel()
			if p.wal != nil {
				p.wal.Close()
			}
			store.Close()
		}
	}()
	if p.producer, err = NewOTLPProducer(store, peers); err != nil {
		return nil, err
	}
	if p.namespace, p.walDir, err = openOTLPGeneration(cfg.Directory); err != nil {
		return nil, err
	}
	// Only this owner rotates snapshots. Background rotation would invalidate a
	// bounded replay cursor; periodic flush is unnecessary because every operation
	// explicitly synchronizes. The upstream workers still stop normally on Close.
	never := time.Duration(math.MaxInt64)
	p.wal, err = journal.NewJournal(journal.WithBufDirPath(p.walDir), journal.WithIsCompress(cfg.Compress), journal.WithIsAggresiveGC(false), journal.WithBufSizeByte(1<<20), journal.WithFlushInterval(never), journal.WithRotateDuration(never), journal.WithRotateCheckInterval(never))
	if err != nil {
		return nil, err
	}
	if err = p.wal.Start(p.ctx); err != nil {
		return nil, err
	}
	if p.frontier, err = p.wal.LoadMaxId(); err != nil {
		return nil, err
	}
	p.syncWAL, p.writeWAL = p.wal.Sync, p.wal.WriteData
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

func openOTLPGeneration(dir string) (string, string, error) {
	path, wal := filepath.Join(dir, "generation.json"), filepath.Join(dir, "wal")
	type generation struct {
		Version   int    `json:"version"`
		Namespace string `json:"namespace"`
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return "", "", e
		}
		for _, item := range entries {
			if item.Name() != ".otlp-disposition.lock" {
				return "", "", errors.New("OTLP storage has no generation metadata; refusing to adopt existing data")
			}
		}
		var seed [32]byte
		if _, e = rand.Read(seed[:]); e != nil {
			return "", "", e
		}
		g := generation{1, hex.EncodeToString(seed[:])}
		if e = os.Mkdir(wal, 0700); e != nil {
			return "", "", e
		}
		b, e := json.Marshal(g)
		if e != nil {
			return "", "", e
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return "", "", e
		}
		n, e := f.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e == nil {
			e = f.Sync()
		}
		e = errors.Join(e, f.Close())
		if e == nil {
			var df *os.File
			df, e = os.Open(dir)
			if e == nil {
				e = errors.Join(df.Sync(), df.Close())
			}
		}
		if e != nil {
			return "", "", e
		} // Leave evidence on failure; never reset identity.
		return g.Namespace, wal, nil
	}
	if err != nil {
		return "", "", err
	}
	if !st.Mode().IsRegular() || st.Size() > 1024 {
		return "", "", errors.New("invalid OTLP generation file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	var g generation
	d := json.NewDecoder(io.LimitReader(f, 1025))
	d.DisallowUnknownFields()
	if err = d.Decode(&g); err != nil {
		return "", "", err
	}
	var extra interface{}
	seed, e := hex.DecodeString(g.Namespace)
	if d.Decode(&extra) != io.EOF || g.Version != 1 || e != nil || len(seed) != 32 || hex.EncodeToString(seed) != g.Namespace {
		return "", "", errors.New("invalid OTLP generation metadata")
	}
	st, err = os.Lstat(wal)
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", errors.New("OTLP WAL must be a dedicated non-symlink directory")
	}
	if err = f.Sync(); err != nil {
		return "", "", err
	}
	return g.Namespace, wal, nil
}

func (p *OTLPJournal) Namespace() string      { return p.namespace }
func (p *OTLPJournal) Counters() OTLPCounters { return p.producer.Counters() }

// Err returns a sticky storage/identity fault. Retryable peer rejection is not
// a storage fault; known terminal rejections are retained by the producer.
func (p *OTLPJournal) Err() error { p.faultMu.Lock(); defer p.faultMu.Unlock(); return p.fault }
func (p *OTLPJournal) poison(err error) error {
	p.faultMu.Lock()
	defer p.faultMu.Unlock()
	if p.fault == nil {
		p.fault = errors.Join(ErrOTLPJournalFault, err)
	}
	return p.fault
}
func (p *OTLPJournal) acquire(ctx context.Context, gate chan struct{}) error {
	if ctx == nil {
		return errors.New("nil OTLP operation context")
	}
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrOTLPJournalClosed
	}
	if err := ctx.Err(); err != nil {
		<-gate
		return err
	}
	if p.ctx.Err() != nil {
		<-gate
		return ErrOTLPJournalClosed
	}
	if err := p.Err(); err != nil {
		<-gate
		return err
	}
	return nil
}
func (p *OTLPJournal) storageBytes() (int64, error) {
	entries, err := os.ReadDir(p.walDir)
	if err != nil {
		return 0, err
	}
	var size int64
	for _, e := range entries {
		st, err := e.Info()
		if err != nil {
			return 0, err
		}
		if !st.Mode().IsRegular() {
			return 0, fmt.Errorf("unexpected OTLP WAL entry %q", e.Name())
		}
		if st.Size() > math.MaxInt64-size {
			return 0, errors.New("OTLP WAL size overflow")
		}
		size += st.Size()
	}
	return size, nil
}

// Admit returns nil only after the frozen plan and original payload are written
// and synchronized. Cancellation after writing can still leave a durable record;
// errors never promise rollback. No asynchronous in-memory enqueue is an ACK.
func (p *OTLPJournal) Admit(ctx context.Context, request *otlpwire.Request) error {
	p.life.RLock()
	defer p.life.RUnlock()
	if p.closed {
		return ErrOTLPJournalClosed
	}
	if request == nil {
		return errors.New("nil OTLP request")
	}
	if err := p.acquire(ctx, p.walGate); err != nil {
		return err
	}
	defer func() { <-p.walGate }()
	if p.frontier == math.MaxInt64 {
		return errors.New("OTLP journal identity space exhausted")
	}
	e := otlpstate.Envelope{Signal: string(request.Signal()), ContentType: request.ContentType(), Items: int64(request.Items()), Payload: request.Payload()}
	r, err := p.producer.Plan(p.namespace, p.frontier+1, e)
	if err != nil {
		return err
	}
	d, err := r.JournalData()
	if err != nil {
		return err
	}
	size, err := p.storageBytes()
	if err != nil {
		return p.poison(err)
	}
	// Conservative room for the encoded wrapper, compression overhead and framing.
	// Replay copies/receipts are intentionally outside this admission threshold.
	reserve := int64(len(d.Data["otlp_delivery"].([]byte)))*2 + 1024
	if size > p.cfg.MaxWALBytes-reserve {
		return ErrOTLPJournalCapacity
	}
	p.frontier = r.ID
	if err = p.writeWAL(d); err != nil {
		return p.poison(err)
	}
	if err = p.syncWAL(); err != nil {
		return p.poison(err)
	}
	return ctx.Err()
}

// ReplayBatch processes at most the configured number of pending envelopes.
// It never rotates a partially consumed snapshot. Each loaded record is copied
// into the active WAL and synchronized BEFORE the next load can trigger cleanup.
func (p *OTLPJournal) ReplayBatch(ctx context.Context) (report OTLPBatchReport, err error) {
	p.life.RLock()
	defer p.life.RUnlock()
	if p.closed {
		return report, ErrOTLPJournalClosed
	}
	if err = p.acquire(ctx, p.passGate); err != nil {
		return report, err
	}
	defer func() { <-p.passGate }()
	callCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer func() { stop(); cancel() }()
	if err = p.acquire(callCtx, p.walGate); err != nil {
		return report, err
	}
	if !p.replayOpen {
		if err = p.wal.Rotate(callCtx); err != nil {
			<-p.walGate
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return report, err
			}
			return report, p.poison(err)
		}
		p.replayOpen = true
	}
	if !p.wal.LockLegacy() {
		<-p.walGate
		return report, p.poison(errors.New("OTLP replay ownership unavailable"))
	}
	<-p.walGate
	defer p.wal.UnLockLegacy()
	var pending []error
	for report.Seen < p.cfg.ReplayBatch {
		if err = p.acquire(callCtx, p.walGate); err != nil {
			return report, errors.Join(append(pending, err)...)
		}
		d := new(journal.Data)
		err = p.wal.LoadLegacyBuf(d)
		if err == io.EOF {
			p.replayOpen = false
			report.Complete = true
			<-p.walGate
			return report, errors.Join(pending...)
		}
		if err != nil {
			<-p.walGate
			return report, p.poison(err)
		}
		r, decodeErr := OTLPRecordFromJournal(d)
		if decodeErr != nil || r.Namespace != p.namespace {
			<-p.walGate
			return report, p.poison(errors.Join(errors.New("OTLP WAL identity or wrapper mismatch"), decodeErr))
		}
		if err = p.writeWAL(d); err == nil {
			err = p.syncWAL()
		}
		<-p.walGate
		if err != nil {
			return report, p.poison(err)
		}
		report.Seen++
		delivery, sendErr := p.producer.Process(callCtx, r, func(c context.Context, id int64) error {
			if e := p.acquire(c, p.walGate); e != nil {
				return e
			}
			defer func() { <-p.walGate }()
			if e := p.wal.WriteId(id); e != nil {
				return p.poison(e)
			}
			if e := p.syncWAL(); e != nil {
				return p.poison(e)
			}
			return nil
		})
		if delivery.JournalReleased {
			report.Released++
			if delivery.FullyDelivered {
				report.Delivered++
			} else {
				report.Quarantined++
			}
		} else {
			report.Pending++
		}
		if sendErr != nil {
			if errors.Is(sendErr, otlpstate.ErrUncertain) || errors.Is(sendErr, otlpstate.ErrCorrupt) || errors.Is(sendErr, otlpstate.ErrConflict) || p.Err() != nil {
				return report, p.poison(sendErr)
			}
			pending = append(pending, sendErr)
		}
	}
	return report, errors.Join(pending...)
}

// Run uses a fixed minimum pause between batches, including retry exhaustion.
// The exporter's Retry-After adds its own per-signal not-before bound. Neither
// timer is durable across restart. No unbounded pending-envelope map is built.
func (p *OTLPJournal) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil OTLP scheduler context")
	}
	select {
	case p.runGate <- struct{}{}:
	default:
		return errors.New("OTLP scheduler already running")
	}
	defer func() { <-p.runGate }()
	for {
		if _, err := p.ReplayBatch(ctx); err != nil {
			if p.Err() != nil {
				return p.Err()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if p.ctx.Err() != nil || errors.Is(err, ErrOTLPJournalClosed) {
				return ErrOTLPJournalClosed
			}
		}
		timer := time.NewTimer(p.cfg.ReplayInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-p.ctx.Done():
			timer.Stop()
			return ErrOTLPJournalClosed
		}
	}
}

// Close first cancels active transports, then waits before releasing either
// ownership lock. A failed last Sync is returned even though resources close.
func (p *OTLPJournal) Close() error {
	p.cancel()
	p.life.Lock()
	defer p.life.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	err := p.wal.Sync()
	p.wal.Close()
	return errors.Join(err, p.store.Close(), p.Err())
}
