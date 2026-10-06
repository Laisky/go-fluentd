package senders

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gofluentd/library"
)

func TestRegressionSenderURLsAndErrorsDoNotLeakCredentials(t *testing.T) {
	if os.Getenv("GOFLUENTD_URL_TEST_CHILD") != "" {
		for _, addr := range []string{
			"http://sentinel-user:sentinel-pass@example.invalid/_bulk?token=sentinel-query#sentinel-fragment",
			"http://sentinel%2Duser:sentinel%2Dpass@example.invalid/_bulk?token=sentinel-query",
			"http://sentinel-user:sentinel-pass@example.invalid/%zz?token=sentinel-query",
		} {
			for _, kind := range []string{"es", "http"} {
				transport := regressionRoundTripper(func(*http.Request) (*http.Response, error) { return nil, context.Canceled })
				msgs := []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"message": "safe"}}}
				var err error
				if kind == "es" {
					s := NewElasticSearchSender(&ElasticSearchSenderCfg{Name: "es", Addr: addr, TagIndexMap: map[string]string{"logs": "logs"}})
					s.httpClient = &http.Client{Transport: transport}
					err = s.SendBulkMsgs(&bulkOpCtx{}, msgs)
				} else {
					s := NewHTTPSender(&HTTPSenderCfg{Name: "http", Addr: addr})
					s.httpClient = &http.Client{Transport: transport}
					err = s.SendBulkMsgs(&bulkOpCtx{}, msgs)
				}
				if err == nil {
					t.Fatal("expected request error")
				}
				fmt.Printf("request failure: %+v\n", err)
			}
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestRegressionSenderURLsAndErrorsDoNotLeakCredentials$")
	child.Env = append(os.Environ(), "GOFLUENTD_URL_TEST_CHILD=1")
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess: %v\n%s", err, output)
	}
	for _, secret := range []string{"sentinel-user", "sentinel-pass", "sentinel-query", "sentinel-fragment", "sentinel%2Duser", "sentinel%2Dpass"} {
		if strings.Contains(string(output), secret) {
			t.Errorf("diagnostics leaked %s", secret)
		}
	}
	if !strings.Contains(string(output), "example.invalid/_bulk") {
		t.Fatal("lost useful endpoint diagnostic")
	}
}
