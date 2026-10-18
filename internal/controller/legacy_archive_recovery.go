package controller

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	journal "github.com/Laisky/go-journal"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// RecoverLegacyArchive sends each exact original MessagePack record as an OTLP
// byte body. Original IDs/tags remain inside the authoritative bytes; indexed
// metadata is supplemental. Source files and ACKs never change, even on success.
func RecoverLegacyArchive(ctx context.Context, c OTLPServiceConfig, directory, sourceID string, execute bool) (count int, err error) {
	if ctx == nil || sourceID == "" || len(sourceID) > 256 || !utf8.ValidString(sourceID) || strings.ContainsAny(sourceID, "\r\n\x00") {
		return 0, errors.New("valid legacy archive source identity required")
	}
	c.defaults()
	if len(c.Destinations) < 1 || len(c.Destinations) > 64 {
		return 0, errors.New("legacy recovery requires configured OTLP destinations")
	}
	st, e := os.Lstat(directory)
	if e != nil || !st.IsDir() {
		return 0, errors.New("existing non-symlink legacy child directory required")
	}
	root, e := os.OpenRoot(directory)
	if e != nil {
		return 0, errors.New("cannot open legacy archive directory")
	}
	defer root.Close()
	archive, e := journal.OpenClosedRecoveryArchive(root, max(4096, min(64<<20, c.MaxDecodedBytes)))
	if e != nil {
		return 0, errors.New("cannot exclusively own closed legacy archive; stop its collector first")
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	limits := otlpwire.Limits{WireBytes: c.MaxWireBytes, DecodedBytes: c.MaxDecodedBytes, Items: c.MaxItems}
	peers, exporters, e := configuredOTLPDestinations(c, limits)
	if e != nil {
		return 0, errors.New("invalid legacy archive destination configuration")
	}
	defer func() {
		for _, x := range exporters {
			x.Close()
		}
	}()
	envelope := func(d *journal.Data, raw []byte) (otlpstate.Envelope, error) {
		request := plogotlp.NewExportRequest()
		resource := request.Logs().ResourceLogs().AppendEmpty()
		resource.Resource().Attributes().PutStr("service.name", "go-fluentd-legacy-recovery")
		scope := resource.ScopeLogs().AppendEmpty()
		scope.Scope().SetName("go-fluentd/closed-legacy-archive")
		record := scope.LogRecords().AppendEmpty()
		record.Body().SetEmptyBytes().FromRaw(raw)
		record.Attributes().PutStr("go_fluentd.legacy.format", "journal-data-msgpack-v1")
		record.Attributes().PutStr("go_fluentd.legacy.source_id", sourceID)
		record.Attributes().PutInt("go_fluentd.legacy.record_id", d.ID)
		if tag, ok := d.Data["tag"].(string); ok {
			record.Attributes().PutStr("go_fluentd.legacy.tag", tag)
		}
		payload, e := request.MarshalProto()
		if e != nil {
			return otlpstate.Envelope{}, e
		}
		parsed, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.Protobuf, "", bytes.NewReader(payload), limits)
		if e != nil {
			return otlpstate.Envelope{}, e
		}
		for _, x := range exporters {
			if e = x.ValidateStorageRequest(parsed); e != nil {
				return otlpstate.Envelope{}, e
			}
		}
		return otlpstate.Envelope{Signal: "logs", ContentType: otlpwire.Protobuf, Items: 1, Payload: payload}, nil
	}
	// Full preflight uses the same raw snapshot and encoder as execution.
	count, e = archive.WalkRaw(ctx, func(d *journal.Data, raw []byte) error { _, e := envelope(d, raw); return e })
	if e != nil {
		return count, errors.New("legacy archive validation failed; originals retained")
	}
	if !execute {
		return count, nil
	}
	count, e = archive.WalkRaw(ctx, func(d *journal.Data, raw []byte) error {
		wrapped, e := envelope(d, raw)
		if e != nil {
			return e
		}
		for _, peer := range peers {
			result, e := peer.Send(ctx, wrapped)
			if e != nil || result.Kind != otlpstate.Accepted {
				return errors.New("legacy archive replay not fully accepted")
			}
		}
		return nil
	})
	if e != nil {
		return count, errors.New("legacy archive replay incomplete; originals retained; retry may duplicate records")
	}
	return count, nil
}
