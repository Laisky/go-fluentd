//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal/v2"
	"github.com/tinylib/msgp/msgp"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
)

func legacyRecoverySource(t *testing.T, compressed bool, body string) string {
	t.Helper()
	dir := sharedPrivateDir(t)
	j, e := journal.NewJournal(journal.WithBufDirPath(dir), journal.WithIsCompress(compressed), journal.WithBufSizeByte(1<<20), journal.WithFlushInterval(time.Hour), journal.WithRotateDuration(time.Hour), journal.WithRotateCheckInterval(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	d := &journal.Data{ID: 7, Data: map[string]interface{}{"tag": "source.original", "message": map[string]interface{}{"body": body, "large": int64(9007199254740993), "bytes": []byte{0, 255}, "nested": map[string]interface{}{}, "future": true}}}
	if e = j.WriteData(d); e != nil {
		t.Fatal(e)
	}
	if e = j.Sync(); e != nil {
		t.Fatal(e)
	}
	if e = j.WriteId(7); e != nil {
		t.Fatal(e)
	}
	if e = j.Sync(); e != nil {
		t.Fatal(e)
	}
	j.Close()
	return dir
}
func legacySourceHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	entries, e := os.ReadDir(root)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		raw, e := os.ReadFile(filepath.Join(root, entry.Name()))
		if e != nil {
			t.Fatal(e)
		}
		out[entry.Name()] = sha256.Sum256(raw)
	}
	return out
}
func TestLegacyRecoveryTransportTLSExactRawDryRunAndRefusal(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "gzip"}[compressed], func(t *testing.T) {
			source := legacyRecoverySource(t, compressed, "original operational body")
			before := legacySourceHashes(t, source)
			root, e := os.OpenRoot(source)
			if e != nil {
				t.Fatal(e)
			}
			archive, e := journal.OpenClosedRecoveryArchive(root, 1<<20)
			if e != nil {
				t.Fatal(e)
			}
			var wantRaw []byte
			if n, e := archive.WalkRaw(t.Context(), func(_ *journal.Data, raw []byte) error { wantRaw = append([]byte(nil), raw...); return nil }); e != nil || n != 1 {
				t.Fatal(n, e)
			}
			archive.Close()
			root.Close()
			var calls atomic.Int64
			var unavailable atomic.Bool
			sink := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer synthetic-legacy-archive-token" {
					t.Error("configured authentication missing")
				}
				if unavailable.Load() {
					w.WriteHeader(503)
					return
				}
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
				}
				request := plogotlp.NewExportRequest()
				if e = request.UnmarshalProto(raw); e != nil {
					t.Error(e)
					return
				}
				stored := request.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
				// Explicit native storage codec retains the full pre-projection OTLP record.
				value, ok := stored.Attributes().Get("otel.record.otlp")
				if !ok {
					t.Error("native archive missing")
					return
				}
				original, e := base64.StdEncoding.DecodeString(value.Str())
				if e != nil {
					t.Error(e)
					return
				}
				wrapped := plogotlp.NewExportRequest()
				if e = wrapped.UnmarshalProto(original); e != nil {
					t.Error(e)
					return
				}
				record := wrapped.Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
				rawLegacy := record.Body().Bytes().AsRaw()
				if !bytes.Equal(rawLegacy, wantRaw) {
					t.Error("legacy MessagePack wire bytes changed")
				}
				var d journal.Data
				if e = msgp.Decode(bytes.NewReader(rawLegacy), &d); e != nil {
					t.Error(e)
				}
				message := d.Data["message"].(map[string]interface{})
				if d.ID != 7 || d.Data["tag"] != "source.original" || message["body"] != "original operational body" || message["large"] != int64(9007199254740993) {
					t.Error("original types/identity changed")
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
			}))
			defer sink.Close()
			ca := filepath.Join(t.TempDir(), "ca.crt")
			os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sink.Certificate().Raw}), 0600)
			t.Setenv("SYNTHETIC_LEGACY_ARCHIVE_TOKEN", "synthetic-legacy-archive-token")
			c := nativeStorageTestConfig(t, sink.URL, 1<<20)
			c.Destinations[0].CAFile = ca
			c.Destinations[0].BearerTokenEnv = "SYNTHETIC_LEGACY_ARCHIVE_TOKEN"
			if n, e := RecoverLegacyArchive(t.Context(), c, source, "b1-stopped-fixture", false); e != nil || n != 1 || calls.Load() != 0 {
				t.Fatal("dry run sent or missed original", n, e)
			}
			if n, e := RecoverLegacyArchive(t.Context(), c, source, "b1-stopped-fixture", true); e != nil || n != 1 || calls.Load() != 1 {
				t.Fatal("exact legacy archive transport", n, e)
			}
			unavailable.Store(true)
			if _, e := RecoverLegacyArchive(t.Context(), c, source, "b1-stopped-fixture", true); e == nil || strings.Contains(e.Error(), "synthetic-legacy-archive-token") {
				t.Fatal("unaccepted source incorrectly completed or secret emitted", e)
			}
			if !reflect.DeepEqual(before, legacySourceHashes(t, source)) {
				t.Fatal("source records/ACKs changed")
			}
			// Lack of configured trust must fail TLS verification rather than bypass it.
			bad := c
			bad.Destinations = append([]OTLPServiceDestination(nil), c.Destinations...)
			bad.Destinations[0].CAFile = ""
			prior := calls.Load()
			if _, e := RecoverLegacyArchive(context.Background(), bad, source, "b1-stopped-fixture", true); e == nil || calls.Load() != prior {
				t.Fatal("TLS trust bypassed", e)
			}
		})
	}
}
func TestLegacyRecoveryTransportValidatesWholeArchiveBeforeSending(t *testing.T) {
	source := legacyRecoverySource(t, false, "original body")
	entries, _ := os.ReadDir(source)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".buf") {
			f, e := os.OpenFile(filepath.Join(source, entry.Name()), os.O_APPEND|os.O_WRONLY, 0)
			if e != nil {
				t.Fatal(e)
			}
			f.Write([]byte{0x81, 0xa5, 's'})
			f.Close()
			break
		}
	}
	var calls atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer sink.Close()
	c := nativeStorageTestConfig(t, sink.URL, 1<<20)
	if _, e := RecoverLegacyArchive(t.Context(), c, source, "truncated-fixture", true); e == nil || calls.Load() != 0 {
		t.Fatal("partial preflight sent a valid prefix", e)
	}
	large := legacyRecoverySource(t, false, strings.Repeat("x", 16384))
	c.MaxDecodedBytes = 4096
	if _, e := RecoverLegacyArchive(t.Context(), c, large, "oversized-fixture", true); e == nil || calls.Load() != 0 {
		t.Fatal("oversized archive was sent or truncated", e)
	}
}
