package library

import (
	"bytes"
	"testing"

	"github.com/tinylib/msgp/msgp"
)

func TestRegressionFluentEncoderReusedBatchPreservesEveryRecord(t *testing.T) {
	// Verify complete wire records across reuse without depending on pool internals.
	var actual bytes.Buffer
	enc := NewFluentEncoder(&actual)
	for _, values := range [][2]string{{"first-a", "first-b"}, {"second-a", "second-b"}, {"third-a", "third-b"}} {
		actual.Reset()
		msgs := []*FluentMsg{{Tag: "logs", Message: map[string]interface{}{"value": values[0]}}, {Tag: "logs", Message: map[string]interface{}{"value": values[1]}}}
		if err := enc.EncodeBatch("logs", msgs); err != nil {
			t.Fatal(err)
		}
		if err := enc.Flush(); err != nil {
			t.Fatal(err)
		}
		expected := FluentBatchMsg{"logs", []interface{}{[]interface{}{0, msgs[0].Message}, []interface{}{0, msgs[1].Message}}}
		var want bytes.Buffer
		w := msgp.NewWriter(&want)
		if err := expected.EncodeMsg(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual.Bytes(), want.Bytes()) {
			t.Errorf("batch %v lost/replaced a record: got %x; want %x", values, actual.Bytes(), want.Bytes())
		}
	}
}
