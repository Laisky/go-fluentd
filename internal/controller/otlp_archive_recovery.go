package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
)

// RecoverOTLPAcceptedArchive runs offline, holding the existing exclusive
// disposition lock. It starts no listener or background replay, writes no WAL
// or receipts and never deletes the recovery source. Without execute it only
// validates and counts. Endpoints/secrets never enter the returned diagnostics.
func RecoverOTLPAcceptedArchive(ctx context.Context, c OTLPServiceConfig, execute bool) (count int, err error) {
	if ctx == nil || !c.RetainAccepted || c.StorageDirectory == "" {
		return 0, errors.New("retained archive recovery requires overall disk budget configuration")
	}
	c.defaults()
	// Never initialize a fresh root or create its first ownership lock in recovery.
	for _, name := range []string{"generation.json", ".otlp-disposition.lock"} {
		st, e := os.Lstat(filepath.Join(c.StorageDirectory, name))
		if e != nil || !st.Mode().IsRegular() {
			return 0, errors.New("existing retained archive metadata required")
		}
	}
	store, e := otlpstate.Open(c.StorageDirectory, otlpstate.Limits{PayloadBytes: int(c.MaxDecodedBytes), ResponseBytes: int(c.MaxResponseBytes)})
	if e != nil {
		return 0, errors.New("cannot exclusively own retained archive; stop its collector first")
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	g, _, e := openOTLPGeneration(c.StorageDirectory, true, true)
	if e != nil || g.SharedBudgetVersion != 2 {
		return 0, errors.New("invalid retained archive generation")
	}
	ids := map[string]bool{}
	for _, d := range c.Destinations {
		if d.ID == "" || ids[d.ID] {
			return 0, errors.New("archive recovery requires unique destination IDs")
		}
		ids[d.ID] = true
	}
	// Validate every retained checksum/namespace/destination before any transport.
	count, e = store.WalkAcceptedArchive(ctx, g.Namespace, func(k otlpstate.Key, _ otlpstate.Envelope) error {
		if !ids[k.Destination] {
			return errors.New("retained archive destination unavailable")
		}
		return nil
	})
	if e != nil {
		return count, errors.New("retained archive validation failed; originals retained")
	}
	if !execute {
		return count, nil
	}
	peers, exporters, e := configuredOTLPDestinations(c, otlpwire.Limits{WireBytes: c.MaxWireBytes, DecodedBytes: c.MaxDecodedBytes, Items: c.MaxItems})
	if e != nil {
		return 0, errors.New("invalid archive destination configuration")
	}
	defer func() {
		for _, x := range exporters {
			x.Close()
		}
	}()
	byID := map[string]OTLPDestination{}
	for _, p := range peers {
		byID[p.ID] = p
	}
	count, e = store.WalkAcceptedArchive(ctx, g.Namespace, func(k otlpstate.Key, envelope otlpstate.Envelope) error {
		outcome, e := byID[k.Destination].Send(ctx, envelope)
		if e != nil || outcome.Kind != otlpstate.Accepted {
			return errors.New("archive replay incomplete; originals retained")
		}
		return nil
	})
	if e != nil {
		return count, errors.New("archive replay incomplete; originals retained; retry may duplicate records")
	}
	return count, nil
}
