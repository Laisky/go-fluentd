package controller

import (
	"context"
	"errors"
	"time"
)

const defaultOTLPStorageFiles = 4096

func defaultOTLPStorageBytes(wal int64) int64 {
	if wal > 1<<49 {
		return 1 << 50
	}
	return max(512<<20, 2*wal)
}

// OTLPCapacitySnapshot is observational, not a filesystem/quota guarantee. Bytes and
// Files are the last admission inventory (possibly a lower bound on rejection).
// PendingUpperBound includes accepted IDs behind an unresolved prefix. This
// deliberately trades available capacity for avoiding a per-envelope RAM map.
type OTLPCapacitySnapshot struct {
	SharedLimitBytes    int64 `json:"shared_limit_bytes,omitempty"`
	SharedChargedBytes  int64 `json:"shared_charged_bytes,omitempty"`
	SharedReservedBytes int64 `json:"shared_reserved_bytes,omitempty"`
	SharedBudgetKnown   bool  `json:"shared_budget_known"`
	WALSegmentBytes     int64 `json:"wal_segment_bytes,omitempty"`

	Bytes                    int64  `json:"bytes"`
	Files                    int64  `json:"files"`
	PendingUpperBound        int64  `json:"pending_upper_bound"`
	RecoveryReady            bool   `json:"recovery_ready"`
	FilesystemAvailableBytes uint64 `json:"filesystem_available_bytes"`
	FilesystemFreeInodes     uint64 `json:"filesystem_free_inodes"`
	FilesystemAvailable      bool   `json:"filesystem_available"`
}

func (p *OTLPJournal) CapacitySnapshot() OTLPCapacitySnapshot {
	bytes, inodes, ok := otlpFilesystemCapacity(p.cfg.Directory)
	out := OTLPCapacitySnapshot{
		Bytes: p.lastStorageBytes.Load(), Files: p.lastStorageFiles.Load(),
		PendingUpperBound: p.reservedRecords.Load(), RecoveryReady: p.capacityKnown.Load(),
		FilesystemAvailableBytes: bytes, FilesystemFreeInodes: inodes, FilesystemAvailable: ok,
	}
	if p.budget != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		snap, err := p.budget.Snapshot(ctx)
		if err == nil {
			out.SharedLimitBytes = snap.Limit
			out.SharedChargedBytes = snap.ChargedBytes
			out.SharedReservedBytes = snap.ReservedBytes
			out.SharedBudgetKnown = true
		}
		out.WALSegmentBytes = p.cfg.WALSegmentBytes
	}
	return out
}

// Reserve every not-checkpointed ID for a replay copy and all frozen destination
// receipts. Receipt JSON can base64-encode payload/response bytes and escape up
// to 4 KiB of diagnostic text. 64 KiB bounds the remaining validated metadata;
// two receipt names account for atomic publication plus uncertain temp evidence.
// Neither terminal records nor already-admitted work are discarded to fit.
func (p *OTLPJournal) capacityRecord(r OTLPRecord, encodedBytes int) (int64, int64) {
	receipt := 2*(int64(len(r.Envelope.Payload))+int64(p.cfg.Limits.ResponseBytes)) + 64<<10
	future, files := 2*int64(encodedBytes)+2048+2*int64(len(r.Required))*receipt, 2*int64(len(r.Required))+2
	if p.cfg.RetainAccepted {
		// Two funded copies plus bounded index stages allow lossless receipt
		// packing/recovery without requesting fresh space from a full store.
		future += int64(len(r.Required)) * 4 * (receipt + 64<<10)
		files += int64(len(r.Required)) * 4
	}
	return future, files
}

func (p *OTLPJournal) observeCapacityRecord(r OTLPRecord, encodedBytes int) {
	bytes, files := p.capacityRecord(r, encodedBytes)
	p.maxRecordReserve = max(p.maxRecordReserve, bytes)
	p.maxRecordFiles = max(p.maxRecordFiles, files)
}

func (p *OTLPJournal) reserveAdmission(scanCtx, parent context.Context, r OTLPRecord, encodedBytes int) error {
	bytes, files := p.capacityRecord(r, encodedBytes)
	bytes, files = max(bytes, p.maxRecordReserve), max(files, p.maxRecordFiles)
	pending := r.ID - p.generation.ReleasedThrough
	// Division avoids overflow for a very old frontier or a small explicit cap.
	const checkpointBytes, checkpointFiles = 4096, 4
	if pending < 1 || p.cfg.MaxStorageBytes <= checkpointBytes || pending > (p.cfg.MaxStorageBytes-checkpointBytes)/bytes || pending > (int64(p.cfg.MaxStorageFiles)-checkpointFiles)/files {
		p.storageRejected.Add(1)
		return ErrOTLPJournalCapacity
	}
	reserveBytes, reserveFiles := pending*bytes+checkpointBytes, pending*files+checkpointFiles
	used, entries, err := p.scanRoot(scanCtx, p.cfg.MaxStorageBytes-reserveBytes, p.cfg.StorageScanMaxEntries)
	if err != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		if errors.Is(err, ErrOTLPJournalScanBudget) || scanCtx.Err() != nil {
			return p.rejectScanBudget()
		}
		return err
	}
	p.lastStorageBytes.Store(used)
	p.lastStorageFiles.Store(entries)
	if used > p.cfg.MaxStorageBytes-reserveBytes || entries > int64(p.cfg.MaxStorageFiles)-reserveFiles {
		p.storageRejected.Add(1)
		return ErrOTLPJournalCapacity
	}
	return nil
}
