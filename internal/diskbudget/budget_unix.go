//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

// Package diskbudget accounts a group of exclusively owned managed persistent roots.
// Reservations precede writes and survive crashes. Only a quiescent owner may
// reconcile its root; unknown files and allocated directory blocks are counted.
package diskbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultLimitBytes int64 = 3000000000
const MaximumLimitBytes int64 = 1 << 50
const ledgerAllowance int64 = 1 << 20
const bootstrap int64 = 2 << 20

var ErrCapacity = errors.New("overall go-fluentd storage budget full")
var ErrMigration = errors.New("existing OTLP state requires qualified shared-budget migration")

type claim struct {
	Parents []string

	Bytes   int64 `json:"bytes"`
	Reserve int64 `json:"reserve"`
}
type ledger struct {
	Version int              `json:"version"`
	Limit   int64            `json:"limit"`
	Roots   map[string]claim `json:"roots"`
}
type Snapshot struct{ Limit, ChargedBytes, ReservedBytes int64 }
type Budget struct {
	directoryKey string

	key     string
	parents []string

	mu        sync.Mutex
	dir, root string
	limit     int64
	owner     *os.File
	closed    bool
	fault     error
}

func privateDir(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	st, e := os.Lstat(p)
	if e != nil {
		return "", e
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return "", errors.New("shared budget requires an existing private non-symlink directory")
	}
	return p, nil
}
func overlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(os.PathSeparator)) || strings.HasPrefix(b, a+string(os.PathSeparator))
}
func lock(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, e := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("invalid shared budget lock")
	}
	for {
		e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			if err := ctx.Err(); err != nil {
				f.Close()
				return nil, err
			}
			return f, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (b *Budget) read() (ledger, error) {
	l := ledger{Version: 2, Limit: b.limit, Roots: map[string]claim{}}
	path := filepath.Join(b.dir, "budget.json")
	st, e := os.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return l, nil
	}
	if e != nil {
		return l, e
	}
	if !st.Mode().IsRegular() || st.Size() > maxLedgerBytes {
		return l, errors.New("invalid shared budget ledger")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return l, e
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if e = d.Decode(&l); e != nil {
		return l, e
	}
	var extra any
	if d.Decode(&extra) != io.EOF || l.Version != 2 || l.Limit != b.limit || l.Roots == nil || len(l.Roots) > 64 {
		return l, errors.New("incompatible shared budget ledger")
	}
	for root, c := range l.Roots {
		if !validIdentity(root) || len(c.Parents) > 128 || c.Bytes < 0 || c.Reserve < 0 || c.Bytes > b.limit || c.Reserve > b.limit-c.Bytes {
			return l, errors.New("invalid shared budget claim")
		}
	}
	n, e := total(l)
	if e != nil || n > l.Limit {
		return l, errors.New("invalid aggregate shared budget total")
	}
	return l, nil
}
func total(l ledger) (int64, error) {
	n := ledgerAllowance
	for _, c := range l.Roots {
		if c.Bytes > math.MaxInt64-n {
			return 0, ErrCapacity
		}
		n += c.Bytes
		if c.Reserve > math.MaxInt64-n {
			return 0, ErrCapacity
		}
		n += c.Reserve
	}
	return n, nil
}
func (b *Budget) write(l ledger) error {
	raw, e := json.Marshal(l)
	if e != nil {
		return e
	}
	if len(raw) > maxLedgerBytes {
		return errors.New("shared budget ledger too large")
	}
	f, e := os.CreateTemp(b.dir, ".budget-pending-*")
	if e != nil {
		return e
	}
	n, e := f.Write(raw)
	if e == nil && n != len(raw) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), filepath.Join(b.dir, "budget.json")); e != nil {
		return e
	}
	d, e := os.Open(b.dir)
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}
func (b *Budget) change(ctx context.Context, fn func(*ledger) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.closed {
		return os.ErrClosed
	}
	if b.fault != nil {
		return b.fault
	}
	if err := b.validateOwnership(); err != nil {
		return err
	}
	f, e := lock(ctx, filepath.Join(b.dir, ".budget.lock"))
	if e != nil {
		return e
	}
	defer f.Close()
	if e = b.validateLedgerFiles(); e != nil {
		return e
	}
	l, e := b.read()
	if e != nil {
		return e
	}
	if e = fn(&l); e != nil {
		return e
	}
	n, e := total(l)
	if e != nil {
		return e
	}
	if n > l.Limit {
		return ErrCapacity
	}
	if e = b.write(l); e != nil {
		b.fault = fmt.Errorf("shared budget persistence uncertain: %w", e)
	}
	return e
}

// Open only adopts an empty unregistered root. Existing accepted queues are not
// silently reset, moved or placed under an unverified reservation estimate.
func Open(ctx context.Context, dir, root string, limit int64) (*Budget, error) {
	if limit == 0 {
		limit = DefaultLimitBytes
	}
	if limit < 4<<20 || limit > MaximumLimitBytes {
		return nil, errors.New("overall go-fluentd budget must be between 4 MiB and 1 PiB")
	}
	d, e := privateDir(dir)
	if e != nil {
		return nil, e
	}
	r, e := privateDir(root)
	if e != nil {
		return nil, e
	}
	if overlap(d, r) || len(r) > 2048 {
		return nil, errors.New("shared budget and state roots must be separate")
	}
	key, parents, e := identity(r)
	if e != nil {
		return nil, e
	}
	budgetKey, budgetParents, e := identity(d)
	if e != nil {
		return nil, e
	}
	if key == budgetKey || contains(parents, budgetKey) || contains(budgetParents, key) {
		return nil, errors.New("budget/state inode domains overlap")
	}
	b := &Budget{dir: d, root: r, key: key, parents: parents, limit: limit, directoryKey: budgetKey}
	e = b.change(ctx, func(l *ledger) error {
		for other, c := range l.Roots {
			if other != key && (contains(parents, other) || contains(c.Parents, key)) {
				return errors.New("overlapping shared budget roots")
			}
		}
		c, known := l.Roots[key]
		if !known {
			used, e := usage(ctx, r)
			if e != nil {
				return e
			}
			c = claim{Bytes: used, Reserve: bootstrap, Parents: parents}
			l.Roots[key] = c
			n, e := total(*l)
			if e != nil || n > limit {
				return ErrCapacity
			}
			entries, e := os.ReadDir(r)
			if e != nil {
				return e
			}
			if len(entries) != 0 {
				return ErrMigration
			}
		}
		// No waiting for another owner while holding the global ledger lock.
		fd, e := syscall.Open(filepath.Join(r, ".go-fluentd-disk-owner.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), "shared-budget-owner")
		if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			f.Close()
			return errors.New("shared budget root already owned")
		}
		b.owner = f
		l.Roots[key] = c
		return nil
	})
	if e != nil {
		if b.owner != nil {
			b.owner.Close()
		}
		return nil, e
	}
	return b, nil
}

// Admit durably funds initial WAL growth and all bounded future work in one ledger commit.
func (b *Budget) Admit(ctx context.Context, initial, future int64) error {
	if initial < 0 || future < 0 {
		return errors.New("negative shared admission")
	}
	return b.change(ctx, func(l *ledger) error {
		c, ok := l.Roots[b.key]
		if !ok {
			return ErrMigration
		}
		if initial > b.limit-c.Bytes || future > b.limit-c.Reserve {
			return ErrCapacity
		}
		c.Bytes += initial
		c.Reserve += future
		l.Roots[b.key] = c
		return nil
	})
}

// Reserve promises bounded future work before input is acknowledged.
func (b *Budget) Reserve(ctx context.Context, n int64) error {
	if n < 0 {
		return errors.New("negative shared reservation")
	}
	return b.change(ctx, func(l *ledger) error {
		c, ok := l.Roots[b.key]
		if !ok {
			return ErrMigration
		}
		if n > b.limit-c.Reserve {
			return ErrCapacity
		}
		c.Reserve += n
		l.Roots[b.key] = c
		return nil
	})
}

// Charge is persisted BEFORE mutation. A crash before writing overcounts until a
// locked reconciliation. Repeated crashes cannot create uncharged replay copies.
func (b *Budget) Charge(ctx context.Context, n int64) error {
	if n < 0 {
		return errors.New("negative shared charge")
	}
	return b.change(ctx, func(l *ledger) error {
		c, ok := l.Roots[b.key]
		if !ok {
			return ErrMigration
		}
		if n > b.limit-c.Bytes {
			return ErrCapacity
		}
		c.Bytes += n
		c.Reserve = max(0, c.Reserve-n)
		l.Roots[b.key] = c
		return nil
	})
}

// Reconcile requires the caller's WAL, replay and outcome writers to be quiescent.
// It never removes a file. Release future reservations only after no pending ID.
func (b *Budget) Reconcile(ctx context.Context, empty bool) error {
	return b.change(ctx, func(l *ledger) error {
		used, e := usage(ctx, b.root)
		if e != nil {
			return e
		}
		c, ok := l.Roots[b.key]
		if !ok {
			return ErrMigration
		}
		// Refund unused pre-write charges before releasing excess funding. A
		// quiescent recovery must never replenish consumed bootstrap space:
		// doing so requests new capacity and can prevent a full store starting.
		if used < c.Bytes {
			c.Reserve += c.Bytes - used
		}
		if empty {
			c.Reserve = min(c.Reserve, bootstrap)
		}
		c.Bytes = used
		l.Roots[b.key] = c
		return nil
	})
}
func (b *Budget) Snapshot(ctx context.Context) (Snapshot, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out Snapshot
	if b.closed {
		return out, os.ErrClosed
	}
	if b.fault != nil {
		return out, b.fault
	}
	if e := b.validateOwnership(); e != nil {
		return out, e
	}
	f, e := lock(ctx, filepath.Join(b.dir, ".budget.lock"))
	if e != nil {
		return out, e
	}
	defer f.Close()
	if e = b.validateLedgerFiles(); e != nil {
		return out, e
	}
	l, e := b.read()
	if e != nil {
		return out, e
	}
	if _, known := l.Roots[b.key]; !known {
		return out, ErrMigration
	}
	out.Limit = l.Limit
	out.ChargedBytes = ledgerAllowance
	for _, c := range l.Roots {
		out.ChargedBytes += c.Bytes
		out.ReservedBytes += c.Reserve
	}
	return out, nil
}
func (b *Budget) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.owner.Close()
}
func usage(ctx context.Context, root string) (int64, error) {
	var total int64
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if depth > 128 {
			return errors.New("storage nesting exceeds bound")
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		st, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return errors.New("unexpected shared-budget state entry")
		}
		size := st.Size()
		if v, ok := st.Sys().(*syscall.Stat_t); ok {
			size = max(size, v.Blocks*512)
		}
		if size < 0 || size > math.MaxInt64-total {
			return ErrCapacity
		}
		total += size
		if !st.IsDir() {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		for {
			entries, readErr := f.ReadDir(256)
			for _, entry := range entries {
				if e = walk(filepath.Join(path, entry.Name()), depth+1); e != nil {
					return e
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	err := walk(root, 0)
	return total, err
}

// Kernel filesystem identity distinguishes equal mount paths in separate
// containers and detects aliases of the same backing root. No payload is read.
func identity(path string) (string, []string, error) {
	var ids []string
	for {
		st, e := os.Stat(path)
		if e != nil {
			return "", nil, e
		}
		v, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			return "", nil, errors.New("filesystem identity unavailable")
		}
		ids = append(ids, fmt.Sprintf("%x:%x", v.Dev, v.Ino))
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
		if len(ids) > 128 {
			return "", nil, errors.New("storage path nesting exceeds bound")
		}
	}
	return ids[0], ids[1:], nil
}
func contains(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
func validIdentity(s string) bool {
	if len(s) < 3 || len(s) > 40 {
		return false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if !strings.ContainsRune("0123456789abcdef", r) {
				return false
			}
		}
	}
	return true
}

func (b *Budget) validateOwnership() error {
	for path, want := range map[string]string{b.root: b.key, b.dir: b.directoryKey} {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		v, ok := st.Sys().(*syscall.Stat_t)
		if !st.IsDir() || !ok || fmt.Sprintf("%x:%x", v.Dev, v.Ino) != want {
			return errors.New("overall disk-budget ownership directory changed")
		}
	}
	return nil
}
