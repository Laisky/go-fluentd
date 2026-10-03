package fluentwire

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"testing"
)

func testLimits() Limits {
	return Limits{MaxFrameBytes: 1024, MaxValueBytes: 512, MaxContainerElements: 16, MaxValues: 64, MaxDepth: 8}
}
func budget(t testing.TB, l Limits) *Budget {
	t.Helper()
	b, err := NewBudget(l)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type missingBody struct {
	data            []byte
	readsPastHeader int
}

var errBodyRequested = errors.New("parser requested missing body")

func (r *missingBody) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		r.readsPastHeader++
		return 0, errBodyRequested
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestRejectDeclaredLengthsBeforeBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
	}{
		{"outer32", []byte{0xdd, 0xff, 0xff, 0xff, 0xff}},
		{"outer16", []byte{0xdc, 0xff, 0xff}},
		{"array32", []byte{0x92, 0xa0, 0xdd, 0xff, 0xff, 0xff, 0xff}},
		{"map32", []byte{0x92, 0xa0, 0xdf, 0xff, 0xff, 0xff, 0xff}},
		{"string32", []byte{0x92, 0xa0, 0xdb, 0xff, 0xff, 0xff, 0xff}},
		{"binary32", []byte{0x92, 0xa0, 0xc6, 0xff, 0xff, 0xff, 0xff}},
		{"extension32", []byte{0x92, 0xa0, 0xc9, 0xff, 0xff, 0xff, 0xff}},
		{"container16", []byte{0x92, 0xa0, 0xdc, 0, 17}},
		{"string16", []byte{0x92, 0xa0, 0xda, 2, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &missingBody{data: tc.wire}
			_, err := budget(t, testLimits()).ReadFrame(bufio.NewReader(r), 2, 4)
			if !errors.Is(err, ErrLimit) || r.readsPastHeader != 0 {
				t.Fatalf("err=%v reads after header=%d", err, r.readsPastHeader)
			}
		})
	}
}

func TestPipelinedAndFragmentedFrames(t *testing.T) {
	frames := [][]byte{{0x92, 0xa1, 't', 0x90}, {0x93, 0xa1, 't', 0, 0x81, 0xa1, 'v', 1}, {0x94, 0xa1, 't', 0, 0x80, 0x80}, {0xdc, 0, 2, 0xa0, 0x90}, {0xdd, 0, 0, 0, 2, 0xa0, 0x90}}
	input := bytes.Join(frames, nil)
	for _, fragment := range []bool{false, true} {
		var source io.Reader = bytes.NewReader(input)
		if fragment {
			source = oneByteReader{source}
		}
		r := bufio.NewReader(source)
		for _, want := range frames {
			got, err := budget(t, testLimits()).ReadFrame(r, 2, 4)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("got=%x want=%x err=%v", got, want, err)
			}
		}
		if _, err := budget(t, testLimits()).ReadFrame(r, 2, 4); err != io.EOF {
			t.Fatal(err)
		}
	}
}

type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestEveryScalarEncoding(t *testing.T) {
	values := [][]byte{{0}, {0x7f}, {0xe0}, {0xff}, {0xc0}, {0xc2}, {0xc3}, {0xa0}, {0xa1, 'x'}}
	for marker, length := range map[byte]int{0xcc: 1, 0xcd: 2, 0xce: 4, 0xcf: 8, 0xd0: 1, 0xd1: 2, 0xd2: 4, 0xd3: 8, 0xca: 4, 0xcb: 8, 0xd4: 2, 0xd5: 3, 0xd6: 5, 0xd7: 9, 0xd8: 17} {
		values = append(values, append([]byte{marker}, make([]byte, length)...))
	}
	for _, m := range []byte{0xc4, 0xc7, 0xd9, 0xc5, 0xc8, 0xda, 0xc6, 0xc9, 0xdb} {
		width := map[byte]int{0xc4: 1, 0xc7: 1, 0xd9: 1, 0xc5: 2, 0xc8: 2, 0xda: 2, 0xc6: 4, 0xc9: 4, 0xdb: 4}[m]
		v := append([]byte{m}, make([]byte, width)...)
		v[len(v)-1] = 1
		if m == 0xc7 || m == 0xc8 || m == 0xc9 {
			v = append(v, 0)
		}
		values = append(values, append(v, 'x'))
	}
	values = append(values, []byte{0x80}, []byte{0xde, 0, 0}, []byte{0xdf, 0, 0, 0, 0}, []byte{0x90}, []byte{0xdc, 0, 0}, []byte{0xdd, 0, 0, 0, 0})
	for _, v := range values {
		wire := append([]byte{0x92, 0xa0}, v...)
		got, err := budget(t, testLimits()).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4)
		if err != nil || !bytes.Equal(got, wire) {
			t.Fatalf("wire=%x err=%v", wire, err)
		}
	}
}

func TestFrameAndValueByteBoundaries(t *testing.T) {
	for _, payloadLen := range []int{0, 1, 7, 64, 512} {
		wire := []byte{0x92, 0xa0, 0xc5, byte(payloadLen >> 8), byte(payloadLen)}
		wire = append(wire, make([]byte, payloadLen)...)
		l := testLimits()
		l.MaxFrameBytes = len(wire)
		l.MaxValueBytes = max(1, payloadLen)
		if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); err != nil {
			t.Fatal(err)
		}
		l.MaxFrameBytes--
		if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); !errors.Is(err, ErrLimit) {
			t.Fatalf("frame limit: %v", err)
		}
		if payloadLen > 1 {
			l.MaxFrameBytes++
			l.MaxValueBytes = payloadLen - 1
			if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); !errors.Is(err, ErrLimit) {
				t.Fatalf("value limit: %v", err)
			}
		}
	}
}

func TestAggregateValuesAndSharedPackedBudget(t *testing.T) {
	wire := []byte{0x92, 0xa0, 0x92, 0x92, 0, 0x80, 0x92, 0, 0x80}
	l := testLimits()
	l.MaxValues = 9
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); err != nil {
		t.Fatal(err)
	}
	l.MaxValues = 8
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	l.MaxValues = 6
	b := budget(t, l)
	r := bufio.NewReader(bytes.NewReader([]byte{0x92, 0, 0x80, 0x92, 0, 0x80, 0x92, 0, 0x80}))
	for i := 0; i < 2; i++ {
		if _, err := b.ReadFrame(r, 2, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.ReadFrame(r, 2, 2); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func TestDepthAndMapKeyAccounting(t *testing.T) {
	wire := []byte{0x92, 0xa0, 0x91, 0x91, 0xc0}
	l := testLimits()
	l.MaxDepth = 4
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); err != nil {
		t.Fatal(err)
	}
	l.MaxDepth = 3
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	l = testLimits()
	l.MaxValues = 4
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader([]byte{0x92, 0xa0, 0x81, 0xa0, 0})), 2, 4); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func TestTruncationsAndInvalidMarkers(t *testing.T) {
	wire := []byte{0x93, 0xa1, 't', 0, 0x81, 0xa3, 'l', 'o', 'g', 0xc4, 3, 'a', 'b', 'c'}
	for i := 0; i < len(wire); i++ {
		if _, err := budget(t, testLimits()).ReadFrame(bufio.NewReader(bytes.NewReader(wire[:i])), 2, 4); err == nil || (i > 0 && err == io.EOF) {
			t.Fatalf("accepted truncation or treated it as clean EOF at %d: %v", i, err)
		}
	}
	for _, wire := range [][]byte{{0x90}, {0x91, 0}, {0x95}, {0x80}, {0x92, 0xa0, 0xc1}} {
		if _, err := budget(t, testLimits()).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); err == nil {
			t.Fatalf("accepted %x", wire)
		}
	}
}

func TestInvalidLimits(t *testing.T) {
	base := testLimits()
	v := reflect.ValueOf(base)
	for i := 0; i < v.NumField(); i++ {
		l := base
		reflect.ValueOf(&l).Elem().Field(i).SetZero()
		if _, err := NewBudget(l); err == nil {
			t.Fatalf("accepted zero %s", v.Type().Field(i).Name)
		}
	}
	l := base
	l.MaxDepth = 129
	if _, err := NewBudget(l); err == nil {
		t.Fatal("unbounded depth")
	}
	l = base
	l.MaxValueBytes = l.MaxFrameBytes + 1
	if _, err := NewBudget(l); err == nil {
		t.Fatal("inconsistent bytes")
	}
}

func FuzzReadFrame(f *testing.F) {
	for _, seed := range [][]byte{{0x92, 0xa0, 0x90}, {0x93, 0xa0, 0, 0x80}, {0xdd, 0xff, 0xff, 0xff, 0xff}, {0x92, 0xa0, 0xdb, 0xff, 0xff, 0xff, 0xff}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		l := testLimits()
		b := budget(t, l)
		r := bufio.NewReader(bytes.NewReader(input))
		got, err := b.ReadFrame(r, 2, 4)
		if err == nil {
			if len(got) > l.MaxFrameBytes || !bytes.HasPrefix(input, got) {
				t.Fatalf("invalid successful frame %x", got)
			}
			again, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(got)), 2, 4)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatalf("non-repeatable parse: %v", err)
			}
		}
	})
}

func BenchmarkRejectMaxArrayHeader(b *testing.B) {
	var header [5]byte
	header[0] = 0xdd
	binary.BigEndian.PutUint32(header[1:], ^uint32(0))
	source := bytes.NewReader(header[:])
	r := bufio.NewReader(source)
	l := testLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		source.Reset(header[:])
		r.Reset(source)
		_, err := budget(b, l).ReadFrame(r, 2, 4)
		if !errors.Is(err, ErrLimit) {
			b.Fatal(err)
		}
	}
}

func TestFixedWidthNumbersDoNotConsumeVariableValueLimit(t *testing.T) {
	l := testLimits()
	l.MaxValueBytes = 1
	wire := []byte{0x92, 0xa0, 0xcf, 0, 0, 0, 0, 0, 0, 0, 1}
	if _, err := budget(t, l).ReadFrame(bufio.NewReader(bytes.NewReader(wire)), 2, 4); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkValidateLogFrame(b *testing.B) {
	for _, size := range []int{1024, 65536} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			wire := []byte{0x93, 0xa1, 't', 0, 0x81, 0xa3, 'l', 'o', 'g', 0xc6, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(wire[len(wire)-4:], uint32(size))
			wire = append(wire, make([]byte, size)...)
			l := Limits{MaxFrameBytes: 8 << 20, MaxValueBytes: 8 << 20, MaxContainerElements: 4096, MaxValues: 65536, MaxDepth: 32}
			source := bytes.NewReader(wire)
			r := bufio.NewReader(source)
			b.SetBytes(int64(len(wire)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source.Reset(wire)
				r.Reset(source)
				if _, err := budget(b, l).ReadFrame(r, 2, 4); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestMaxArrayHeaderAllocationBound(t *testing.T) {
	source := bytes.NewReader([]byte{0xdd, 0xff, 0xff, 0xff, 0xff})
	r := bufio.NewReader(source)
	l := testLimits()
	_, _ = budget(t, l).ReadFrame(r, 2, 4)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 256; i++ {
		source.Reset([]byte{0xdd, 0xff, 0xff, 0xff, 0xff})
		r.Reset(source)
		if _, err := budget(t, l).ReadFrame(r, 2, 4); !errors.Is(err, ErrLimit) {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 1<<20 {
		t.Fatalf("max array headers allocated %d bytes (budget 1 MiB for 256 rejections)", delta)
	}
}
