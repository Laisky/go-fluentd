package library

//go:generate msgp

// FluentMsg is the structure of fluent message
type FluentMsg struct {
	// SourceFormat marks envelopes which must not receive legacy log metadata.
	// It is stored separately in the journal wrapper, never in event payloads.
	SourceFormat string `msg:"-" json:"-"`
	// DeliveryID is transport metadata derived by the producer, not an event ID.
	DeliveryID string `msg:"-" json:"-"`
	// JournalTag is local acknowledgement provenance, not a routing tag. It is
	// set by the journal writer/replayer and never sent to downstream services.
	JournalTag string `msg:"-" json:"-"`
	// DurableAck is an optional one-shot acceptance receipt owned by the pipeline.
	// It is not a downstream delivery ACK and is never persisted or transmitted.
	DurableAck chan error `msg:"-" json:"-"`
	Tag        string
	Message    map[string]interface{}
	ID         int64
	ExtIds     []int64
}

type FluentBatchMsg []interface{}
