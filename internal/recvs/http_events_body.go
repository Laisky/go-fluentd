package recvs

import (
	"bytes"
	"io"
)

// contentLength is only a bounded capacity hint, never a framing or acceptance
// condition. The caller still applies http.MaxBytesReader. Unknown, large or
// mismatched lengths retain complete-read and error behavior. The buffer is
// private to this request; no pooled storage can overwrite decoded values.
func readEventBody(r io.Reader, contentLength int64) ([]byte, error) {
	if contentLength <= 0 || contentLength > 64<<10 {
		return io.ReadAll(r)
	}
	// Leave bytes.MinRead spare so the final EOF probe does not double capacity.
	buf := bytes.NewBuffer(make([]byte, 0, int(contentLength)+bytes.MinRead))
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}
