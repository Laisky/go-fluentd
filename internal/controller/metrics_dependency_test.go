package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Use a fresh process instead of replacing Prometheus's global registry while
// other controller tests may have background workers. The child runs the real
// RunServer registration path; it also retains the race-enabled test binary.
func TestRegressionMetricsDependencyCompatibility(t *testing.T) {
	const child = "GOFLUENTD_METRICS_COMPAT_CHILD"
	if os.Getenv(child) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegressionMetricsDependencyCompatibility$", "-test.v")
		cmd.Env = append(os.Environ(), child+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated metrics contract failed: %v\n%s", err, output)
		}
		return
	}

	// Cancellation closes any ephemeral listener after route registration.
	// Requests below execute against the actual production router in memory.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	RunServer(ctx, "127.0.0.1:0")

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://example.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := request(method, "/health", "")
		if response.Code != http.StatusOK || response.Body.String() != "hello, world" {
			t.Fatalf("health route changed: method=%s status=%d body=%q", method, response.Code, response.Body.String())
		}
	}
	profile := request(http.MethodGet, "/pprof/goroutine?debug=1", "")
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), "goroutine profile:") {
		t.Fatalf("pprof route changed: status=%d body=%q", profile.Code, profile.Body.String())
	}

	// Like routes added after RunServer in existing integrations, this route
	// should be instrumented, with its request body and response unchanged.
	server.POST("/metrics-compat/:id", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Header("X-Metrics-Contract", "preserved")
		c.Data(http.StatusAccepted, "text/plain", body)
	})
	const payload = "hello\x00世界 / café"
	response := request(http.MethodPost, "/metrics-compat/42", payload)
	if response.Code != http.StatusAccepted || response.Body.String() != payload || response.Header().Get("X-Metrics-Contract") != "preserved" {
		t.Fatalf("metrics middleware changed request/response: status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}

	// Scraping twice must not count /metrics itself. The health and pprof routes
	// were historically registered before the metrics middleware; preserve that
	// ordering rather than silently expanding metric coverage during this repair.
	for scrape := 0; scrape < 2; scrape++ {
		metrics := request(http.MethodGet, "/metrics", "")
		if metrics.Code != http.StatusOK || !strings.HasPrefix(metrics.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("metrics scrape changed: status=%d type=%q", metrics.Code, metrics.Header().Get("Content-Type"))
		}
		body := metrics.Body.String()
		for _, name := range []string{"gin_requests_total", "gin_request_duration_seconds", "gin_request_size_bytes", "gin_response_size_bytes", "go_goroutines", "process_cpu_seconds_total"} {
			if !strings.Contains(body, "# TYPE "+name+" ") {
				t.Errorf("scrape missing metric family %s", name)
			}
		}
		counter := regexp.MustCompile(`(?m)^gin_requests_total\{([^}]*)\} ([^\s]+)$`).FindAllStringSubmatch(body, -1)
		if len(counter) != 1 {
			t.Fatalf("expected one instrumented request series, got %v", counter)
		}
		labels := make(map[string]string)
		for _, match := range regexp.MustCompile(`(\w+)="([^"]*)"`).FindAllStringSubmatch(counter[0][1], -1) {
			labels[match[1]] = match[2]
		}
		if len(labels) != 5 || labels["code"] != "202" || labels["method"] != "POST" || labels["host"] != "example.test" || labels["url"] != "/metrics-compat/42" || labels["handler"] == "" {
			t.Fatalf("metric label contract changed: %v", labels)
		}
		value, err := strconv.ParseFloat(counter[0][2], 64)
		if err != nil || value != 1 {
			t.Fatalf("request counted incorrectly: value=%v error=%v", value, err)
		}
		for _, name := range []string{"gin_request_size_bytes_count", "gin_response_size_bytes_count"} {
			if !regexp.MustCompile(`(?m)^` + name + ` 1$`).MatchString(body) {
				t.Errorf("summary sample count changed for %s", name)
			}
		}
	}
}
