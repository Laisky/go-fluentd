package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"github.com/tinylib/msgp/msgp"

	"gofluentd/library"
)

// Exercise the dependency through the consumer's replay/rewrite path. Sparse
// acknowledgements must never be interpreted as a maximum-ID frontier.
func TestRegressionSelectiveJournalSparseACKConsumer(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%v", compressed), func(t *testing.T) {
			dir := t.TempDir()
			backend := upgradeBackend(t, dir, compressed)
			want := map[int64]map[string]interface{}{}
			for _, id := range []int64{1000, 3, 90, 1, 7, 15} {
				payload := upgradePayload(id)
				payload["large"] = strings.Repeat(fmt.Sprint(id)+"世界", 4096)
				if id == 15 { // deliberately cross the bounded reader fast path
					payload["large"] = strings.Repeat("oversized", 32768)
				}
				if err := backend.WriteData(&journal.Data{ID: id, Data: map[string]interface{}{"tag": "source", "message": payload}}); err != nil {
					t.Fatal(err)
				}
				if id == 1000 || id == 90 || id == 7 {
					if err := backend.WriteId(id); err != nil {
						t.Fatal(err)
					}
				} else {
					want[id] = payload
				}
			}
			if err := backend.Sync(); err != nil {
				t.Fatal(err)
			}
			backend.Close()
			for restart := 0; restart < 2; restart++ {
				backend = upgradeBackend(t, dir, compressed)
				controller := upgradeController(backend, 1)
				high, err := controller.LoadMaxID()
				if err != nil || high != 1000 {
					t.Fatalf("frontier=%d err=%v", high, err)
				}
				out := make(chan *library.FluentMsg, 16)
				if _, err := controller.ProcessLegacyMsg(out); err != nil {
					t.Fatal(err)
				}
				close(out)
				got := map[int64]map[string]interface{}{}
				for msg := range out {
					if msg.Tag != "source" || msg.JournalTag != "source" {
						t.Fatalf("changed ownership: %+v", msg)
					}
					if _, exists := got[msg.ID]; exists {
						t.Fatalf("unexpected duplicate pending ID %d", msg.ID)
					}
					got[msg.ID] = msg.Message
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("restart%d lost or changed sparse pending payloads", restart)
				}
				backend.Close()
			}
		})
	}
}

// Independently assemble an old Data-before-ID envelope rather than using the
// dependency's generated record encoder to define what its reader must accept.
func selectiveOldWire(id int64) []byte {
	b := []byte{0x82, 0xa4, 'D', 'a', 't', 'a', 0x81, 0xa4, 'b', 'o', 'd', 'y'}
	b = msgp.AppendString(b, "independent 世界")
	b = append(b, 0xa2, 'I', 'D')
	return msgp.AppendInt64(b, id)
}

func TestRegressionSelectiveJournalNeverHidesAcknowledgedCorruption(t *testing.T) {
	for _, kind := range []string{"invalid-map-key", "invalid-value", "gzip-checksum"} {
		t.Run(kind, func(t *testing.T) {
			wire := selectiveOldWire(19)
			gz := kind == "gzip-checksum"
			switch kind {
			case "invalid-map-key":
				wire = []byte{0x82, 0xa4, 'D', 'a', 't', 'a', 0x81, 1, 0xc0, 0xa2, 'I', 'D', 19}
			case "invalid-value":
				wire = []byte{0x82, 0xa4, 'D', 'a', 't', 'a', 0x81, 0xa1, 'x', 0xc1, 0xa2, 'I', 'D', 19}
			case "gzip-checksum":
				var buf bytes.Buffer
				w := gzip.NewWriter(&buf)
				if _, err := w.Write(wire); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				wire = buf.Bytes()
				wire[len(wire)-8] ^= 1 // corrupt CRC, not a recoverable torn tail
			}
			path := filepath.Join(t.TempDir(), "20200101_00000001.buf")
			if gz {
				path += ".gz"
			}
			if err := os.WriteFile(path, wire, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			loader := journal.NewLegacyLoader(ctx, journal.Logger, []string{path}, nil, gz, time.Hour)
			loader.AddID(19)
			if err := loader.Load(new(journal.Data)); err == nil || err == io.EOF {
				t.Fatalf("acknowledged corruption became successful replay/EOF: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, wire) {
				t.Fatal("corrupt evidence removed or modified")
			}
		})
	}
}
