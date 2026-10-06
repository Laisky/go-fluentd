package log

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestRegressionSafeURLAllowlist(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://user:pass@host.test:9243/_bulk?token=secret&unknown=hidden#fragment", "https://host.test:9243/_bulk"},
		{"https://u%40ser:p%2Fass@[::1]:9243/a%20b?secret=x", "https://[::1]:9243/a%20b"},
		{"https://host.test/path?", "https://host.test/path"},
		{"https://host.test/path", "https://host.test/path"},
		{"https://secret@host.test/%zz?token=secret", "<invalid URL>"},
		{"http:secret@opaque?token=secret", "<invalid URL>"},
		{"file:///secret", "<invalid URL>"},
		{"//secret@host/path", "<invalid URL>"},
		{"https://secret%zz@host/path", "<invalid URL>"},
		{"https:///secret", "<invalid URL>"},
	} {
		if got := SafeURL(tc.raw); got != tc.want {
			t.Errorf("SafeURL returned %q; want %q", got, tc.want)
		}
	}
}

func TestRegressionSafeHTTPErrorPreservesCauseWithoutRenderingIt(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("sentinel-cause")} {
		original := &url.Error{Op: "POST", URL: "https://sentinel-user:sentinel-pass@host.test/path?token=sentinel-query", Err: cause}
		err := SafeHTTPError("send batch", original.URL, original)
		if !errors.Is(err, cause) {
			t.Fatal("lost original error identity")
		}
		var got *url.Error
		if !errors.As(err, &got) || got != original {
			t.Fatal("lost typed cause")
		}
		for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%v", err)} {
			if strings.Contains(rendered, "sentinel-") {
				t.Fatalf("unsafe rendering %q", rendered)
			}
			if !strings.Contains(rendered, "host.test/path") || !strings.Contains(rendered, "send batch") {
				t.Fatal("lost endpoint or operation")
			}
		}
	}
	if SafeHTTPError("operation", "invalid", nil) != nil {
		t.Fatal("nil error changed")
	}
}
