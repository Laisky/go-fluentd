package senders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gofluentd/library"
)

// Exercise framing, retries and independent concurrent messages across the
// default and enlarged write-buffer boundaries using actual HTTP transports.
func TestHTTPEventTransportBufferBoundaries(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls-%t", encrypted), func(t *testing.T) {
			sizes := []int{0, 1024, 4095, 4096, 8192, 16384, 32700, 32768, 65536, 131072}
			messages := make([]*library.FluentMsg, len(sizes))
			expected := map[string][]byte{}
			for i, size := range sizes {
				id := fmt.Sprintf("buffer-%d", i)
				m := &library.FluentMsg{ID: int64(i), DeliveryID: id,
					Message: map[string]interface{}{"id": id, "text": strings.Repeat("x", size) + "世界\"\\\n<>&", "n": uint64(18446744073709551615)}}
				messages[i] = m
				var out bytes.Buffer
				encoder := json.NewEncoder(&out)
				encoder.SetEscapeHTML(false)
				if err := encoder.Encode(m.Message); err != nil {
					t.Fatal(err)
				}
				expected[id] = out.Bytes()
			}
			var mu sync.Mutex
			attempts := map[string]int{}
			peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.Header.Get("X-Go-Fluentd-ID")
				want, exists := expected[id]
				got, err := io.ReadAll(r.Body)
				if err != nil || !exists || !bytes.Equal(got, want) || r.ContentLength != int64(len(want)) ||
					r.Method != "POST" || r.URL.Path != "/records" || r.Header.Get("Content-Type") != "application/x-ndjson" ||
					r.Header.Get("Authorization") != "Bearer buffer-test" {
					t.Errorf("changed request %s: exists=%t read=%v length=%d want=%d", id, exists, err, r.ContentLength, len(want))
				}
				if encrypted && r.ProtoMajor != 2 {
					t.Errorf("TLS case did not exercise HTTP/2: %s", r.Proto)
				}
				mu.Lock()
				attempts[id]++
				n := attempts[id]
				mu.Unlock()
				if n == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			if encrypted {
				peer.EnableHTTP2 = true
				peer.StartTLS()
			} else {
				peer.Start()
			}
			defer peer.Close()
			sender := eventSender(t, peer.URL+"/records", "ndjson", "", func(c *HTTPEventsSenderCfg) {
				c.BearerToken = "buffer-test"
				c.Timeout = 5 * time.Second
				c.MaxAttempts = 2
			})
			if encrypted {
				sender.httpClient.Transport.(*http.Transport).TLSClientConfig = peer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			var workers sync.WaitGroup
			for _, message := range messages {
				workers.Add(1)
				go func(m *library.FluentMsg) {
					defer workers.Done()
					if err := sender.Send(context.Background(), []*library.FluentMsg{m}); err != nil {
						t.Error(err)
					}
				}(message)
			}
			workers.Wait()
			mu.Lock()
			defer mu.Unlock()
			for id := range expected {
				if attempts[id] != 2 {
					t.Errorf("%s attempts=%d, want 2", id, attempts[id])
				}
			}
		})
	}
}

// This measures the in-process HTTP client AND mock allocations. The separate
// executable load campaign, not this microbenchmark, measures application CPU.
func BenchmarkHTTPEventTransportWriteBuffer(b *testing.B) {
	for _, size := range []int{512, 16384, 65536} {
		for _, capacity := range []int{4096, 32768} {
			b.Run(fmt.Sprintf("payload-%d/buffer-%d", size, capacity), func(b *testing.B) {
				peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, err := io.Copy(io.Discard, r.Body); err != nil {
						b.Error(err)
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				defer peer.Close()
				sender, err := NewHTTPEventsSender(HTTPEventsSenderCfg{Name: "buffer-benchmark", Addr: peer.URL, Format: "ndjson", Tags: []string{"test"}, MaxAttempts: 1})
				if err != nil {
					b.Fatal(err)
				}
				defer sender.httpClient.CloseIdleConnections()
				sender.httpClient.Transport.(*http.Transport).WriteBufferSize = capacity
				batch := []*library.FluentMsg{{Message: map[string]interface{}{"text": strings.Repeat("x", size), "id": "benchmark"}}}
				// Establish the connection outside timed/allocation measurements.
				if err := sender.Send(context.Background(), batch); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := sender.Send(context.Background(), batch); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
