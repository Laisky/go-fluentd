package controller

import (
	"context"
	"errors"
	"gofluentd/internal/otlpstate"
)

// ReplayAcceptedArchive is an explicit recovery operation after downstream loss.
// It uses frozen logical destination IDs and original raw envelopes. Originals
// remain retained even after another HTTP200. Ambiguous ACKs can duplicate rows.
// It is not mounted as an unauthenticated HTTP endpoint or a background loop.
func (p *OTLPJournal) ReplayAcceptedArchive(ctx context.Context) (int, error) {
	p.life.RLock()
	defer p.life.RUnlock()
	if p.closed {
		return 0, ErrOTLPJournalClosed
	}
	if !p.cfg.RetainAccepted {
		return 0, errors.New("accepted archive retention is not enabled")
	}
	if err := p.acquire(ctx, p.passGate); err != nil {
		return 0, err
	}
	defer func() { <-p.passGate }()
	n, err := p.store.WalkAcceptedArchive(ctx, p.namespace, func(key otlpstate.Key, e otlpstate.Envelope) error {
		peer, ok := p.producer.peers[key.Destination]
		if !ok {
			return errors.New("retained archive destination unavailable")
		}
		result, err := peer.Send(ctx, e)
		if err != nil || result.Kind != otlpstate.Accepted {
			return errors.New("retained archive replay not fully accepted; originals retained")
		}
		return nil
	})
	return n, err
}
