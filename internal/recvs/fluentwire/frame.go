// Package fluentwire validates untrusted MessagePack framing before a generic
// decoder can allocate from wire lengths. It deliberately does not interpret
// records or change the scalar semantics of the application's MessagePack codec.
package fluentwire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrLimit marks a rejected resource budget. A caller must close the stream:
// after rejection its position is not necessarily a frame boundary.
var ErrLimit = errors.New("Fluent ingress limit exceeded")

// Limits bounds both encoded payloads and the metadata a decoder can allocate.
// MaxValues includes map keys, map values, array members and container roots.
// A Budget is shared by an outer frame and all of its packed entries.
type Limits struct {
	MaxFrameBytes        int
	MaxValueBytes        int
	MaxContainerElements uint32
	MaxValues            uint64
	MaxDepth             int
}

// Validate rejects disabled, inconsistent and overflowing limits.
func (l Limits) Validate() error {
	if l.MaxFrameBytes <= 0 || l.MaxValueBytes <= 0 ||
		l.MaxContainerElements == 0 || l.MaxValues == 0 || l.MaxDepth <= 0 {
		return errors.New("all Fluent framing limits must be positive")
	}
	if l.MaxValueBytes > l.MaxFrameBytes {
		return errors.New("Fluent max_value_bytes exceeds max_frame_bytes")
	}
	// Bound recursive call-stack use even if an operator supplies a huge value.
	if l.MaxDepth > 128 {
		return errors.New("Fluent max_depth exceeds hard ceiling 128")
	}
	return nil
}

// Budget limits aggregate allocations/work across one outer frame and its
// packed entries. New outer frames need a new budget; packed entries must not.
type Budget struct {
	limits Limits
	values uint64
}

// NewBudget validates the policy before accepting any untrusted bytes.
func NewBudget(l Limits) (*Budget, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &Budget{limits: l, values: l.MaxValues}, nil
}

// ReadFrame consumes exactly one array-shaped frame, preserving buffered bytes
// for the next frame. The outer count is checked before creating a payload
// buffer. No allocation is based on an unchecked wire length. Payload storage
// grows only for bytes actually received, not for a claimed-but-missing body.
// minFields/maxFields must be small protocol constants, not peer input.
func (b *Budget) ReadFrame(r *bufio.Reader, minFields, maxFields uint32) (data []byte, err error) {
	if minFields == 0 || minFields > maxFields || maxFields > 4 {
		return nil, errors.New("invalid Fluent frame shape policy")
	}
	f := frameReader{r: r, budget: b}
	marker, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	// EOF is clean only before the first byte. In particular, a truncated
	// packed entry must not look like the end of the packed-entry stream.
	defer func() {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
	}()
	// Do not append even the first byte until the root shape is known.
	var count uint32
	var header [5]byte
	header[0] = marker
	hlen := 1
	switch {
	case marker >= 0x90 && marker <= 0x9f:
		count = uint32(marker & 0x0f)
	case marker == 0xdc:
		hlen = 3
		if _, err = io.ReadFull(r, header[1:hlen]); err != nil {
			return nil, err
		}
		count = uint32(binary.BigEndian.Uint16(header[1:hlen]))
	case marker == 0xdd:
		hlen = 5
		if _, err = io.ReadFull(r, header[1:hlen]); err != nil {
			return nil, err
		}
		count = binary.BigEndian.Uint32(header[1:hlen])
	default:
		return nil, errors.New("Fluent frame must be a MessagePack array")
	}
	if count < minFields || count > maxFields {
		return nil, fmt.Errorf("%w: frame fields %d, expected %d..%d", ErrLimit, count, minFields, maxFields)
	}
	if err = b.reserve(1 + uint64(count)); err != nil {
		return nil, err
	}
	if err = f.appendBytes(header[:hlen]); err != nil {
		return nil, err
	}
	for i := uint32(0); i < count; i++ {
		if err = f.value(2); err != nil {
			return nil, err
		}
	}
	return f.data, nil
}

func (b *Budget) reserve(n uint64) error {
	if n > b.values {
		return fmt.Errorf("%w: total values", ErrLimit)
	}
	b.values -= n
	return nil
}

type frameReader struct {
	r      *bufio.Reader
	budget *Budget
	data   []byte
}

func (f *frameReader) appendBytes(p []byte) error {
	if len(p) > f.budget.limits.MaxFrameBytes-len(f.data) {
		return fmt.Errorf("%w: frame bytes", ErrLimit)
	}
	f.data = append(f.data, p...)
	return nil
}

func (f *frameReader) byte() (byte, error) {
	// Check before reading so a rejected frame cannot eat the next frame.
	if len(f.data) >= f.budget.limits.MaxFrameBytes {
		return 0, fmt.Errorf("%w: frame bytes", ErrLimit)
	}
	v, err := f.r.ReadByte()
	if err != nil {
		return 0, err
	}
	f.data = append(f.data, v)
	return v, nil
}

func (f *frameReader) length(n int) (uint32, error) {
	var buf [4]byte
	if n > f.budget.limits.MaxFrameBytes-len(f.data) {
		return 0, fmt.Errorf("%w: frame bytes", ErrLimit)
	}
	if _, err := io.ReadFull(f.r, buf[:n]); err != nil {
		return 0, err
	}
	f.data = append(f.data, buf[:n]...)
	switch n {
	case 1:
		return uint32(buf[0]), nil
	case 2:
		return uint32(binary.BigEndian.Uint16(buf[:2])), nil
	default:
		return binary.BigEndian.Uint32(buf[:]), nil
	}
}

func (f *frameReader) payload(n uint32, extension bool) error {
	return f.copyPayload(n, extension, true)
}

func (f *frameReader) copyPayload(n uint32, extension, limitValue bool) error {
	if limitValue && uint64(n) > uint64(f.budget.limits.MaxValueBytes) {
		return fmt.Errorf("%w: value bytes", ErrLimit)
	}
	needed := uint64(n)
	if extension {
		needed++ // extension type byte
	}
	if needed > uint64(f.budget.limits.MaxFrameBytes-len(f.data)) {
		return fmt.Errorf("%w: frame bytes", ErrLimit)
	}
	if extension {
		if _, err := f.byte(); err != nil {
			return err
		}
	}
	// Borrow the fixed-size input buffer instead of allocating a scratch
	// buffer for each scalar. Append only payload bytes actually received.
	for n > 0 {
		want := f.r.Size()
		if uint64(want) > uint64(n) {
			want = int(n)
		}
		p, err := f.r.Peek(want)
		f.data = append(f.data, p...)
		if len(p) > 0 {
			if _, discardErr := f.r.Discard(len(p)); discardErr != nil {
				return discardErr
			}
			n -= uint32(len(p))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *frameReader) container(n uint32, isMap bool, depth int) error {
	if n > f.budget.limits.MaxContainerElements {
		return fmt.Errorf("%w: container elements", ErrLimit)
	}
	children := uint64(n)
	if isMap {
		children *= 2
	}
	// Reserve siblings before descending. This bounds allocations even when a
	// nested container appears before many as-yet-unread siblings.
	if err := f.budget.reserve(children); err != nil {
		return err
	}
	for i := uint64(0); i < children; i++ {
		if err := f.value(depth + 1); err != nil {
			return err
		}
	}
	return nil
}

func (f *frameReader) value(depth int) error {
	if depth > f.budget.limits.MaxDepth {
		return fmt.Errorf("%w: nesting depth", ErrLimit)
	}
	m, err := f.byte()
	if err != nil {
		return err
	}
	switch {
	case m <= 0x7f || m >= 0xe0:
		return nil
	case m >= 0xa0 && m <= 0xbf:
		return f.payload(uint32(m&0x1f), false)
	case m >= 0x90 && m <= 0x9f:
		return f.container(uint32(m&0x0f), false, depth)
	case m >= 0x80 && m <= 0x8f:
		return f.container(uint32(m&0x0f), true, depth)
	}
	var width int
	switch m {
	case 0xc0, 0xc2, 0xc3:
		return nil
	case 0xc1:
		return errors.New("reserved MessagePack marker 0xc1")
	case 0xcc, 0xd0:
		return f.copyPayload(1, false, false)
	case 0xcd, 0xd1:
		return f.copyPayload(2, false, false)
	case 0xca, 0xce, 0xd2:
		return f.copyPayload(4, false, false)
	case 0xcb, 0xcf, 0xd3:
		return f.copyPayload(8, false, false)
	case 0xd4, 0xd5, 0xd6, 0xd7, 0xd8:
		return f.payload(uint32(1)<<(m-0xd4), true)
	case 0xc4, 0xc7, 0xd9:
		width = 1
	case 0xc5, 0xc8, 0xda, 0xdc, 0xde:
		width = 2
	case 0xc6, 0xc9, 0xdb, 0xdd, 0xdf:
		width = 4
	default:
		return errors.New("unknown MessagePack marker")
	}
	n, err := f.length(width)
	if err != nil {
		return err
	}
	switch m {
	case 0xdc, 0xdd:
		return f.container(n, false, depth)
	case 0xde, 0xdf:
		return f.container(n, true, depth)
	case 0xc7, 0xc8, 0xc9:
		return f.payload(n, true)
	default:
		return f.payload(n, false)
	}
}
