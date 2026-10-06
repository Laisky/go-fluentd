package senders

import (
	"fmt"
	"io"
	"net/http"

	"gofluentd/library/log"

	utils "github.com/Laisky/go-utils"
	"github.com/pkg/errors"
)

const (
	defaultESResponseBytes int64 = 4 << 20
	maximumESResponseBytes int64 = 64 << 20
)

// Protocol/size failures cannot improve by immediately repeating the same batch.
// They still report the batch as failed: this is not a successful journal ACK.
type esProtocolError struct{ reason string }

func (e *esProtocolError) Error() string { return "Elasticsearch " + e.reason }
func retryESResponse(err error) bool {
	var protocol *esProtocolError
	return !errors.As(err, &protocol)
}

func (s *ElasticSearchSender) checkResp(resp *http.Response) error {
	if resp == nil || resp.Body == nil {
		return &esProtocolError{"response is missing a body"}
	}
	if !isStatusCodeOk(resp.StatusCode) {
		// Never read or echo an untrusted status body. The caller closes promptly
		// rather than draining a possibly unlimited stream just to reuse a socket.
		reason := fmt.Sprintf("returned status %d", resp.StatusCode)
		if resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429 {
			return &esProtocolError{reason}
		}
		return fmt.Errorf("Elasticsearch %s", reason)
	}
	limit := s.MaxResponseBytes
	// The constructor validates this; also fail closed for zero-value/internal use.
	if limit <= 0 || limit > maximumESResponseBytes {
		return &esProtocolError{"response limit is invalid"}
	}
	if resp.ContentLength > limit {
		return &esProtocolError{"response exceeds decoded byte limit"}
	}
	// net/http transparently decompresses gzip before Body.Read and resets its
	// ContentLength. Enforce the actual decoded length, never trust the header.
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if int64(len(body)) > limit {
		return &esProtocolError{"response exceeds decoded byte limit"}
	}
	if err != nil {
		return log.SafeHTTPError("read Elasticsearch response", s.Addr, err)
	}
	var result struct {
		Errors *bool `json:"errors"`
	}
	if err = utils.JSON.Unmarshal(body, &result); err != nil {
		return &esProtocolError{"response is not a valid bulk result"}
	}
	if result.Errors == nil {
		return &esProtocolError{"response is missing the errors result"}
	}
	if *result.Errors {
		return fmt.Errorf("Elasticsearch rejected one or more bulk items")
	}
	return nil
}
