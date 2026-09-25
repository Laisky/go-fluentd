package otlpwire_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/library/otlpwire"
)

// TestOTLPJournalEnvelopeRoundTrip exercises real journal files via exported
// APIs. It proves the envelope representation, not a configured OTLP receiver,
// sender, downstream delivery, SIGKILL recovery, or physical power-loss safety.
func TestOTLPJournalEnvelopeRoundTrip(t *testing.T) {
	for _, signal := range signals {
		for _, contentType := range types {
			for _, wireGzip := range []bool{false, true} {
				for _, journalGzip := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/wire-gzip=%v/journal-gzip=%v", signal, contentType, wireGzip, journalGzip)
					t.Run(name, func(t *testing.T) {
						dir := t.TempDir()
						open := func() *journal.Journal {
							t.Helper()
							j, err := journal.NewJournal(
								journal.WithBufDirPath(dir),
								journal.WithIsCompress(journalGzip),
								journal.WithIsAggresiveGC(false),
								journal.WithBufSizeByte(1<<20),
								journal.WithFlushInterval(time.Hour),
								journal.WithRotateDuration(time.Hour),
								journal.WithRotateCheckInterval(time.Hour),
							)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(j.Close)
							if err := j.Start(context.Background()); err != nil {
								t.Fatal(err)
							}
							return j
						}

						raw := fixture(t, signal, contentType)
						if contentType == otlpwire.JSON {
							// Unknown future fields must survive, not just the model
							// recognized by today's Collector decoder.
							raw = append([]byte(`{"futureEnvelope":{"large":"18446744073709551615"},`), bytes.TrimSpace(raw)[1:]...)
						}
						wire, encoding := raw, ""
						if wireGzip {
							wire, encoding = gzipBytes(t, raw), "gzip"
						}
						req, err := otlpwire.ReadRequest(signal, contentType, encoding, bytes.NewReader(wire), otlpwire.DefaultLimits())
						if err != nil {
							t.Fatal(err)
						}
						j := open()
						record := &journal.Data{ID: 1, Data: map[string]interface{}{
							"signal":       string(req.Signal()),
							"content_type": req.ContentType(),
							"payload":      req.Payload(),
						}}
						if err := j.WriteData(record); err != nil {
							t.Fatal(err)
						}
						if err := j.Sync(); err != nil {
							t.Fatal(err)
						}
						// A caller changing its own copy after the durable write
						// must not affect the saved bytes or the parsed request.
						record.Data["payload"].([]byte)[0] ^= 0xff
						if !bytes.Equal(req.Payload(), raw) {
							t.Fatal("caller-owned record aliases the parsed request")
						}
						j.Close()

						for round := 0; round < 2; round++ {
							j = open()
							maxID, err := j.LoadMaxId()
							if err != nil || maxID != 1 {
								t.Fatalf("round %d: identity frontier %d: %v", round, maxID, err)
							}
							if !j.LockLegacy() {
								t.Fatal("cannot acquire replay ownership")
							}
							got := new(journal.Data)
							if err := j.LoadLegacyBuf(got); err != nil {
								t.Fatal(err)
							}
							payload, ok := got.Data["payload"].([]byte)
							if got.ID != 1 || len(got.Data) != 3 || got.Data["signal"] != string(signal) || got.Data["content_type"] != contentType || !ok || !bytes.Equal(payload, raw) {
								t.Fatalf("round %d: persisted OTLP envelope changed", round)
							}
							restored, err := otlpwire.ReadRequest(signal, contentType, "", bytes.NewReader(payload), otlpwire.DefaultLimits())
							if err != nil || restored.Items() != count(signal) {
								t.Fatalf("round %d: restored item count/schema changed: %v", round, err)
							}
							// Transfer the pending envelope before asking for EOF,
							// which can reclaim the old segment.
							if err := j.WriteData(got); err != nil {
								t.Fatal(err)
							}
							if err := j.Sync(); err != nil {
								t.Fatal(err)
							}
							if err := j.LoadLegacyBuf(new(journal.Data)); err != io.EOF {
								t.Fatalf("round %d: expected completed replay, got %v", round, err)
							}
							j.Close()
						}
					})
				}
			}
		}
	}
}
