package library

//go:generate msgp

// FluentMsg is the structure of fluent message
type FluentMsg struct {
	// JournalTag is local acknowledgement provenance, not a routing tag. It is
	// set by the journal writer/replayer and never sent to downstream services.
	JournalTag string `msg:"-" json:"-"`
	Tag        string
	Message    map[string]interface{}
	ID         int64
	ExtIds     []int64
}

type FluentBatchMsg []interface{}
