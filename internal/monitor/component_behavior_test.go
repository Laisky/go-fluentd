package monitor

import (
	stdjson "encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestBehaviorMonitorSnapshot(t *testing.T) {
	srv := gin.New()
	BindHTTP(srv)
	AddMetric("behavior", func() map[string]interface{} { return map[string]interface{}{"count": 7} })
	t.Cleanup(func() { AddMetric("behavior", func() map[string]interface{} { return nil }) })
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
		var result map[string]interface{}
		if w.Code != http.StatusOK || stdjson.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid snapshot: %d %s", w.Code, w.Body)
		}
		if result["ts"] == nil || result["behavior"].(map[string]interface{})["count"] != float64(7) {
			t.Fatal(result)
		}
	}
}
func TestBehaviorMonitorEncodingFailureIsNotSuccess(t *testing.T) {
	srv := gin.New()
	BindHTTP(srv)
	AddMetric("bad", func() map[string]interface{} { return map[string]interface{}{"invalid": make(chan int)} })
	defer AddMetric("bad", func() map[string]interface{} { return nil })
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("unencodable metrics returned HTTP %d instead of 500", w.Code)
	}
}
func TestBehaviorMonitorConcurrentRegistrationAndRequests(t *testing.T) {
	srv := gin.New()
	BindHTTP(srv)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				AddMetric(fmt.Sprintf("parallel-%d", i), func() map[string]interface{} { return map[string]interface{}{"value": i} })
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
				if w.Code != 200 || !stdjson.Valid(w.Body.Bytes()) {
					t.Errorf("inconsistent response: %d %q", w.Code, w.Body.Bytes())
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
