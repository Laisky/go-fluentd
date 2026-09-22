package senders

import (
	"bytes"
	"compress/gzip"
)

// reset initializes the zero-value context used by HTTP workers and reuses
// buffers between complete, synchronous requests. Contexts are worker-owned.
func (b *bulkOpCtx) reset() {
	if b.buf == nil {
		b.buf = new(bytes.Buffer)
	}
	b.buf.Reset()
	if b.gzWriter == nil {
		b.gzWriter = gzip.NewWriter(b.buf)
	} else {
		b.gzWriter.Reset(b.buf)
	}
}
