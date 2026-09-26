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
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"github.com/tinylib/msgp/msgp"
)

// Hand-authored legacy envelopes exercise sequence semantics, not merely each
// record's syntactic validity. The old generated decoder leaves missing fields
// unchanged in the reused destination. Selective skipping must not substitute
// caller sentinels for the preceding acknowledged record's fields.
func TestRegressionSelectiveReplaySequenceCompatibility(t *testing.T) {
	priorData := map[string]interface{}{"origin": "acknowledged 世界", "n": int64(91)}
	first := msgp.AppendMapHeader(nil, 2)
	first = msgp.AppendString(first, "Data")
	var err error
	first, err = msgp.AppendIntf(first, priorData)
	if err != nil {
		t.Fatal(err)
	}
	first = msgp.AppendString(first, "ID")
	first = msgp.AppendInt64(first, 9)
	missingData := msgp.AppendMapHeader(nil, 1)
	missingData = msgp.AppendString(missingData, "ID")
	missingData = msgp.AppendInt64(missingData, 10)
	tailData := map[string]interface{}{"origin": "pending tail"}
	missingID := msgp.AppendMapHeader(nil, 1)
	missingID = msgp.AppendString(missingID, "Data")
	missingID, err = msgp.AppendIntf(missingID, tailData)
	if err != nil {
		t.Fatal(err)
	}
	for _, gz := range []bool{false, true} {
		for _, split := range []bool{false, true} {
			for _, tc := range []struct {
				name string
				tail []byte
				id   int64
				data map[string]interface{}
			}{
				{"missing-data", missingData, 10, priorData},
				{"missing-id", missingID, 9, tailData},
			} {
				t.Run(fmt.Sprintf("gzip=%t/split=%t/%s", gz, split, tc.name), func(t *testing.T) {
					dir := t.TempDir()
					write := func(name string, wire []byte) string {
						t.Helper()
						if gz {
							var b bytes.Buffer
							w := gzip.NewWriter(&b)
							if _, err := w.Write(wire); err != nil {
								t.Fatal(err)
							}
							if err := w.Close(); err != nil {
								t.Fatal(err)
							}
							wire = b.Bytes()
							name += ".gz"
						}
						path := filepath.Join(dir, name)
						if err := os.WriteFile(path, wire, 0600); err != nil {
							t.Fatal(err)
						}
						return path
					}
					var paths []string
					if split {
						paths = []string{write("20260926_00000001.buf", first), write("20260926_00000002.buf", tc.tail)}
					} else {
						paths = []string{write("20260926_00000001.buf", append(append([]byte(nil), first...), tc.tail...))}
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					loader := journal.NewLegacyLoader(ctx, journal.Logger, paths, nil, gz, time.Hour)
					loader.AddID(9)
					got := &journal.Data{ID: 777, Data: map[string]interface{}{"sentinel": "caller"}}
					err := loader.Load(got)
					if tc.name == "missing-id" {
						// The existing TTL set retains ACK membership; missing
						// ID inherits 9 and must remain suppressed, not become
						// the caller's unrelated sentinel 777.
						if err != io.EOF {
							t.Fatalf("missing ID changed ACK suppression: got ID=%d error=%v, want EOF", got.ID, err)
						}
						if err := loader.Clean(); err != nil {
							t.Fatal(err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if got.ID != tc.id || !reflect.DeepEqual(got.Data, tc.data) {
						t.Fatalf("selective replay changed sequence semantics: got ID=%d Data=%#v, want ID=%d Data=%#v", got.ID, got.Data, tc.id, tc.data)
					}
					if err := loader.Clean(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
