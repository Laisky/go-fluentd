package recvs

import (
	"bytes"
	"io"
	"net/http"
	"sync"

	"gofluentd/library/streamformat"
)

// This bounds each idle scratch buffer, not total request memory or accepted
// message size. Large requests still pass through the configured MaxBytesReader
// but their scratch is discarded. Decoded records never alias this storage.
const maxRetainedEventInput = 128 << 10

var eventInputBuffers = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}

func recycleEventInput(b *bytes.Buffer) {
	if b.Cap() <= maxRetainedEventInput {
		b.Reset()
		eventInputBuffers.Put(b)
	}
}

// decodeEventInput owns scratch only while reading and validating a complete
// request. Decode produces owned values for every encoding; ownership regression
// tests overwrite scratch immediately after return. No scratch crosses a queue,
// durable acknowledgement, request cancellation or asynchronous transport call.
func decodeEventInput(r io.Reader, contentLength int64, format, contentType string, headers http.Header, maxRecords int) ([]map[string]interface{}, error) {
	b := eventInputBuffers.Get().(*bytes.Buffer)
	b.Reset()
	defer recycleEventInput(b)
	return decodeEventInputBuffer(b, r, contentLength, format, contentType, headers, maxRecords)
}

func decodeEventInputBuffer(b *bytes.Buffer, r io.Reader, contentLength int64, format, contentType string, headers http.Header, maxRecords int) ([]map[string]interface{}, error) {
	body, err := readEventBodyInto(b, r, contentLength)
	if err != nil {
		return nil, err
	}
	return streamformat.Decode(format, contentType, headers, body, maxRecords)
}
