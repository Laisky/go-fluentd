package controller

import (
	"context"
	"errors"
	journal "github.com/Laisky/go-journal"
)

// ReadRecoveryArchive exposes original retained legacy records to a dedicated
// recovery transport. It deliberately ignores delivery ACK filtering and never
// deletes originals. Callback must not reenter managed journal I/O.
func (j *Journal) ReadRecoveryArchive(ctx context.Context, visit func(string, *journal.Data) error) (int, error) {
	if j.diskBudget == nil || visit == nil {
		return 0, errors.New("overall retained archive is not enabled")
	}
	if !j.legacyLock.TryLock() {
		return 0, errors.New("journal replay already active")
	}
	defer j.legacyLock.ForceRelease()
	total := 0
	var result error
	j.tag2JMap.Range(func(key, value interface{}) bool {
		reader, ok := value.(interface {
			ReadRecoveryRecords(context.Context, func(*journal.Data) error) (int, error)
		})
		if !ok {
			result = errors.New("retained archive reader unavailable")
			return false
		}
		n, err := reader.ReadRecoveryRecords(ctx, func(d *journal.Data) error { return visit(key.(string), d) })
		total += n
		if err != nil {
			result = err
			return false
		}
		return true
	})
	if result == nil {
		result = j.reconcileOverallBudget(ctx)
	}
	return total, result
}
