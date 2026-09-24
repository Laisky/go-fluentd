// Package otlpstate retains terminal OTLP destination outcomes separately from
// delivery acknowledgements. It does not register exporters or acknowledge WALs.
package otlpstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrClosed    = errors.New("OTLP disposition store closed")
	ErrConflict  = errors.New("OTLP identity reused with different content")
	ErrCorrupt   = errors.New("invalid OTLP disposition record")
	ErrUncertain = errors.New("OTLP disposition persistence uncertain; stop and inspect storage")
)

type Kind string

const (
	Accepted  Kind = "accepted"
	Retryable Kind = "retryable"
	Partial   Kind = "partially_rejected"
	Permanent Kind = "permanently_rejected"
	Invalid   Kind = "invalid_response"
)

func (k Kind) terminal() bool { return k == Partial || k == Permanent || k == Invalid }

// Key must remain stable across replay/reconfiguration. Destination is an opaque
// logical destination ID, NOT a credential-bearing URL. Journal scopes RecordID.
type Key struct {
	Destination string `json:"destination"`
	Journal     string `json:"journal"`
	RecordID    int64  `json:"record_id"`
}

// Envelope keeps the original uncompressed Export request, not a decoded map.
// Items is the known log/span/data-point count, not metric descriptor count.
type Envelope struct {
	Signal      string `json:"signal"`
	ContentType string `json:"content_type"`
	Items       int64  `json:"items"`
	Payload     []byte `json:"payload"`
}

// Outcome is classified by the transport, which owns response validation. A
// count cannot identify which items failed. Response holds bounded raw peer
// bytes; Truncated must be set when only a prefix could be retained. Never copy
// Authorization or arbitrary response headers into diagnostic records.
type Outcome struct {
	Kind                Kind   `json:"kind"`
	RejectedItems       int64  `json:"rejected_items"`
	HTTPStatus          int    `json:"http_status"`
	Diagnostic          string `json:"diagnostic"`
	ResponseContentType string `json:"response_content_type"`
	Response            []byte `json:"response"`
	Truncated           bool   `json:"truncated"`
}

// Result requires explicit classification: nil error is NOT full delivery.
// Quarantined means a terminal result was durably retained, never a delivery ACK.
type Result struct {
	Outcome     Outcome
	Quarantined bool
	Replayed    bool
	// Durable marks a synchronized destination outcome. Accepted outcomes are
	// durable only through DoDelivery; Do preserves its terminal-only policy.
	Durable bool
}

// Limits bounds individual records, not aggregate memory or total disk usage.
type Limits struct{ PayloadBytes, ResponseBytes int }

func DefaultLimits() Limits { return Limits{4 << 20, 1 << 20} }
func (l Limits) valid() bool {
	return l.PayloadBytes > 0 && l.PayloadBytes <= 64<<20 && l.ResponseBytes > 0 && l.ResponseBytes <= 8<<20
}

type entry struct {
	Version    int      `json:"version"`
	Key        Key      `json:"key"`
	Envelope   Envelope `json:"envelope"`
	Outcome    Outcome  `json:"outcome"`
	RecordedAt string   `json:"recorded_at"`
}
type diskRecord struct {
	SHA256 string          `json:"sha256"`
	Entry  json.RawMessage `json:"entry"`
}

// Store owns an existing application-controlled directory. Records and temporary
// evidence are retained indefinitely; do not delete them while WALs can replay.
// Different key stripes may execute concurrently. Close waits for in-flight Do
// callbacks, which must honor their context. Do not call Store methods from send.
type Store struct {
	life              sync.RWMutex
	closed            bool
	dir               string
	dirFile, lockFile *os.File
	limits            Limits
	stripes           [64]chan struct{}
	faultMu           sync.Mutex
	fault             error
	// Per-instance I/O operations permit deterministic durability-fault tests.
	syncRecord    func(*os.File) error
	syncDirectory func(*os.File) error
	linkRecord    func(string, string) error
}

// Open requires an existing non-symlink directory, provisioned on persistent
// storage by the caller. That directory's creation must itself be durable. Never
// unlink .otlp-disposition.lock, including after Close or a process crash.
func Open(dir string, limits Limits) (*Store, error) {
	if dir == "" {
		return nil, errors.New("disposition directory is required")
	}
	if !limits.valid() {
		return nil, errors.New("invalid OTLP disposition limits")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, errors.New("disposition path must be an existing directory, not a symlink")
	}
	lock, err := acquireLock(filepath.Join(abs, ".otlp-disposition.lock"))
	if err != nil {
		return nil, err
	}
	df, err := os.Open(abs)
	if err != nil {
		lock.Close()
		return nil, err
	}
	// Persist the lock inode and any final link that survived an earlier crash.
	if err = df.Sync(); err != nil {
		df.Close()
		lock.Close()
		return nil, err
	}
	s := &Store{dir: abs, dirFile: df, lockFile: lock, limits: limits,
		syncRecord: (*os.File).Sync, syncDirectory: (*os.File).Sync, linkRecord: os.Link}
	for i := range s.stripes {
		s.stripes[i] = make(chan struct{}, 1)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.dirFile.Close(), s.lockFile.Close())
}

func validText(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func (s *Store) validate(k Key, e Envelope) error {
	if !validText(k.Destination, 512) || !validText(k.Journal, 512) || k.RecordID < 0 {
		return errors.New("invalid OTLP disposition identity")
	}
	if e.Signal != "logs" && e.Signal != "metrics" && e.Signal != "traces" {
		return errors.New("invalid OTLP signal")
	}
	if e.ContentType != "application/json" && e.ContentType != "application/x-protobuf" {
		return errors.New("invalid OTLP content type")
	}
	if e.Items < 0 || len(e.Payload) > s.limits.PayloadBytes {
		return errors.New("invalid OTLP envelope size or item count")
	}
	return nil
}
func (s *Store) validateOutcome(o Outcome, n int64) error {
	if o.Kind != Accepted && o.Kind != Retryable && !o.Kind.terminal() {
		return errors.New("unknown OTLP disposition")
	}
	if o.HTTPStatus < 0 || o.HTTPStatus > 599 || (o.HTTPStatus > 0 && o.HTTPStatus < 100) {
		return errors.New("invalid HTTP status")
	}
	if o.RejectedItems < 0 || o.RejectedItems > n {
		return errors.New("invalid rejected count")
	}
	if o.Kind == Partial && (o.RejectedItems == 0 || o.HTTPStatus != 200) {
		return errors.New("invalid OTLP partial rejection")
	}
	if (o.Kind == Accepted || o.Kind == Retryable) && o.RejectedItems != 0 {
		return errors.New("invalid nonterminal rejected count")
	}
	if o.Kind == Accepted && (o.HTTPStatus != 200 || o.Truncated) {
		return errors.New("invalid OTLP acceptance")
	}
	if len(o.Response) > s.limits.ResponseBytes || len(o.Diagnostic) > 4096 || !utf8.ValidString(o.Diagnostic) || len(o.ResponseContentType) > 256 || !utf8.ValidString(o.ResponseContentType) {
		return errors.New("OTLP peer evidence exceeds limits")
	}
	return nil
}
func keyHash(k Key) [32]byte {
	// Length-prefix fields: no collisions caused by separators in operator names.
	text := strconv.Itoa(len(k.Destination)) + ":" + k.Destination + strconv.Itoa(len(k.Journal)) + ":" + k.Journal + ":" + strconv.FormatInt(k.RecordID, 10)
	return sha256.Sum256([]byte(text))
}
func copyEnvelope(e Envelope) Envelope { e.Payload = bytes.Clone(e.Payload); return e }
func copyOutcome(o Outcome) Outcome    { o.Response = bytes.Clone(o.Response); return o }
func (s *Store) failed() error         { s.faultMu.Lock(); defer s.faultMu.Unlock(); return s.fault }
func (s *Store) poison(err error) error {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	if s.fault == nil {
		s.fault = errors.Join(ErrUncertain, err)
	}
	return s.fault
}

// Do checks the durable destination record before calling send. Known terminal
// replays return their original classified result WITHOUT invoking send. Accepted
// and Retryable outcomes are not persisted here: their WAL/ACK policy belongs to
// the producer. A terminal response is retained even if ctx was canceled while
// send ran. Persistence or classification failure blocks this Store, so later
// calls cannot accidentally retry a known terminal result in the same process.
// Input and callback buffers must not be mutated concurrently with their handoff.
func (s *Store) Do(ctx context.Context, k Key, e Envelope, send func(context.Context, Envelope) (Outcome, error)) (Result, error) {
	return s.do(ctx, k, e, send, false)
}

// DoDelivery records both full acceptance and terminal rejection before returning
// a durable result. Replaying a persisted acceptance skips the remote callback,
// just like a persisted rejection. Retryable/transport failures remain pending.
// Acceptance uses record version 2; older terminal-only binaries fail closed on
// that version. This does not close the response-before-persistence crash window.
func (s *Store) DoDelivery(ctx context.Context, k Key, e Envelope, send func(context.Context, Envelope) (Outcome, error)) (Result, error) {
	return s.do(ctx, k, e, send, true)
}

// Validate checks identity and envelope limits without side effects. It does not
// establish that the store is open, healthy or that any outcome is durable.
func (s *Store) Validate(k Key, e Envelope) error { return s.validate(k, e) }

func (s *Store) do(ctx context.Context, k Key, e Envelope, send func(context.Context, Envelope) (Outcome, error), retainAccepted bool) (Result, error) {
	if ctx == nil || send == nil {
		return Result{}, errors.New("nil context or send callback")
	}
	if err := s.validate(k, e); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	hash := keyHash(k)
	stripe := s.stripes[int(hash[0])%len(s.stripes)]
	select {
	case stripe <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-stripe }()
	s.life.RLock()
	defer s.life.RUnlock()
	if s.closed {
		return Result{}, ErrClosed
	}
	if err := s.failed(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	e = copyEnvelope(e)
	name := hex.EncodeToString(hash[:])
	old, err := s.lookup(name, k, e)
	if err != nil {
		return Result{}, err
	}
	if old != nil {
		return Result{Outcome: copyOutcome(old.Outcome), Quarantined: old.Outcome.Kind.terminal(), Replayed: true, Durable: true}, nil
	}
	o, sendErr := send(ctx, copyEnvelope(e))
	// A classified terminal response may accompany a decoding/response error.
	// Do not discard that result merely because send also reports an error.
	if o.Kind.terminal() {
		if err := s.validateOutcome(o, e.Items); err != nil {
			return Result{}, s.poison(err)
		}
		o = copyOutcome(o)
		if err := s.persist(name, entry{1, k, e, o, time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			return Result{}, s.poison(err)
		}
		return Result{Outcome: o, Quarantined: true, Durable: true}, nil
	}
	if sendErr != nil {
		return Result{}, sendErr
	}
	if err := s.validateOutcome(o, e.Items); err != nil {
		return Result{}, s.poison(err)
	}
	if retainAccepted && o.Kind == Accepted {
		o = copyOutcome(o)
		if err := s.persist(name, entry{2, k, e, o, time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
			return Result{}, s.poison(err)
		}
		return Result{Outcome: o, Durable: true}, nil
	}
	return Result{Outcome: copyOutcome(o)}, nil
}

func decodeStrict(b []byte, dst interface{}) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra interface{}
	if d.Decode(&extra) != io.EOF {
		return ErrCorrupt
	}
	return nil
}
func (s *Store) lookup(name string, k Key, e Envelope) (*entry, error) {
	path := filepath.Join(s.dir, name+".json")
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		pending, err := filepath.Glob(filepath.Join(s.dir, ".pending-"+name+"-*"))
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			return nil, ErrUncertain
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	// Base64 payload/response expansion plus fixed, bounded metadata.
	max := int64(s.limits.PayloadBytes+s.limits.ResponseBytes)*2 + 32*1024
	if st.Size() > max {
		return nil, ErrCorrupt
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, ErrCorrupt
	}
	var disk diskRecord
	if err = decodeStrict(b, &disk); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	sum := sha256.Sum256(disk.Entry)
	if disk.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, ErrCorrupt
	}
	var got entry
	if err = decodeStrict(disk.Entry, &got); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	validVersion := (got.Version == 1 && got.Outcome.Kind.terminal()) || (got.Version == 2 && got.Outcome.Kind == Accepted)
	if !validVersion || s.validate(got.Key, got.Envelope) != nil || s.validateOutcome(got.Outcome, got.Envelope.Items) != nil {
		return nil, ErrCorrupt
	}
	if _, err = time.Parse(time.RFC3339Nano, got.RecordedAt); err != nil {
		return nil, ErrCorrupt
	}
	if got.Key != k || (got.Envelope.Signal != e.Signal || got.Envelope.ContentType != e.ContentType || got.Envelope.Items != e.Items || !bytes.Equal(got.Envelope.Payload, e.Payload)) {
		return nil, ErrConflict
	}
	return &got, nil
}

func (s *Store) persist(name string, rec entry) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	data, err := json.Marshal(diskRecord{hex.EncodeToString(sum[:]), payload})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".pending-"+name+"-*")
	if err != nil {
		return err
	}
	// On EVERY failure preserve the temp inode. A subsequent open can refuse
	// automatic retry for this identity even if only a partial record survived.
	tmp := f.Name()
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.syncRecord(f)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	// Atomic no-replace publication; never overwrite forensic evidence.
	if err = s.linkRecord(tmp, filepath.Join(s.dir, name+".json")); err != nil {
		return err
	}
	// Only the following directory barrier makes publication a completed receipt.
	if err = s.syncDirectory(s.dirFile); err != nil {
		return err
	}
	// Redundant temp cleanup is best-effort AFTER durability. A crash can leave
	// both names; lookup prefers and validates the committed record.
	_ = os.Remove(tmp)
	return nil
}
