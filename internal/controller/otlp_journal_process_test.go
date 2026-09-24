//go:build linux

package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/controller"
	"gofluentd/internal/otlphttp"
	"gofluentd/library/otlpwire"
)

// Child runs the real owner and HTTP handler, not manually authored journal
// records. The parent controls one replay snapshot then kills the process.
func TestOTLPJournalChild(t *testing.T) {
	root := os.Getenv("OTLP_LIFECYCLE_ROOT")
	if root == "" {
		return
	}
	phase := os.Getenv("OTLP_LIFECYCLE_PHASE")
	var peers []controller.OTLPDestination
	for _, id := range []string{"a", "b", "c"} {
		x, e := otlphttp.NewExporter(otlphttp.ExporterConfig{Endpoints: map[otlpwire.Signal]string{otlpwire.Logs: os.Getenv("OTLP_LIFECYCLE_PEER") + "/" + id}, MaxAttempts: 1})
		if e != nil {
			t.Fatal(e)
		}
		defer x.Close()
		peers = append(peers, controller.OTLPDestination{ID: id, Send: x.Send})
	}
	p, e := controller.OpenOTLPJournal(context.Background(), controller.OTLPJournalConfig{Directory: root, Compress: os.Getenv("OTLP_LIFECYCLE_GZIP") == "true", ReplayBatch: 1}, peers)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	h, e := otlphttp.NewReceiver(otlphttp.ReceiverConfig{}, p.Admit)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	publish := func(name string, v interface{}) {
		b, _ := json.Marshal(v)
		f, e := os.OpenFile(filepath.Join(root, name+"-"+phase), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(b); e != nil {
			t.Fatal(e)
		}
		if e = f.Sync(); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
	}
	publish("ready", map[string]string{"url": server.URL, "namespace": p.Namespace()})
	for {
		if _, e = os.Stat(filepath.Join(root, "process-"+phase)); e == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, e := lifecycleDrain(t, p)
	message := ""
	if e != nil {
		message = e.Error()
	}
	publish("result", struct {
		Report controller.OTLPBatchReport
		Error  string
	}{r, message})
	select {} // Never graceful-close before the parent's SIGKILL.
}
func TestOTLPJournalHTTPAcceptedSurvivesSIGKILL(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(strconv.FormatBool(gzip), func(t *testing.T) {
			root := t.TempDir()
			var a, b, c atomic.Int32
			var healthy atomic.Bool
			payload := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"survive-é🙂"}}]}]}],"unknown":"18446744073709551615"}`)
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, e := io.ReadAll(r.Body)
				if e != nil || !bytes.Equal(got, payload) {
					t.Error("process changed source bytes")
				}
				w.Header().Set("Content-Type", otlpwire.JSON)
				switch r.URL.Path {
				case "/a":
					a.Add(1)
				case "/b":
					b.Add(1)
					if !healthy.Load() {
						w.WriteHeader(503)
						return
					}
				case "/c":
					c.Add(1)
					io.WriteString(w, `{"partialSuccess":{"rejectedLogRecords":"1"}}`)
					return
				default:
					t.Error("unexpected peer route")
				}
				io.WriteString(w, `{}`)
			}))
			defer peer.Close()
			namespace := ""
			for phase := 0; phase < 3; phase++ {
				phaseName := strconv.Itoa(phase)
				logPath := filepath.Join(t.TempDir(), "child.log")
				f, e := os.Create(logPath)
				if e != nil {
					t.Fatal(e)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestOTLPJournalChild$", "-test.timeout=30s")
				cmd.Env = append(os.Environ(), "OTLP_LIFECYCLE_ROOT="+root, "OTLP_LIFECYCLE_PHASE="+phaseName, "OTLP_LIFECYCLE_PEER="+peer.URL, "OTLP_LIFECYCLE_GZIP="+strconv.FormatBool(gzip))
				cmd.Stdout, cmd.Stderr = f, f
				if e = cmd.Start(); e != nil {
					t.Fatal(e)
				}
				exited := make(chan error, 1)
				go func() { exited <- cmd.Wait() }()
				killed := false
				t.Cleanup(func() {
					if !killed {
						cmd.Process.Kill()
						<-exited
					}
					f.Close()
				})
				read := func(name string) []byte {
					deadline := time.Now().Add(15 * time.Second)
					for time.Now().Before(deadline) {
						v, e := os.ReadFile(filepath.Join(root, name+"-"+phaseName))
						if e == nil && json.Valid(v) {
							return v
						}
						select {
						case e := <-exited:
							killed = true
							logs, _ := os.ReadFile(logPath)
							t.Fatalf("child exited: %v\n%s", e, logs)
						default:
						}
						time.Sleep(5 * time.Millisecond)
					}
					logs, _ := os.ReadFile(logPath)
					t.Fatalf("child checkpoint missing\n%s", logs)
					return nil
				}
				var ready map[string]string
				json.Unmarshal(read("ready"), &ready)
				if phase == 0 {
					namespace = ready["namespace"]
				} else if ready["namespace"] != namespace {
					t.Fatal("SIGKILL regenerated namespace")
				}
				if phase == 0 {
					resp, e := http.Post(ready["url"]+"/v1/logs", otlpwire.JSON, bytes.NewReader(payload))
					if e != nil {
						t.Fatal(e)
					}
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()
					if resp.StatusCode != 200 || string(body) != "{}" {
						t.Fatal("source not durably accepted", resp.StatusCode, string(body))
					}
				}
				if phase == 1 {
					healthy.Store(true)
				}
				if e = os.WriteFile(filepath.Join(root, "process-"+phaseName), nil, 0600); e != nil {
					t.Fatal(e)
				}
				var result struct {
					Report controller.OTLPBatchReport
					Error  string
				}
				if e = json.Unmarshal(read("result"), &result); e != nil {
					t.Fatal(e)
				}
				switch phase {
				case 0:
					if result.Report.Pending != 1 || result.Report.Released != 0 || !strings.Contains(result.Error, "unresolved") {
						t.Fatal(result)
					}
				case 1:
					if result.Report.Quarantined != 1 || result.Report.Released != 1 || result.Report.Delivered != 0 || result.Error != "" {
						t.Fatal(result)
					}
				case 2:
					if result.Report.Seen != 0 || result.Error != "" {
						t.Fatal(result)
					}
				}
				if e = cmd.Process.Kill(); e != nil {
					t.Fatal(e)
				}
				if e = <-exited; e == nil {
					t.Fatal("child exited without SIGKILL")
				}
				killed = true
				f.Close()
				if dir := os.Getenv("OTLP_LIFECYCLE_EVIDENCE"); dir != "" {
					os.MkdirAll(dir, 0700)
					logs, _ := os.ReadFile(logPath)
					os.WriteFile(filepath.Join(dir, fmt.Sprintf("gzip-%v-phase-%d.log", gzip, phase)), logs, 0600)
				}
			}
			if a.Load() != 1 || b.Load() != 2 || c.Load() != 1 {
				t.Fatalf("known outcomes resent after SIGKILL: A=%d B=%d C=%d", a.Load(), b.Load(), c.Load())
			}
		})
	}
}
