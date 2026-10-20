package otlpstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Accepted receipts are normal logging state. Packing preserves their exact
// bytes, including original envelopes, while avoiding one inode per delivery.
// No HTTP/API/exporter or application-owned persistence is added.
const acceptedPartBytes int64 = 4 << 20
const maxAcceptedParts = 1024
const maxAcceptedIndexBytes = 16 << 10
const maxAcceptedBatch = 32
const acceptedFrameHeader = 44

var acceptedMagic = []byte("OTLPACK1")

type packedSource struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
type acceptedIndex struct {
	Version   int            `json:"version"`
	Sequence  uint32         `json:"sequence"`
	Namespace string         `json:"namespace"`
	Committed int64          `json:"committed"`
	Records   int64          `json:"records"`
	LastStart int64          `json:"last_start"`
	Sources   []packedSource `json:"sources"`
	Checksum  string         `json:"checksum"`
}
type acceptedPack struct {
	index  acceptedIndex
	size   int64
	pruned bool
}

func acceptedName(n uint32, suffix string) string {
	return fmt.Sprintf("accepted-pack-%08d.%s", n, suffix)
}
func acceptedSequence(name, suffix string) (uint32, bool) {
	if !strings.HasPrefix(name, "accepted-pack-") || !strings.HasSuffix(name, "."+suffix) {
		return 0, false
	}
	stem := strings.TrimSuffix(strings.TrimPrefix(name, "accepted-pack-"), "."+suffix)
	n, e := strconv.ParseUint(stem, 10, 32)
	return uint32(n), e == nil && n > 0 && acceptedName(uint32(n), suffix) == name
}
func (v acceptedIndex) checksum() string {
	v.Checksum = ""
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func receiptNameValid(n string) bool {
	if len(n) != 69 || !strings.HasSuffix(n, ".json") {
		return false
	}
	b, e := hex.DecodeString(n[:64])
	return e == nil && hex.EncodeToString(b) == n[:64]
}

func (s *Store) loadAcceptedPacks() error {
	dir, e := os.Open(s.dir)
	if e != nil {
		return e
	}
	defer dir.Close()
	packs := map[uint32]os.FileInfo{}
	indexes := map[uint32]bool{}
	stages := 0
	for {
		names, readErr := dir.Readdirnames(256)
		for _, name := range names {
			if receiptNameValid(name) {
				s.packingPending.Store(true)
			}
			if strings.HasPrefix(name, ".accepted-index-") {
				suffix := strings.TrimPrefix(name, ".accepted-index-")
				number, x := strconv.ParseUint(suffix, 10, 32)
				st, x2 := os.Lstat(filepath.Join(s.dir, name))
				stages++
				if x != nil || strconv.FormatUint(number, 10) != suffix || x2 != nil || !acceptedFileSafe(st, s.dirInfo) || st.Size() > maxAcceptedIndexBytes || stages > 128 {
					return ErrCorrupt
				}
				continue
			}
			if n, ok := acceptedSequence(name, "pack"); ok {
				st, e := os.Lstat(filepath.Join(s.dir, name))
				if e != nil || !acceptedFileSafe(st, s.dirInfo) {
					return ErrCorrupt
				}
				packs[n] = st
				s.packSequence = max(s.packSequence, n)
			} else if n, ok := acceptedSequence(name, "index.json"); ok {
				indexes[n] = true
				s.packSequence = max(s.packSequence, n)
			} else if strings.HasPrefix(name, "accepted-pack-") {
				return ErrCorrupt
			}
			if len(packs) > maxAcceptedParts || len(indexes) > maxAcceptedParts {
				return ErrCorrupt
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	for n := range indexes {
		if _, ok := packs[n]; !ok {
			return ErrCorrupt
		}
	}
	for n, st := range packs {
		if !indexes[n] {
			if st.Size() != 0 {
				return ErrCorrupt
			}
			continue
		} // empty pre-index crash evidence remains charged
		idx, e := s.readAcceptedIndex(n)
		if e != nil || idx.Committed > st.Size() {
			return ErrCorrupt
		}
		s.acceptedPacks = append(s.acceptedPacks, acceptedPack{index: idx, size: st.Size()})
		s.packedRecords.Add(uint64(idx.Records))
		s.packedParts.Add(1)
	}
	sort.Slice(s.acceptedPacks, func(i, j int) bool { return s.acceptedPacks[i].index.Sequence < s.acceptedPacks[j].index.Sequence })
	return nil
}
func (s *Store) readAcceptedIndex(n uint32) (acceptedIndex, error) {
	var v acceptedIndex
	p := filepath.Join(s.dir, acceptedName(n, "index.json"))
	st, e := os.Lstat(p)
	if e != nil || !acceptedFileSafe(st, s.dirInfo) || st.Size() > maxAcceptedIndexBytes {
		return v, ErrCorrupt
	}
	f, e := os.Open(p)
	if e != nil {
		return v, e
	}
	b, e := io.ReadAll(io.LimitReader(f, maxAcceptedIndexBytes+1))
	e = errors.Join(e, f.Close())
	if e != nil || len(b) > maxAcceptedIndexBytes || decodeStrict(b, &v) != nil {
		return v, ErrCorrupt
	}
	if v.Version != 1 || v.Sequence != n || !validText(v.Namespace, 512) || v.Committed < 0 || v.Records < 0 || v.Records > v.Committed/acceptedFrameHeader || v.LastStart < 0 || v.LastStart > v.Committed || len(v.Sources) > maxAcceptedBatch || v.Checksum != v.checksum() {
		return v, ErrCorrupt
	}
	if (v.Committed == 0) != (v.Records == 0) || len(v.Sources) == 0 && v.Committed != 0 {
		return v, ErrCorrupt
	}
	if v.Committed == 0 && (v.LastStart != 0 || len(v.Sources) != 0) || v.Committed > 0 && (v.LastStart >= v.Committed || int64(len(v.Sources)) > v.Records) {
		return v, ErrCorrupt
	}
	seen := map[string]bool{}
	sum := int64(0)
	for _, p := range v.Sources {
		h, e := hex.DecodeString(p.SHA256)
		if !receiptNameValid(p.Name) || seen[p.Name] || e != nil || hex.EncodeToString(h) != p.SHA256 || len(h) != 32 || p.Bytes < 1 || p.Bytes > s.maxArchiveRecord() || p.Bytes+acceptedFrameHeader > v.Committed-v.LastStart-sum {
			return v, ErrCorrupt
		}
		seen[p.Name] = true
		sum += p.Bytes + acceptedFrameHeader
	}
	if sum != v.Committed-v.LastStart {
		return v, ErrCorrupt
	}
	return v, nil
}
func (s *Store) maxArchiveRecord() int64 {
	return int64(s.limits.PayloadBytes+s.limits.ResponseBytes)*2 + 32<<10
}
func (s *Store) checkAcceptedIndex(v acceptedIndex) error {
	fresh, e := s.readAcceptedIndex(v.Sequence)
	if e != nil || fresh.Checksum != v.checksum() {
		return ErrCorrupt
	}
	return nil
}
func (s *Store) publishAcceptedIndex(v acceptedIndex, prior *acceptedIndex) error {
	check := func() error {
		if prior != nil {
			return s.checkAcceptedIndex(*prior)
		}
		_, e := os.Lstat(filepath.Join(s.dir, acceptedName(v.Sequence, "index.json")))
		if !errors.Is(e, os.ErrNotExist) {
			return ErrCorrupt
		}
		return nil
	}
	if e := check(); e != nil {
		return e
	}
	v.Checksum = v.checksum()
	raw, e := json.Marshal(v)
	if e != nil || len(raw) > maxAcceptedIndexBytes {
		return ErrCorrupt
	}
	f, e := os.CreateTemp(s.dir, ".accepted-index-*")
	if e != nil {
		return e
	}
	n, e := f.Write(raw)
	if e == nil && n != len(raw) {
		e = io.ErrShortWrite
	}
	if e == nil {
		e = s.syncRecord(f)
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = check(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), filepath.Join(s.dir, acceptedName(v.Sequence, "index.json"))); e != nil {
		return e
	}
	return s.syncDirectory(s.dirFile)
}
func frame(raw []byte) []byte {
	b := make([]byte, acceptedFrameHeader+len(raw))
	copy(b, acceptedMagic)
	binary.BigEndian.PutUint32(b[8:12], uint32(len(raw)))
	h := sha256.Sum256(raw)
	copy(b[12:44], h[:])
	copy(b[44:], raw)
	return b
}
func (s *Store) walkAcceptedPack(ctx context.Context, p acceptedPack, start int64, visit func(*entry, []byte) error) (int, error) {
	if e := s.checkAcceptedIndex(p.index); e != nil {
		return 0, e
	}
	file := filepath.Join(s.dir, acceptedName(p.index.Sequence, "pack"))
	st, e := os.Lstat(file)
	if e != nil || !acceptedFileSafe(st, s.dirInfo) || st.Size() < p.index.Committed {
		return 0, ErrCorrupt
	}
	f, e := os.Open(file)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	if _, e = f.Seek(start, io.SeekStart); e != nil {
		return 0, e
	}
	remaining := p.index.Committed - start
	n := 0
	for remaining > 0 {
		if e = ctx.Err(); e != nil {
			return n, e
		}
		if remaining < acceptedFrameHeader {
			return n, ErrCorrupt
		}
		head := make([]byte, acceptedFrameHeader)
		if _, e = io.ReadFull(f, head); e != nil {
			return n, ErrCorrupt
		}
		size := int64(binary.BigEndian.Uint32(head[8:12]))
		if !bytes.Equal(head[:8], acceptedMagic) || size < 1 || size > s.maxArchiveRecord() || size > remaining-acceptedFrameHeader {
			return n, ErrCorrupt
		}
		raw := make([]byte, int(size))
		if _, e = io.ReadFull(f, raw); e != nil {
			return n, ErrCorrupt
		}
		hash := sha256.Sum256(raw)
		if !bytes.Equal(hash[:], head[12:44]) {
			return n, ErrCorrupt
		}
		var disk diskRecord
		if decodeStrict(raw, &disk) != nil {
			return n, ErrCorrupt
		}
		var untrusted entry
		if decodeStrict(disk.Entry, &untrusted) != nil {
			return n, ErrCorrupt
		}
		key := keyHash(untrusted.Key)
		rec, e := s.decodeArchiveRecord(hex.EncodeToString(key[:])+".json", raw)
		if e != nil || rec.Outcome.Kind != Accepted || rec.Key.Journal != p.index.Namespace {
			return n, ErrCorrupt
		}
		if e = visit(rec, raw); e != nil {
			return n, e
		}
		n++
		remaining -= size + acceptedFrameHeader
	}
	if start == 0 && int64(n) != p.index.Records {
		return n, ErrCorrupt
	}
	return n, nil
}

// finishPackedSources verifies the entire last committed batch before removing
// any duplicate loose copy. This is lossless compaction, not retirement/eviction.
func (s *Store) finishPackedSources(ctx context.Context, p acceptedPack) (int, error) {
	expected := p.index.Sources
	i := 0
	n, e := s.walkAcceptedPack(ctx, p, p.index.LastStart, func(rec *entry, raw []byte) error {
		if i >= len(expected) {
			return ErrCorrupt
		}
		v := expected[i]
		key := keyHash(rec.Key)
		h := sha256.Sum256(raw)
		if v.Name != hex.EncodeToString(key[:])+".json" || v.Bytes != int64(len(raw)) || v.SHA256 != hex.EncodeToString(h[:]) {
			return ErrCorrupt
		}
		i++
		return nil
	})
	if e != nil || n != len(expected) {
		return 0, errors.Join(ErrCorrupt, e)
	}
	removed := 0
	for _, v := range expected {
		if e = ctx.Err(); e != nil {
			return removed, e
		}
		_, raw, e := s.readPruneRecord(v.Name)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return removed, e
		}
		h := sha256.Sum256(raw)
		if int64(len(raw)) != v.Bytes || hex.EncodeToString(h[:]) != v.SHA256 {
			return removed, ErrConflict
		}
		if e = os.Remove(filepath.Join(s.dir, v.Name)); e != nil {
			return removed, e
		}
		removed++
	}
	if removed > 0 {
		e = s.syncDirectory(s.dirFile)
	}
	return removed, e
}

// CompactAccepted has the SAME caller-proven released-frontier requirement as
// PruneAccepted. It preserves every original in a checksummed normal-log part.
// Metadata/memory/batch/part counts are finite; source receipts survive until
// both part data and authoritative commit offset have synchronized. A crash
// tail beyond that offset is retained/charged and never promoted or overwritten.
func (s *Store) CompactAccepted(ctx context.Context, namespace string, through int64) (int, error) {
	if ctx == nil || !validText(namespace, 512) || through < 0 {
		return 0, ErrCorrupt
	}
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	if e := s.failed(); e != nil {
		return 0, e
	}
	s.packingPending.Store(true)
	st, e := os.Lstat(s.dir)
	if e != nil || !os.SameFile(st, s.dirInfo) {
		return 0, ErrUncertain
	}
	removed := 0
	for i, p := range s.acceptedPacks {
		if p.pruned {
			continue
		}
		n, e := s.finishPackedSources(ctx, p)
		removed += n
		if e != nil {
			if ctx.Err() != nil {
				return removed, ctx.Err()
			}
			return removed, s.poison(e)
		}
		s.acceptedPacks[i].pruned = true
	}
	dir, e := os.Open(s.dir)
	if e != nil {
		return removed, e
	}
	defer dir.Close()
	var sources []packedSource
	var raws [][]byte
	var dataBytes int64
	done := false
	for !done {
		if e = ctx.Err(); e != nil {
			return removed, e
		}
		names, readErr := dir.Readdirnames(256)
		for _, name := range names {
			if !receiptNameValid(name) {
				continue
			}
			rec, raw, e := s.readPruneRecord(name)
			if e != nil {
				return removed, e
			}
			if rec.Key.Journal != namespace || rec.Outcome.Kind != Accepted || rec.Key.RecordID > through {
				continue
			}
			if len(raws) > 0 && dataBytes+int64(len(raw))+acceptedFrameHeader > acceptedPartBytes {
				done = true
				break
			}
			h := sha256.Sum256(raw)
			sources = append(sources, packedSource{name, hex.EncodeToString(h[:]), int64(len(raw))})
			raws = append(raws, raw)
			dataBytes += int64(len(raw)) + acceptedFrameHeader
			if len(raws) == maxAcceptedBatch {
				done = true
				break
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return removed, readErr
		}
	}
	if len(raws) == 0 {
		s.packingPending.Store(false)
		return removed, nil
	}
	// One funding callback covers data copy, initial+replacement index stages,
	// directory growth and an uncertain metadata copy. No new capacity is claimed
	// after mutation. The controller also reserved this work before ingress ACK.
	if s.BeforePersist != nil {
		if e = s.BeforePersist(dataBytes + 2*maxAcceptedIndexBytes + 32<<10); e != nil {
			return removed, e
		}
	}
	active := -1
	if len(s.acceptedPacks) > 0 {
		i := len(s.acceptedPacks) - 1
		p := s.acceptedPacks[i]
		if p.index.Namespace == namespace && p.size == p.index.Committed && p.size+dataBytes <= max(acceptedPartBytes, dataBytes) {
			active = i
		}
	}
	if active < 0 {
		if s.packSequence >= 99999999 || len(s.acceptedPacks) >= maxAcceptedParts {
			return removed, ErrCorrupt
		}
		s.packSequence++
		v := acceptedIndex{Version: 1, Sequence: s.packSequence, Namespace: namespace}
		f, e := os.OpenFile(filepath.Join(s.dir, acceptedName(v.Sequence, "pack")), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return removed, s.poison(e)
		}
		e = errors.Join(s.syncRecord(f), f.Close())
		if e == nil {
			e = s.syncDirectory(s.dirFile)
		}
		if e == nil {
			e = s.publishAcceptedIndex(v, nil)
		}
		if e != nil {
			return removed, s.poison(e)
		}
		s.acceptedPacks = append(s.acceptedPacks, acceptedPack{index: v})
		active = len(s.acceptedPacks) - 1
		s.packedParts.Add(1)
	}
	p := s.acceptedPacks[active]
	file := filepath.Join(s.dir, acceptedName(p.index.Sequence, "pack"))
	if e = s.checkAcceptedIndex(p.index); e != nil {
		return removed, s.poison(e)
	}
	info, e := os.Lstat(file)
	if e != nil || !acceptedFileSafe(info, s.dirInfo) || info.Size() != p.index.Committed {
		return removed, s.poison(ErrCorrupt)
	}
	f, e := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return removed, s.poison(e)
	}
	for _, raw := range raws {
		b := frame(raw)
		var n int
		n, e = f.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		if e != nil {
			break
		}
	}
	if e == nil {
		e = s.syncRecord(f)
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return removed, s.poison(e)
	}
	next := p.index
	next.LastStart = next.Committed
	next.Committed += dataBytes
	next.Records += int64(len(raws))
	next.Sources = sources
	if e = s.publishAcceptedIndex(next, &p.index); e != nil {
		return removed, s.poison(e)
	}
	next.Checksum = next.checksum()
	s.acceptedPacks[active] = acceptedPack{index: next, size: next.Committed}
	s.packedRecords.Add(uint64(len(raws)))
	n, e := s.finishPackedSources(ctx, s.acceptedPacks[active])
	removed += n
	if e != nil {
		if ctx.Err() != nil {
			return removed, ctx.Err()
		}
		return removed, s.poison(e)
	}
	s.acceptedPacks[active].pruned = true
	s.packingPending.Store(done)
	return removed, nil
}
func (s *Store) HasAcceptedPacks() bool { return len(s.acceptedPacks) > 0 }
func (s *Store) PackingPending() bool   { return s.packingPending.Load() }
func (s *Store) PackingStatistics() (uint64, uint64) {
	return s.packedRecords.Load(), s.packedParts.Load()
}
