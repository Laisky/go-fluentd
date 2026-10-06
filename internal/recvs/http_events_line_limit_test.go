package recvs

import (
	"gofluentd/library/streamformat"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegressionHTTPEventsLineLimitPublishesNothing(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		_, engine, out := eventsReceiver(t, streamformat.NDJSON, nil)
		// Even a valid first record must not reach the durable pipeline.
		body := `{}` + newline + strings.Repeat(newline, streamformat.MaxNDJSONLines)
		req := eventsRequest(body, "application/x-ndjson")
		req.ContentLength = -1 // Unknown/chunked length still enforces decoding limits.
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || len(out) != 0 {
			t.Fatalf("status=%d published=%d", w.Code, len(out))
		}
	}
}
