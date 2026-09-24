package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync/atomic"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
)

var ErrOTLPDeliveryPending = errors.New("OTLP delivery has unresolved destinations")

const maxOTLPDestinations = 64

// OTLPDestination has a stable, opaque operator-assigned ID. Credentials and
// URLs belong to the callback, not the persisted record. Send classifies one
// remote attempt and must honor ctx; protocol-specific retries belong above it.
type OTLPDestination struct {
	ID   string
	Send func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error)
}

// OTLPRecord is the immutable admission plan. Persist it before acknowledging
// input or calling Process. Required is captured at admission, never reconstructed
// from the current routing table during replay. Namespace must survive restarts
// and distinguish journal directories/generations that could reuse an ID.
type OTLPRecord struct {
	Namespace string             `json:"namespace"`
	ID        int64              `json:"id"`
	Required  []string           `json:"required"`
	Envelope  otlpstate.Envelope `json:"envelope"`
}

// JournalData returns an owned wrapper for a dedicated OTLP journal. It is not
// the legacy log journal's tag/message schema. Adapters must keep the two apart.
func (r OTLPRecord) JournalData() (*journal.Data, error) {
	if err := validateOTLPPlan(r); err != nil {
		return nil, err
	}
	b, err := json.Marshal(struct {
		Version int        `json:"version"`
		Record  OTLPRecord `json:"record"`
	}{1, r})
	if err != nil {
		return nil, err
	}
	return &journal.Data{ID: r.ID, Data: map[string]interface{}{"otlp_delivery": b}}, nil
}

// OTLPRecordFromJournal decodes the persisted plan, not a new configuration.
// Payload bytes inside the wrapper remain opaque and are not re-encoded as OTLP.
func OTLPRecordFromJournal(d *journal.Data) (OTLPRecord, error) {
	if d == nil || len(d.Data) != 1 {
		return OTLPRecord{}, errors.New("invalid OTLP journal wrapper")
	}
	b, ok := d.Data["otlp_delivery"].([]byte)
	if !ok || len(b) > 90<<20 {
		return OTLPRecord{}, errors.New("invalid OTLP journal payload")
	}
	var v struct {
		Version int        `json:"version"`
		Record  OTLPRecord `json:"record"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return OTLPRecord{}, err
	}
	var extra interface{}
	if dec.Decode(&extra) != io.EOF || v.Version != 1 || v.Record.ID != d.ID {
		return OTLPRecord{}, errors.New("invalid OTLP journal version or identity")
	}
	if err := validateOTLPPlan(v.Record); err != nil {
		return OTLPRecord{}, err
	}
	return v.Record, nil
}

func validateOTLPPlan(r OTLPRecord) error {
	if len(r.Required) == 0 || len(r.Required) > maxOTLPDestinations {
		return errors.New("OTLP record needs 1 to 64 required destinations")
	}
	// Check the plan's structural and absolute bounds. The store's complete
	// identity/envelope validation and possibly smaller limits are also checked
	// for every destination before Process has any effects.
	seen := make(map[string]bool, len(r.Required))
	for _, id := range r.Required {
		if id == "" || seen[id] {
			return errors.New("empty or duplicate OTLP destination")
		}
		seen[id] = true
	}
	if r.Namespace == "" || r.ID < 0 || r.Envelope.Items < 0 || len(r.Envelope.Payload) > 64<<20 {
		return errors.New("invalid OTLP record")
	}
	return nil
}

// OTLPDestinationResult describes this processing pass. Replayed receipts do not
// count as new deliveries. A partial rejection is never full acceptance.
type OTLPDestinationResult struct {
	Destination string
	Result      otlpstate.Result
	Err         error
}

type OTLPDeliveryReport struct {
	Destinations    []OTLPDestinationResult
	Resolved        bool // every required destination is durably accepted/quarantined
	FullyDelivered  bool // every required destination is durably fully accepted
	JournalReleased bool // release callback completed successfully in this call
}

// OTLPCounters are process-local observations, not reconstructed lifetime totals.
// Accepted/Quarantined count freshly persisted destination envelopes, not items.
// Cached replay results increment ReplayHits instead. Partial item counts are
// available in individual results; no rejected subset is inferred.
type OTLPCounters struct {
	Accepted, Quarantined, Retryable, Blocked, ReplayHits uint64
}

type OTLPProducer struct {
	store                                                 *otlpstate.Store
	peers                                                 map[string]OTLPDestination
	ids                                                   []string
	accepted, quarantined, retryable, blocked, replayHits atomic.Uint64
}

func NewOTLPProducer(store *otlpstate.Store, destinations []OTLPDestination) (*OTLPProducer, error) {
	if store == nil || len(destinations) == 0 || len(destinations) > maxOTLPDestinations {
		return nil, errors.New("OTLP producer requires a store and 1 to 64 destinations")
	}
	p := &OTLPProducer{store: store, peers: make(map[string]OTLPDestination, len(destinations))}
	for _, d := range destinations {
		if d.Send == nil {
			return nil, errors.New("nil OTLP destination callback")
		}
		if _, exists := p.peers[d.ID]; exists {
			return nil, errors.New("duplicate OTLP destination")
		}
		if err := store.Validate(otlpstate.Key{Destination: d.ID, Journal: "validation", RecordID: 0}, otlpstate.Envelope{Signal: "logs", ContentType: "application/json"}); err != nil {
			return nil, err
		}
		p.peers[d.ID] = d
		p.ids = append(p.ids, d.ID)
	}
	sort.Strings(p.ids)
	return p, nil
}

// Plan snapshots the required destinations and owns its payload. The caller must
// durably write JournalData before input acceptance. Later config additions must
// not silently add destinations to a retained plan; removing one blocks replay.
func (p *OTLPProducer) Plan(namespace string, id int64, e otlpstate.Envelope) (OTLPRecord, error) {
	r := OTLPRecord{Namespace: namespace, ID: id, Required: append([]string(nil), p.ids...), Envelope: e}
	if err := p.validate(r); err != nil {
		return OTLPRecord{}, err
	}
	r.Envelope.Payload = bytes.Clone(e.Payload)
	return r, nil
}

func (p *OTLPProducer) validate(r OTLPRecord) error {
	if err := validateOTLPPlan(r); err != nil {
		return err
	}
	for _, id := range r.Required {
		if _, exists := p.peers[id]; !exists {
			return fmt.Errorf("required OTLP destination %q is unavailable", id)
		}
		if err := p.store.Validate(otlpstate.Key{Destination: id, Journal: r.Namespace, RecordID: r.ID}, r.Envelope); err != nil {
			return err
		}
	}
	return nil
}

func (p *OTLPProducer) Counters() OTLPCounters {
	return OTLPCounters{p.accepted.Load(), p.quarantined.Load(), p.retryable.Load(), p.blocked.Load(), p.replayHits.Load()}
}

// Process executes one bounded pass, without sleeping or creating goroutines.
// Separate records can run concurrently; same-destination receipts are serialized
// by Store. All required destinations are validated before contacting any peer.
// release must idempotently complete the local WAL acknowledgement and its Sync;
// it is called only after every required destination is durable. Quarantine can
// release WAL ownership because its separate receipt retains the whole payload,
// but is NOT delivered. A release error preserves receipts and is safe to retry.
// The producer does not own/close Store. Inputs must not be mutated concurrently.
func (p *OTLPProducer) Process(ctx context.Context, r OTLPRecord, release func(context.Context, int64) error) (OTLPDeliveryReport, error) {
	var report OTLPDeliveryReport
	if ctx == nil || release == nil {
		return report, errors.New("nil OTLP context or release callback")
	}
	if err := p.validate(r); err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	r.Required = append([]string(nil), r.Required...)
	r.Envelope.Payload = bytes.Clone(r.Envelope.Payload)
	allResolved, allAccepted := true, true
	var errs []error
	for _, id := range r.Required {
		result, err := p.store.DoDelivery(ctx, otlpstate.Key{Destination: id, Journal: r.Namespace, RecordID: r.ID}, r.Envelope, p.peers[id].Send)
		report.Destinations = append(report.Destinations, OTLPDestinationResult{id, result, err})
		if err != nil {
			p.blocked.Add(1)
			allResolved, allAccepted = false, false
			errs = append(errs, fmt.Errorf("OTLP destination %q: %w", id, err))
			continue
		}
		if result.Replayed {
			p.replayHits.Add(1)
		}
		switch {
		case result.Durable && result.Outcome.Kind == otlpstate.Accepted && !result.Quarantined:
			if !result.Replayed {
				p.accepted.Add(1)
			}
		case result.Durable && result.Quarantined:
			allAccepted = false
			if !result.Replayed {
				p.quarantined.Add(1)
			}
		default:
			allResolved, allAccepted = false, false
			p.retryable.Add(1)
		}
	}
	report.Resolved, report.FullyDelivered = allResolved, allAccepted
	if !allResolved {
		return report, errors.Join(append(errs, ErrOTLPDeliveryPending)...)
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := release(ctx, r.ID); err != nil {
		return report, fmt.Errorf("OTLP journal release: %w", err)
	}
	report.JournalReleased = true
	return report, nil
}
