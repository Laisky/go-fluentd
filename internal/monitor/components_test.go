package monitor

import (
	stdjson "encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestComponentMonitorJSONAndReplacement(t *testing.T) {
	name := t.Name()
	AddMetric(name, func() map[string]interface{} { return map[string]interface{}{"n": 1} })
	AddMetric(name, func() map[string]interface{} { return map[string]interface{}{"n": 2} })
	e := gin.New()
	BindHTTP(e)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
	var result map[string]interface{}
	if err := stdjson.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result[name].(map[string]interface{})["n"] != float64(2) {
		t.Fatal("registry replacement failed")
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("monitor is JSON but content-type=%q", w.Header().Get("Content-Type"))
	}
}
func TestComponentMonitorSerializationFailure(t *testing.T) {
	name := t.Name()
	AddMetric(name, func() map[string]interface{} { return map[string]interface{}{"bad": make(chan int)} })
	defer AddMetric(name, func() map[string]interface{} { return nil })
	e := gin.New()
	BindHTTP(e)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
	if w.Code != 500 {
		t.Fatalf("serialization failure reported HTTP %d", w.Code)
	}
}
func TestComponentMonitorConcurrentRegistrationAndReads(t *testing.T) {
	e := gin.New()
	BindHTTP(e)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				AddMetric(fmt.Sprintf("concurrent-%d", n), func() map[string]interface{} { return map[string]interface{}{"ok": true} })
				w := httptest.NewRecorder()
				e.ServeHTTP(w, httptest.NewRequest("GET", "/monitor", nil))
				if !stdjson.Valid(w.Body.Bytes()) {
					t.Errorf("invalid concurrent snapshot: %q", w.Body.String())
					return
				}
			}
		}(n)
	}
	wg.Wait()
}
