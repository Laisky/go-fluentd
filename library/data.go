package library

//go:generate msgp

// FluentMsg is the structure of fluent message
type FluentMsg struct {
	Tag     string
	Message map[string]interface{}
	ID      int64
	ExtIds  []int64
}

type FluentBatchMsg []interface{}
