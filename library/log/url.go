package log

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// SafeURL retains only an HTTP(S) endpoint's scheme, host and escaped path.
// All userinfo, query parameters and fragments are secret-bearing. Invalid or
// opaque input never falls back to the original string. Paths themselves must
// not be used for credentials; callers logging arbitrary paths should omit them.
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Opaque != "" {
		return "<invalid URL>"
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment = nil, "", "", ""
	u.ForceQuery = false
	return u.String()
}

type safeHTTPError struct {
	operation, endpoint, category string
	cause                         error
}

func (e *safeHTTPError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.operation, e.endpoint, e.category)
}
func (e *safeHTTPError) Unwrap() error { return e.cause }

// SafeHTTPError preserves errors.Is/As without rendering a transport/parser's
// arbitrary error text (url.Error often embeds the full credential-bearing URL).
// operation must be a fixed, non-sensitive label. Do not log the unwrapped cause.
func SafeHTTPError(operation, rawURL string, err error) error {
	if err == nil {
		return nil
	}
	category := "request failed"
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		category = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		category = "deadline exceeded"
	case errors.As(err, &network) && network.Timeout():
		category = "timeout"
	}
	return &safeHTTPError{operation: operation, endpoint: SafeURL(rawURL), category: category, cause: err}
}
