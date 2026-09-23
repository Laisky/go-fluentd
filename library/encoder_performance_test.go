package library

import (
	"bytes"
	"fmt"
	"github.com/tinylib/msgp/msgp"
	"io"
	"strings"
	"testing"
)

func TestPerformanceContractForwardWire(t *testing.T) {
	var out bytes.Buffer
	enc := NewFluentEncoder(&out)
	if err := enc.EncodeBatch("logs", []*FluentMsg{{Message: map[string]interface{}{"k": "v"}}}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Flush(); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x92, 0xa4, 'l', 'o', 'g', 's', 0x91, 0x92, 0x00, 0x81, 0xa1, 'k', 0xa1, 'v'}
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("Forward wire changed: %x", out.Bytes())
	}
	// Reuse the same encoder after a batch, including MessagePack array-header boundaries.
	for _, n := range []int{0, 1, 15, 16, 255, 256, 512, 65536} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			out.Reset()
			batch := make([]*FluentMsg, n)
			for i := range batch {
				batch[i] = &FluentMsg{Tag: "not-the-wire-tag", Message: map[string]interface{}{"k": int64(i)}}
			}
			if err := enc.EncodeBatch("logs", batch); err != nil {
				t.Fatal(err)
			}
			if err := enc.Flush(); err != nil {
				t.Fatal(err)
			}
			reader := msgp.NewReader(&out)
			outer, err := reader.ReadArrayHeader()
			if err != nil || outer != 2 {
				t.Fatal("bad outer frame", err)
			}
			tag, err := reader.ReadString()
			if err != nil || tag != "logs" {
				t.Fatal("wrong tag", err)
			}
			count, err := reader.ReadArrayHeader()
			if err != nil || count != uint32(n) {
				t.Fatal("wrong record count", err)
			}
			for i := range batch {
				count, err = reader.ReadArrayHeader()
				if err != nil || count != 2 {
					t.Fatal("bad record frame", err)
				}
				timestamp, err := reader.ReadInt64()
				if err != nil || timestamp != 0 {
					t.Fatal("timestamp contract changed", err)
				}
				record, err := reader.ReadIntf()
				if err != nil {
					t.Fatal(err)
				}
				if record.(map[string]interface{})["k"] != int64(i) {
					t.Fatalf("record %d was changed: %v", i, record)
				}
				if batch[i].Tag != "not-the-wire-tag" || batch[i].Message["k"] != int64(i) {
					t.Fatal("input mutated")
				}
			}
			if _, err := reader.ReadIntf(); err != io.EOF {
				t.Fatalf("extra output: %v", err)
			}
		})
	}
}

type performanceFailWriter struct{}

func (performanceFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestPerformanceContractEncoderWriteFailure(t *testing.T) {
	enc := NewFluentEncoder(performanceFailWriter{})
	err := enc.EncodeBatch("logs", []*FluentMsg{{Message: map[string]interface{}{"large": strings.Repeat("x", BufByte+16)}}})
	if err == nil {
		err = enc.Flush()
	}
	if err == nil || !strings.Contains(err.Error(), io.ErrClosedPipe.Error()) {
		t.Fatalf("write error lost: %v", err)
	}
}
