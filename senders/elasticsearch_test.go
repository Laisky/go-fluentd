package senders_test

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
)

var (
	httpClient = &http.Client{ // default http client
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 20,
		},
		Timeout: 30 * time.Second,
	}
)

func TestBulk(t *testing.T) {
	cnt := ""
	buf := &bytes.Buffer{}
	// buf.Write(cnt)
	gw, err := gzip.NewWriterLevel(buf, gzip.BestCompression)
	if err != nil {
		t.Fatalf("got error: %+v", err)
	}
	url := "http://superuser:12345@172.16.4.160:8200/_bulk"

	for i := 0; i < 10; i++ {
		cnt += `{"index": {"_index": "perf-spring-logs-write", "_type": "logs"}}
{"@timestamp": "2018-12-07T12:00:00.000+08:00", "app": "test", "message": "tt5"}
{"index": {"_index": "perf-spring-logs-write", "_type": "logs"}}
{"@timestamp": "2018-12-07T12:00:00.000+08:00", "app": "test", "message": "tt5"}
`
		buf.Reset()
		gw.Reset(buf)
		if _, err := gw.Write([]byte(cnt)); err != nil {
			t.Fatalf("got error: %+v", err)
		}
		gw.Flush()
		t.Logf("send body length %v", buf.Len())
		req, err := http.NewRequest("POST", url, buf)
		if err != nil {
			t.Fatalf("got error: %+v", err)
		}
		req.Header.Set("Content-encoding", "gzip")
		t.Logf("request: %+v", req)

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("got error: %+v", err)
		}

		if err = utils.CheckResp(resp); err != nil {
			t.Fatalf("got error: %+v", err)
		}
	}
}
