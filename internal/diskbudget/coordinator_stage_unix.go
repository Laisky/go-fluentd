//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package diskbudget

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const maxLedgerBytes = 256 << 10
const maxCoordinatorEntries = 130 // primary, lock and at most 128 staged intents
// Fund one next maximum ledger write, block rounding and directory growth
// inside the already charged 1 MiB coordinator allowance, even after crashes.
const nextLedgerWriteAllowance = 2*maxLedgerBytes + 64<<10

var errCoordinatorMetadataCapacity = errors.New("shared budget coordinator metadata allowance exhausted")

func stagedLedgerName(name string) bool {
	const prefix = ".budget-pending-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	suffix := name[len(prefix):]
	// Go 1.27 CreateTemp's nonce is a decimal uint32. Requalify this reserved
	// namespace when changing the toolchain; other names remain foreign.
	if len(suffix) < 1 || len(suffix) > 10 {
		return false
	}
	value, err := strconv.ParseUint(suffix, 10, 32)
	return err == nil && strconv.FormatUint(value, 10) == suffix
}

// validateLedgerFiles is called only under the exclusive ledger flock. A
// surviving staging file is therefore abandoned, not an active peer write.
// budget.json remains the sole authority and must still pass read's complete
// validation. A staged intent is never promoted, merged, parsed or deleted:
// its operation could not return credit before rename plus directory Sync.
// Partial stages and uncommitted refunds cannot forgive an accepted charge.
// The namespace is private to the directory owner; a same-UID actor that can
// replace the primary ledger is outside that filesystem ownership boundary.
func (b *Budget) validateLedgerFiles() error {
	dirInfo, err := os.Lstat(b.dir)
	if err != nil {
		return err
	}
	directory, ok := dirInfo.Sys().(*syscall.Stat_t)
	if !ok || !dirInfo.IsDir() {
		return errors.New("invalid shared budget coordinator")
	}
	used := max(dirInfo.Size(), directory.Blocks*512)
	f, err := os.Open(b.dir)
	if err != nil {
		return err
	}
	defer f.Close()
	var entries []os.DirEntry
	for {
		batch, e := f.ReadDir(maxCoordinatorEntries + 1 - len(entries))
		entries = append(entries, batch...)
		if len(entries) > maxCoordinatorEntries {
			return errors.Join(ErrCapacity, errCoordinatorMetadataCapacity)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if len(batch) == 0 {
			return errors.New("incomplete shared budget metadata inventory")
		}
	}
	primary, staged := false, false
	for _, entry := range entries {
		name := entry.Name()
		isStage := stagedLedgerName(name)
		if name != "budget.json" && name != ".budget.lock" && !isStage {
			return errors.New("uncertain or foreign shared budget evidence retained")
		}
		info, e := os.Lstat(filepath.Join(b.dir, name))
		if e != nil {
			return e
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != directory.Uid || st.Nlink != 1 || info.Size() < 0 {
			return errors.New("foreign shared budget metadata retained")
		}
		if (name == "budget.json" || isStage) && info.Size() > maxLedgerBytes {
			return errors.New("oversized shared budget metadata retained")
		}
		size := max(info.Size(), st.Blocks*512)
		if size < 0 || size > ledgerAllowance-used {
			return errors.Join(ErrCapacity, errCoordinatorMetadataCapacity)
		}
		used += size
		primary = primary || name == "budget.json"
		staged = staged || isStage
	}
	if staged && !primary {
		return errors.New("staged shared budget evidence has no committed primary")
	}
	if used > ledgerAllowance-nextLedgerWriteAllowance {
		return errors.Join(ErrCapacity, errCoordinatorMetadataCapacity)
	}
	return nil
}
