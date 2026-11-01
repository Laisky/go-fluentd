package controller_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/alerts"
	"gofluentd/internal/controller"
	"gofluentd/library/otlpwire"
)

func alertLoadWord(i int) string {
	word := []byte{}
	for {
		word = append(word, byte('a'+i%26))
		i /= 26
		if i == 0 {
			return string(word)
		}
	}
}

func alertLoadBody(service, message string, size int) []byte {
	padding := ""
	if size > len(message) {
		padding = " " + strings.Repeat("x", size-len(message))
	}
	return []byte(fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":%q}}]},"scopeLogs":[{"scope":{"name":"load"},"logRecords":[{"severityNumber":17,"body":{"stringValue":%q}}]}]}]}`, service, message+padding))
}

// TestAlertSustainedLoad is a manual measurement enabled by GOFLUENTD_ALERT_LOAD.
// It drives real HTTP admission through the journal with local storage and
// Telegram peers, then reports acknowledgement latency for each observer mode and
// how long a quiet service waits for a notification while another floods.
func TestAlertSustainedLoad(t *testing.T) {
	if os.Getenv("GOFLUENTD_ALERT_LOAD") == "" {
		t.Skip("set GOFLUENTD_ALERT_LOAD to measure")
	}
	modes := []struct {
		name string
		cfg  *alerts.Config
	}{
		{name: "off"},
		{name: "tag_only", cfg: &alerts.Config{Tags: []string{"app"}}},
		{name: "other_service", cfg: &alerts.Config{Services: []string{"other"}}},
		{name: "flood", cfg: &alerts.Config{Services: []string{"noisy", "quiet"}, IncludeMessage: true}},
	}
	for _, load := range []struct{ size, requests int }{{1 << 10, 1500}, {768 << 10, 200}} {
		for _, mode := range modes {
			t.Run(fmt.Sprintf("%s_%d", mode.name, load.size), func(t *testing.T) { runAlertLoad(t, mode.name, mode.cfg, load.size, load.requests) })
		}
	}
}

func runAlertLoad(t *testing.T, name string, config *alerts.Config, size, requests int) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer peer.Close()
	var firstQuiet atomic.Int64
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "otlp/quiet") {
			firstQuiet.CompareAndSwap(0, time.Now().UnixNano())
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer telegram.Close()
	cfg := serviceConfig(t, peer.URL)
	cfg.MaxDecodedBytes, cfg.MaxWireBytes = 2<<20, 2<<20
	cfg.MaxWALBytes, cfg.MaxStorageBytes = 8<<30, 8<<30
	cfg.MaxStorageFiles, cfg.StorageScanMaxEntries, cfg.ReceiptGC = 1<<20, 1<<20, true
	var observer *alerts.Observer
	if config != nil {
		observerConfig := *config
		observerConfig.Token, observerConfig.ChatID, observerConfig.SendInterval = "123:synthetic", "synthetic", 100*time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		var err error
		if observer, err = alerts.New(ctx, observerConfig, telegram.URL, telegram.Client()); err != nil {
			cancel()
			t.Fatal(err)
		}
		defer func() { cancel(); observer.Wait() }()
		cfg.ObserveAccepted = observer.ObserveOTLP
	}
	svc := serviceStart(t, cfg)
	runAlertLoadClients(t, svc, name, requests, size, &firstQuiet, observer)
}

func runAlertLoadClients(t *testing.T, svc *controller.OTLPService, name string, requests, size int, firstQuiet *atomic.Int64, observer *alerts.Observer) {
	url := "http://" + svc.Addr().String() + "/v1/logs"
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 8}}
	samples := make([]int64, requests)
	var next, quietStarted, failures atomic.Int64
	var wg sync.WaitGroup
	started := time.Now()
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= requests {
					return
				}
				service, message := "noisy", "failure "+alertLoadWord(i)
				if i%100 == 50 {
					service, message = "quiet", "quiet service failure"
					quietStarted.CompareAndSwap(0, time.Now().UnixNano())
				}
				body := alertLoadBody(service, message, size)
				sent := time.Now()
				status, response := servicePost(t, client, url, otlpwire.JSON, "", "private-test-ingress", body)
				samples[i] = time.Since(sent).Nanoseconds()
				if status != http.StatusOK && failures.Add(1) == 1 {
					t.Logf("first rejection at request %d: status=%d body=%.200q", i, status, response)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	quiet := "n/a"
	var stats alerts.Stats
	if observer != nil {
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) && observer.Stats().EventQueueDepth != 0 {
			time.Sleep(20 * time.Millisecond)
		}
		stats = observer.Stats()
		if first := firstQuiet.Load(); first != 0 {
			quiet = time.Duration(first - quietStarted.Load()).Round(time.Millisecond).String()
		} else if name == "flood" {
			quiet = "never"
		}
	}
	t.Logf("mode=%s size=%d requests=%d failures=%d rps=%.0f p50_us=%d p95_us=%d p99_us=%d quiet_wait=%s queued=%d sent=%d dropped_events=%d dropped_requests=%d suppressed=%d",
		name, size, requests, failures.Load(), float64(requests)/elapsed.Seconds(), samples[requests/2]/1000, samples[requests*95/100]/1000, samples[requests*99/100]/1000, quiet, stats.Queued, stats.Sent, stats.DroppedEvents, stats.DroppedRequests, stats.Suppressed)
}
