package postfilters

import (
	"gofluentd/library"
	"reflect"
	"testing"
)

func TestRegressionEventDefaultFilterPreservesEnvelope(t *testing.T) {
	for _, format := range []string{"ndjson", "cloudevents"} {
		t.Run(format, func(t *testing.T) {
			expected := map[string]interface{}{"": "empty key", "a.b": "original dotted key", "msgid": "producer-owned", "subject": "untruncated 世界", "nested": map[string]interface{}{"key": int64(9223372036854775807)}}
			m := &library.FluentMsg{SourceFormat: format, Message: map[string]interface{}{}}
			for k, v := range expected {
				m.Message[k] = v
			}
			f := NewDefaultFilter(&DefaultFilterCfg{MaxLen: 3})
			if f.Filter(m) != m || !reflect.DeepEqual(m.Message, expected) {
				t.Fatalf("implicit log normalization changed event: %#v", m.Message)
			}
		})
	}
}
