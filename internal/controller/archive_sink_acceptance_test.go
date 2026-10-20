//go:build linux

package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gofluentd/library/otlpwire"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in isolated acceptance only. The orchestrator provides an owned sink in
// a network-none anchor namespace; this never targets production endpoints.
func TestOverallArchiveVictoriaLogsSinkKillRecovery(t *testing.T) {
	sink := os.Getenv("GOFLUENTD_ARCHIVE_TEST_SINK")
	if sink == "" {
		t.Skip("requires isolated labeled VictoriaLogs acceptance sink")
	}
	if !strings.HasPrefix(sink, "task20261018-archive-retention-") {
		t.Fatal("unowned sink name")
	}
	owner, e := exec.Command("docker", "inspect", "--format", "{{index .Config.Labels \"com.laisky.task\"}}", sink).Output()
	if e != nil || strings.TrimSpace(string(owner)) != "task20261017-07-zero-loss" {
		t.Fatal("sink ownership not verified", e)
	}
	base := "http://127.0.0.1:9428"
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path string) ([]byte, error) {
		req, e := http.NewRequest(method, base+path, nil)
		if e != nil {
			return nil, e
		}
		resp, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("isolated sink status %d", resp.StatusCode)
		}
		return b, e
	}
	waitReady := func() {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, e := request("GET", "/metrics"); e == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("isolated sink did not restart")
	}
	rows := func() int {
		q := url.Values{"query": []string{"_time:1h otel.storage.format:=\"otlp-record-base64-v1\" | stats count() as rows"}}
		b, e := request("GET", "/select/logsql/query?"+q.Encode())
		if e != nil {
			t.Fatal(e)
		}
		var out struct {
			Rows string `json:"rows"`
		}
		if e = json.Unmarshal(bytes.TrimSpace(b), &out); e != nil {
			t.Fatal(e)
		}
		n, e := strconv.Atoi(out.Rows)
		if e != nil {
			t.Fatal(e)
		}
		return n
	}
	waitReady()
	waitGatewaySent := func(want float64) {
		sentPattern := regexp.MustCompile(`(?m)^otelcol_exporter_sent_log_records(?:_total)?(?:\{[^\n]*\})? ([0-9.e+]+)$`)
		queuePattern := regexp.MustCompile(`(?m)^otelcol_exporter_queue_size(?:\{[^\n]*\})? ([0-9.e+]+)$`)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			resp, e := client.Get("http://127.0.0.1:8888/metrics")
			if e == nil {
				raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				resp.Body.Close()
				sent, queue := sentPattern.FindSubmatch(raw), queuePattern.FindSubmatch(raw)
				if len(sent) == 2 && len(queue) == 2 {
					n, _ := strconv.ParseFloat(string(sent[1]), 64)
					q, _ := strconv.ParseFloat(string(queue[1]), 64)
					if n >= want && q == 0 {
						return
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("gateway did not finish transport and relinquish its queue")
	}
	cfg := nativeStorageTestConfig(t, "http://127.0.0.1:4319", 1<<20)
	cfg.StorageDirectory = sharedPrivateDir(t)
	cfg.MaxWALBytes = 8 << 20
	cfg.MaxStorageBytes = 16 << 20
	cfg.MaxResponseBytes = 512
	cfg.ReceiptGC = true
	overall := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 8 << 20, SegmentBytes: 64 << 10}
	if e = overall.ApplyOTLP(&cfg); e != nil {
		t.Fatal(e)
	}
	service := nativeStorageStart(t, cfg)
	original := archiveRequest(t, 1).Payload()
	req, e := http.NewRequest("POST", "http://"+service.Addr().String()+"/v1/logs", bytes.NewReader(original))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", otlpwire.JSON)
	req.Header.Set("Authorization", "Bearer synthetic-native-ingress")
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("collector admission", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if service.Counters().Accepted == 1 && service.owner.CapacitySnapshot().PendingUpperBound == 0 && acceptedOriginalCount(t, service.owner) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if service.Counters().Accepted != 1 || service.owner.CapacitySnapshot().PendingUpperBound != 0 || acceptedOriginalCount(t, service.owner) != 1 {
		entries, _ := os.ReadDir(cfg.StorageDirectory)
		for _, entry := range entries {
			if len(entry.Name()) == 69 && strings.HasSuffix(entry.Name(), ".json") {
				raw, _ := os.ReadFile(filepath.Join(cfg.StorageDirectory, entry.Name()))
				var receipt struct {
					Entry struct {
						Outcome struct {
							Kind        string `json:"kind"`
							Status      int    `json:"http_status"`
							Diagnostic  string `json:"diagnostic"`
							ContentType string `json:"response_content_type"`
							Truncated   bool   `json:"truncated"`
							Response    []byte `json:"response"`
						} `json:"outcome"`
					} `json:"entry"`
				}
				if json.Unmarshal(raw, &receipt) == nil {
					t.Logf("synthetic fixture outcome: %+v", receipt.Entry.Outcome)
				}
			}
		}
		t.Fatalf("archive readiness: counters=%+v capacity=%+v owner_fault=%v", service.Counters(), service.owner.CapacitySnapshot(), service.owner.Err())
	}
	waitGatewaySent(1)
	if _, e = request("POST", "/internal/force_flush"); e != nil {
		t.Fatal(e)
	} // test-only visibility control, not a durability mechanism.
	before := rows()
	if before != 1 {
		t.Fatal("fixture was not queryable before kill", before)
	}
	if out, e := exec.Command("docker", "kill", "--signal", "KILL", sink).CombinedOutput(); e != nil {
		t.Fatal("isolated sink kill failed", string(out), e)
	}
	if out, e := exec.Command("docker", "start", sink).CombinedOutput(); e != nil {
		t.Fatal("isolated sink restart failed", string(out), e)
	}
	waitReady()
	after := rows()
	if after != 0 {
		t.Fatal("no acknowledged volatile loss reproduced; cannot claim this recovery acceptance", after)
	}
	if acceptedOriginalCount(t, service.owner) != 1 {
		t.Fatal("last durable recovery copy lost after backend ACK")
	}
	// Exact-full admission headroom must not prevent read-only archive replay.
	snap, _ := service.owner.budget.Snapshot(t.Context())
	if e = service.owner.budget.Reserve(t.Context(), snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	n, e := service.owner.ReplayAcceptedArchive(t.Context())
	if e != nil || n != 1 {
		t.Fatal("archive replay failed", n, e)
	}
	waitGatewaySent(2)
	if _, e = request("POST", "/internal/force_flush"); e != nil {
		t.Fatal(e)
	}
	recovered := rows()
	if recovered != 1 {
		t.Fatal("acknowledged original not recovered", recovered)
	}
	q := url.Values{"query": []string{"_time:1h otel.storage.format:=\"otlp-record-base64-v1\" | fields otel.record.otlp"}}
	b, e := request("GET", "/select/logsql/query?"+q.Encode())
	if e != nil {
		t.Fatal(e)
	}
	var row map[string]string
	if e = json.Unmarshal(bytes.TrimSpace(b), &row); e != nil {
		t.Fatal(e)
	}
	archive, e := base64.StdEncoding.DecodeString(row["otel.record.otlp"])
	if e != nil {
		t.Fatal(e)
	}
	var got, want interface{}
	if e = json.Unmarshal(archive, &got); e != nil {
		t.Fatal(e)
	}
	json.Unmarshal(original, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("recovered raw body/nested/unknown context changed")
	}
	if acceptedOriginalCount(t, service.owner) != 1 {
		t.Fatal("recovery success discarded source archive")
	}
	t.Logf("PASS: queryable=%d; after sink SIGKILL=%d; archive replay=%d; recovered=%d; original raw semantic equality; source archive retained; full admission budget", before, after, n, recovered)
}
