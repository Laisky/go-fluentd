package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

var (
	ErrOTLPJournalClosed     = errors.New("OTLP journal closed")
	ErrOTLPJournalCapacity   = errors.New("OTLP journal admission byte limit reached")
	ErrOTLPJournalScanBudget = errors.New("OTLP journal admission storage scan budget exhausted")
	ErrOTLPJournalFault      = errors.New("OTLP journal stopped after storage or identity failure")
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
	// MaxStorageBytes is an optional whole-directory admission threshold, not a filesystem quota.
	MaxStorageBytes int64
	// StorageScanMaxEntries caps each admission metadata scan. Exhaustion refuses
	// telemetry with ErrOTLPJournalScanBudget; it never treats a partial sum as free space.
	StorageScanMaxEntries int
	// StorageScanTimeout bounds metadata work between syscalls, not kernel I/O.
	StorageScanTimeout time.Duration
	// ReceiptGC enables checkpoint-proven reclamation of accepted receipts only.
	ReceiptGC bool
}

// OTLPBatchReport counts envelopes in this pass, not telemetry data points.
// Complete means this frozen replay snapshot reached EOF, not all delivery done.
type OTLPBatchReport struct {
	Seen, Released, Delivered, Quarantined, Pending int
	CheckpointSkipped                               int
	Complete                                        bool
	fresh                                           bool // this call opened a new snapshot; an empty continuation is not an empty WAL
}

// OTLPJournal owns the dedicated WAL, disposition store and producer. It does
// not mount a listener or touch the legacy log journal. Admit is suitable for
// otlphttp.Admission. Run drives bounded replay with a retry cadence; ReplayBatch
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
	wake                       chan struct{} // coalesced successful admission or sticky fault; never one entry per record
	frontier                   int64         // protected by walGate; recovered from data AND ACK segments
	replayOpen                 bool          // protected by passGate; never rotate a partially read snapshot
	faultMu                    sync.Mutex
	fault                      error
	generation                 otlpGeneration
	snapshotFrontier           int64 // frozen when Rotate succeeds, protected by passGate
	snapshotPending            int64 // smallest unresolved ID across the entire snapshot
	hasSnapshotPending         bool
	storageRejected            atomic.Uint64
	scanBudgetRejected         atomic.Uint64
	receiptsReclaimed          atomic.Uint64
	// Narrow instance seams supplement real filesystem/crash behavior tests.
	syncWAL  func() error
	writeWAL func(*journal.Data) error
	scanWAL  func(context.Context) (int64, error)
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
	if cfg.StorageScanMaxEntries == 0 {
		cfg.StorageScanMaxEntries = 4096
	}
	if cfg.StorageScanMaxEntries < 1 || cfg.StorageScanMaxEntries > 1<<20 {
		return nil, errors.New("storage_scan_max_entries must be between 1 and 1048576")
	}
	if cfg.StorageScanTimeout == 0 {
		cfg.StorageScanTimeout = 25 * time.Millisecond
	}
	if cfg.StorageScanTimeout < time.Millisecond || cfg.StorageScanTimeout > time.Second {
		return nil, errors.New("storage_scan_timeout must be between 1ms and 1s")
	}
	if cfg.MaxStorageBytes < 0 || cfg.MaxStorageBytes > 1<<50 || (cfg.MaxStorageBytes > 0 && cfg.MaxStorageBytes < cfg.MaxWALBytes) {
		return nil, errors.New("max_storage_bytes must be zero or at least max_wal_bytes")
	}
	if cfg.MaxWALBytes < 4096 || cfg.MaxWALBytes > 1<<50 || cfg.ReplayBatch < 1 || cfg.ReplayBatch > 1024 || cfg.ReplayInterval < 10*time.Millisecond || cfg.ReplayInterval > time.Hour {
		return nil, errors.New("invalid OTLP journal admission or replay limits")
	}
	store, err := otlpstate.Open(cfg.Directory, cfg.Limits)
	if err != nil {
		return nil, err
	}
	p := &OTLPJournal{cfg: cfg, store: store, walGate: make(chan struct{}, 1), passGate: make(chan struct{}, 1), runGate: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
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
	if p.generation, p.walDir, err = openOTLPGeneration(cfg.Directory); err != nil {
		return nil, err
	}
	p.namespace = p.generation.Namespace
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
	if p.generation.Version == 2 {
		p.frontier = max(p.frontier, p.generation.ReleasedThrough)
	}
	p.syncWAL, p.writeWAL = p.wal.Sync, p.wal.WriteData
	p.scanWAL = p.storageBytes
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *OTLPJournal) Namespace() string      { return p.namespace }
func (p *OTLPJournal) Counters() OTLPCounters { return p.producer.Counters() }

// StorageCounters returns process-local admission rejections and reclaimed receipts.
func (p *OTLPJournal) StorageCounters() (uint64, uint64) {
	return p.storageRejected.Load(), p.receiptsReclaimed.Load()
}

// ScanBudgetRejected is the subset of admission rejections caused by metadata
// entry/time budgets rather than measured byte thresholds; process-local only.
func (p *OTLPJournal) ScanBudgetRejected() uint64 { return p.scanBudgetRejected.Load() }

func (p *OTLPJournal) rejectScanBudget() error {
	p.storageRejected.Add(1)
	p.scanBudgetRejected.Add(1)
	return errors.Join(ErrOTLPJournalCapacity, ErrOTLPJournalScanBudget)
}

// Err returns a sticky storage/identity fault. Retryable peer rejection is not
// a storage fault; known terminal rejections are retained by the producer.
func (p *OTLPJournal) Err() error { p.faultMu.Lock(); defer p.faultMu.Unlock(); return p.fault }
func (p *OTLPJournal) poison(err error) error {
	p.faultMu.Lock()
	defer p.faultMu.Unlock()
	if p.fault == nil {
		p.fault = errors.Join(ErrOTLPJournalFault, err)
	}
	p.signalWake()
	return p.fault
}
func (p *OTLPJournal) signalWake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
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
func (p *OTLPJournal) storageBytes(ctx context.Context) (int64, error) {
	f, err := os.Open(p.walDir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var size int64
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entries, readErr := f.ReadDir(256)
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			if seen == p.cfg.StorageScanMaxEntries {
				return 0, ErrOTLPJournalScanBudget
			}
			seen++
			st, err := e.Info()
			if err != nil {
				return 0, err
			}
			if !st.Mode().IsRegular() {
				return 0, fmt.Errorf("unexpected OTLP WAL entry %q", e.Name())
			}
			if st.Size() < 0 || st.Size() > math.MaxInt64-size {
				return 0, errors.New("OTLP WAL size overflow")
			}
			size += st.Size()
		}
		if errors.Is(readErr, io.EOF) {
			return size, nil
		}
		if readErr != nil {
			return 0, readErr
		}
	}
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
	// Conservative room for the encoded wrapper, compression overhead and framing.
	// Replay copies/receipts are intentionally outside this admission threshold.
	reserve := int64(len(d.Data["otlp_delivery"].([]byte)))*2 + 1024
	// An envelope that cannot fit even an empty WAL needs no directory scan.
	if reserve > p.cfg.MaxWALBytes {
		p.storageRejected.Add(1)
		return ErrOTLPJournalCapacity
	}
	scanCtx, cancelScan := context.WithTimeout(ctx, p.cfg.StorageScanTimeout)
	defer cancelScan()
	size, err := p.scanWAL(scanCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrOTLPJournalScanBudget) || scanCtx.Err() != nil {
			return p.rejectScanBudget()
		}
		return p.poison(err)
	}
	if size > p.cfg.MaxWALBytes-reserve {
		p.storageRejected.Add(1)
		return ErrOTLPJournalCapacity
	}
	if p.cfg.MaxStorageBytes > 0 {
		used, scanErr := otlpDirectoryBytes(scanCtx, p.cfg.Directory, p.cfg.MaxStorageBytes-reserve, p.cfg.StorageScanMaxEntries)
		if scanErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(scanErr, ErrOTLPJournalScanBudget) || scanCtx.Err() != nil {
				return p.rejectScanBudget()
			}
			return scanErr
		}
		if used > p.cfg.MaxStorageBytes-reserve {
			p.storageRejected.Add(1)
			return ErrOTLPJournalCapacity
		}
	}
	// A scan can end exactly as its budget expires. Refuse before any write;
	// parent cancellation retains its own meaning instead of poisoning storage.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanCtx.Err() != nil {
		return p.rejectScanBudget()
	}
	cancelScan()
	p.frontier = r.ID
	if err = p.writeWAL(d); err != nil {
		return p.poison(err)
	}
	if err = p.syncWAL(); err != nil {
		return p.poison(err)
	}
	p.signalWake() // even cancellation after Sync leaves a record requiring delivery
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
		report.fresh = true
		if err = p.wal.Rotate(callCtx); err != nil {
			<-p.walGate
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return report, err
			}
			return report, p.poison(err)
		}
		p.replayOpen = true
		p.snapshotFrontier = p.frontier
		p.hasSnapshotPending = false
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
			cutoff := p.snapshotFrontier
			if p.hasSnapshotPending {
				cutoff = p.snapshotPending - 1
			}
			gcErr := p.checkpointAndPrune(callCtx, cutoff)
			<-p.walGate
			if gcErr != nil {
				if callCtx.Err() != nil {
					return report, gcErr
				}
				return report, p.poison(gcErr)
			}
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
		if r.ID > p.snapshotFrontier {
			<-p.walGate
			return report, p.poison(errors.New("OTLP replay exceeds frozen frontier"))
		}
		if p.generation.Version == 2 && r.ID <= p.generation.ReleasedThrough {
			report.Seen++
			report.CheckpointSkipped++
			<-p.walGate
			continue
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
			if !p.hasSnapshotPending || r.ID < p.snapshotPending {
				p.snapshotPending, p.hasSnapshotPending = r.ID, true
			}
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

// Run drains healthy full batches without an artificial inter-batch delay.
// Empty or unresolved batches keep the configured pause, including retry
// exhaustion. The exporter's Retry-After adds its per-signal not-before bound.
// Neither timer is durable across restart. No pending-envelope map is built.
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
		report, err := p.ReplayBatch(ctx)
		if err != nil {
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
		if err == nil && report.fresh && report.Complete && report.Seen == 0 {
			if err = p.waitForAdmission(ctx); err != nil {
				return err
			}
			continue
		}
		// A healthy full batch is evidence of useful backlog, not a reason
		// to throttle delivery. Retryable/failed batches still take the full
		// configured pause; empty snapshots also pause, avoiding a busy loop.
		if err == nil && report.Pending == 0 && !report.Complete && report.Seen == p.cfg.ReplayBatch {
			continue
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

// A fresh empty snapshot proves there is no work yet. Admission signals are
// buffered and are never cleared ahead of a snapshot, so concurrent new data
// cannot lose its wakeup. Idle ticks check storage health without rotating and
// allocating new WAL buffers; the retry cadence of nonempty work is unchanged.
func (p *OTLPJournal) waitForAdmission(ctx context.Context) error {
	ticker := time.NewTicker(p.cfg.ReplayInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.ctx.Done():
			return ErrOTLPJournalClosed
		case <-p.wake:
			return nil
		case <-ticker.C:
			if err := p.Err(); err != nil {
				return err
			}
			st, err := os.Stat(p.walDir)
			if err != nil {
				return p.poison(err)
			}
			if !st.IsDir() {
				return p.poison(errors.New("OTLP WAL directory replaced"))
			}
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
