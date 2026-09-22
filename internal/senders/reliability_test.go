package senders

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"gofluentd/library"
)

type regressionRoundTripper func(*http.Request) (*http.Response, error)

func (f regressionRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type regressionBody struct {
	io.Reader
	closed bool
}

func (b *regressionBody) Close() error { b.closed = true; return nil }

func regressionBulkContext() *bulkOpCtx {
	buf := new(bytes.Buffer)
	return &bulkOpCtx{buf: buf, gzWriter: gzip.NewWriter(buf)}
}

func regressionHTTP(t *testing.T, zeroContext bool) {
	t.Helper()
	s := NewHTTPSender(&HTTPSenderCfg{Name: "http-test", Addr: "http://example.invalid/logs"})
	body := &regressionBody{Reader: strings.NewReader(`{}`)}
	requests := 0
	s.httpClient = &http.Client{Transport: regressionRoundTripper(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("missing gzip content encoding")
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("open gzip request: %v", err)
		} else {
			data, err := io.ReadAll(gz)
			if err != nil {
				t.Errorf("request must contain a complete gzip stream: %v", err)
			}
			gz.Close()
			var got []map[string]interface{}
			if err := json.Unmarshal(data, &got); err != nil || len(got) != 1 || got[0]["message"] != "hello" {
				t.Errorf("unexpected request payload: %s (%v)", data, err)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})}
	ctx := regressionBulkContext()
	if zeroContext {
		ctx = &bulkOpCtx{}
	}
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("zero-value bulk context must not panic: %v", p)
		}
	}()
	if err := s.SendBulkMsgs(ctx, []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"message": "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("requests=%d, want 1", requests)
	}
	if !body.closed {
		t.Error("HTTP response body was not closed")
	}
}

func TestRegressionHTTPZeroBulkContext(t *testing.T) { regressionHTTP(t, true) }
func TestRegressionHTTPCompleteGzipAndCloseBody(t *testing.T) { regressionHTTP(t, false) }

func TestRegressionHTTPFailureIsReturned(t *testing.T) {
	s := NewHTTPSender(&HTTPSenderCfg{Name: "http-test", Addr: "http://example.invalid/logs"})
	body := &regressionBody{Reader: strings.NewReader(`unavailable`)}
	s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: body}, nil
	})}
	if err := s.SendBulkMsgs(regressionBulkContext(), []*library.FluentMsg{{Message: map[string]interface{}{"x": "y"}}}); err == nil {
		t.Error("503 response must fail")
	}
	if !body.closed {
		t.Error("failed response body was not closed")
	}
}

func regressionES() *ElasticSearchSender {
	return NewElasticSearchSender(&ElasticSearchSenderCfg{Name: "es-test", Addr: "http://example.invalid/_bulk", TagIndexMap: map[string]string{"logs": "logs"}})
}

func TestRegressionElasticsearchResponse(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status int
		wantErr bool
	}{
		{"success", `{"errors":false,"items":[{"index":{"status":201}}]}`, 200, false},
		{"partial_failure", `{"errors":true,"items":[{"index":{"status":429}}]}`, 200, true},
		{"malformed_json", `not-json`, 200, true},
		{"missing_result", `{}`, 200, true},
		{"null_result", `null`, 200, true},
		{"http_failure", `{"errors":false}`, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}
			defer resp.Body.Close()
			err := regressionES().checkResp(resp)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkResp(%q): err=%v, wantErr=%v", tc.body, err, tc.wantErr)
			}
		})
	}
}

func TestRegressionElasticsearchDoesNotSilentlySkipMessages(t *testing.T) {
	good := &library.FluentMsg{Tag: "logs", Message: map[string]interface{}{"message": "ok"}}
	for _, tc := range []struct {
		name string
		msgs []*library.FluentMsg
	}{
		{"missing_index", []*library.FluentMsg{{Tag: "unknown", Message: map[string]interface{}{"x": "y"}}}},
		{"mixed_missing_index", []*library.FluentMsg{good, {Tag: "unknown", Message: map[string]interface{}{"x": "y"}}}},
		{"unencodable", []*library.FluentMsg{{Tag: "logs", Message: map[string]interface{}{"bad": make(chan int)}}}},
		{"mixed_unencodable", []*library.FluentMsg{good, {Tag: "logs", Message: map[string]interface{}{"bad": make(chan int)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := regressionES()
			requests := 0
			s.httpClient = &http.Client{Transport: regressionRoundTripper(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errors":false,"items":[{"index":{"status":201}}]}`))}, nil
			})}
			if err := s.SendBulkMsgs(regressionBulkContext(), tc.msgs); err == nil {
				t.Error("invalid message must not be silently acknowledged")
			}
			if requests != 0 {
				t.Errorf("sent a partial batch (%d requests); prepare all records before sending", requests)
			}
		})
	}
}

func TestRegressionElasticsearchMetadataEscapesIndex(t *testing.T) {
	s := regressionES()
	index := "logs\"\\test"
	s.TagIndexMap["logs"] = index
	b, err := s.getMsgStarting(&library.FluentMsg{Tag: "logs"})
	if err != nil { t.Fatal(err) }
	var got struct { Index struct { Name string `json:"_index"` } `json:"index"` }
	if err := json.Unmarshal(b, &got); err != nil { t.Fatalf("invalid bulk metadata %q: %v", b, err) }
	if got.Index.Name != index { t.Errorf("index=%q, want %q", got.Index.Name, index) }
}
