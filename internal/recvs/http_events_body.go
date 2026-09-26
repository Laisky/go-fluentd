package recvs

import (
	"bytes"
	"io"
)

// readEventBody returns private storage for callers that retain the raw body.
func readEventBody(r io.Reader, contentLength int64) ([]byte, error) {
	return readEventBodyInto(new(bytes.Buffer), r, contentLength)
}

// contentLength is only a bounded capacity hint, never a framing or acceptance
// condition. The caller still applies http.MaxBytesReader. Returned bytes belong
// to buf; its owner must finish decoding before resetting or recycling it.
func readEventBodyInto(buf *bytes.Buffer, r io.Reader, contentLength int64) ([]byte, error) {
	buf.Reset()
	if contentLength > 0 && contentLength <= 64<<10 {
		// Leave bytes.MinRead spare so the EOF probe does not double capacity.
		buf.Grow(int(contentLength) + bytes.MinRead)
	}
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}
