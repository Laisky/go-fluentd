package recvs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gofluentd/library"

	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
)

const eventFixture = `{"specversion":"1.0","id":"producer-42","source":"/orders","type":"order.created","data":{"n":18446744073709551615,"text":"世界","ok":true}}`

func eventsReceiver(t *testing.T, format string, edit func(*HTTPEventsRecvCfg)) (*HTTPEventsRecv, *gin.Engine, chan *library.FluentMsg) {
	t.Helper()
	e := gin.New()
	cfg := HTTPEventsRecvCfg{HTTPSrv: e, Name: "events", Path: "/events", Tag: "events.test", Format: format, AckTimeout: time.Second}
	if edit != nil {
		edit(&cfg)
	}
	r, err := NewHTTPEventsRecv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.SetMsgPool(recvPool()) // deliberately supplies dirty pooled messages
	r.SetCounter(utils.NewCounter())
	out := make(chan *library.FluentMsg, 8)
	r.SetSyncOutChan(out)
	return r, e, out
}

func eventsRequest(body, ct string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/events", strings.NewReader(body))
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	return r
}

func eventsStart(e *gin.Engine, req *http.Request) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		done <- w
	}()
	return done
}

func eventsNotComplete(t *testing.T, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	synctest.Wait()
	select {
	case w := <-done:
		t.Fatalf("response before all durable receipts: %d", w.Code)
	default:
	}
}

func TestComponentHTTPEventsWaitsForEveryReceipt(t *testing.T) {
	cases := []struct{ name, format, ct, body string }{
		{"ndjson", "ndjson", "application/x-ndjson", "{\"n\":9223372036854775807,\"text\":\"世界\"}\n{\"n\":-9223372036854775808}\n"},
		{"cloudevents", "cloudevents", "application/cloudevents-batch+json", "[" + eventFixture + "," + strings.ReplaceAll(eventFixture, "producer-42", "producer-43") + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, e, out := eventsReceiver(t, tc.format, nil)
				async := make(chan *library.FluentMsg, 2)
				r.SetAsyncOutChan(async)
				done := eventsStart(e, eventsRequest(tc.body, tc.ct))
				a, b := <-out, <-out
				for _, m := range []*library.FluentMsg{a, b} {
					if m.Tag != "events.test" || m.JournalTag != "" || len(m.ExtIds) != 0 || m.Message["stale"] != nil || m.DurableAck == nil {
						t.Fatalf("corrupted envelope or pooled state: %+v", m)
					}
				}
				if a.ID == b.ID || len(async) != 0 {
					t.Fatal("WAL identities collided or durable path bypassed")
				}
				if tc.format == "cloudevents" {
					if a.Message["id"] != "producer-42" || b.Message["id"] != "producer-43" ||
						!reflect.DeepEqual(a.Message["data"], map[string]interface{}{"n": uint64(math.MaxUint64), "text": "世界", "ok": true}) {
						t.Fatalf("CloudEvent context/payload changed: %+v", a.Message)
					}
				} else if a.Message["n"] != int64(math.MaxInt64) || b.Message["n"] != int64(math.MinInt64) || a.Message["text"] != "世界" {
					t.Fatal("NDJSON integer precision or Unicode changed")
				}
				eventsNotComplete(t, done)
				b.CompleteAcceptance(nil) // out-of-order completion is not enough
				eventsNotComplete(t, done)
				a.CompleteAcceptance(nil)
				*a = library.FluentMsg{ID: -999} // pipeline may immediately reuse it
				w := <-done
				if w.Code != http.StatusNoContent || w.Body.Len() != 0 || len(out) != 0 {
					t.Fatalf("success response: %d %q", w.Code, w.Body.String())
				}
			})
		})
	}
}

func TestComponentHTTPEventsSingleAndBinary(t *testing.T) {
	for _, binary := range []bool{false, true} {
		t.Run(fmt.Sprint(binary), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, e, out := eventsReceiver(t, "cloudevents", func(c *HTTPEventsRecvCfg) { c.BearerToken = "test-token" })
				r.Run(context.Background())
				if r.GetName() != "events" {
					t.Fatal(r.GetName())
				}
				req := eventsRequest(eventFixture, "application/cloudevents+json")
				if binary {
					req = eventsRequest("\x00\xff", "application/octet-stream")
					for k, v := range map[string]string{"Ce-Specversion": "1.0", "Ce-Id": "producer-42", "Ce-Source": "/orders", "Ce-Type": "order.created", "Ce-Subject": "caf%C3%A9"} {
						req.Header.Set(k, v)
					}
				}
				req.Header.Set("Authorization", "bEaReR test-token")
				done := eventsStart(e, req)
				msg := <-out
				if msg.Message["id"] != "producer-42" || binary && (msg.Message["data_base64"] != "AP8=" || msg.Message["subject"] != "café") {
					t.Fatalf("wire envelope changed: %+v", msg.Message)
				}
				msg.CompleteAcceptance(nil)
				if w := <-done; w.Code != 204 {
					t.Fatal(w.Code)
				}
			})
		})
	}
}

func TestComponentHTTPEventsRejectsWholeRequest(t *testing.T) {
	cases := []struct {
		name, format, body, ct string
		status                 int
		edit                   func(*http.Request)
	}{
		{"bad suffix", "ndjson", "{\"n\":1}\nnot-json\n", "application/x-ndjson", 400, nil},
		{"duplicate key", "ndjson", "{\"n\":1,\"n\":2}\n", "application/x-ndjson", 400, nil},
		{"record limit", "ndjson", "{}\n{}\n{}\n", "application/x-ndjson", 400, nil},
		{"unknown length", "ndjson", strings.Repeat("x", 513), "application/x-ndjson", 413, func(r *http.Request) { r.ContentLength = -1 }},
		{"declared length", "ndjson", "{}\n", "application/x-ndjson", 413, func(r *http.Request) { r.ContentLength = 513 }},
		{"wrong type", "ndjson", "{}\n", "application/json", 415, nil},
		{"missing type", "ndjson", "{}\n", "", 415, nil},
		{"wrong charset", "ndjson", "{}\n", "application/x-ndjson;charset=latin1", 415, nil},
		{"duplicate type", "ndjson", "{}\n", "application/x-ndjson", 415, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{"compression", "ndjson", "{}\n", "application/x-ndjson", 415, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"duplicate encoding", "ndjson", "{}\n", "application/x-ndjson", 415, func(r *http.Request) { r.Header["Content-Encoding"] = []string{"identity", "identity"} }},
		{"bad event suffix", "cloudevents", "[" + eventFixture + ",{}]", "application/cloudevents-batch+json", 400, nil},
		{"no auth", "ndjson", "{}\n", "application/x-ndjson", 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"wrong auth", "ndjson", "{}\n", "application/x-ndjson", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }},
		{"duplicate auth", "ndjson", "{}\n", "application/x-ndjson", 401, func(r *http.Request) { r.Header.Add("Authorization", "Bearer secret") }},
		{"read failure", "ndjson", "{}\n", "application/x-ndjson", 400, func(r *http.Request) { r.Body = io.NopCloser(eventsBrokenReader{}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, e, out := eventsReceiver(t, tc.format, func(c *HTTPEventsRecvCfg) { c.MaxBodySize, c.MaxRecords, c.BearerToken = 512, 2, "secret" })
			counter := utils.NewCounter()
			r.SetCounter(counter)
			req := eventsRequest(tc.body, tc.ct)
			req.Header.Set("Authorization", "Bearer secret")
			if tc.edit != nil {
				tc.edit(req)
			}
			w := httptest.NewRecorder()
			e.ServeHTTP(w, req)
			if w.Code != tc.status || len(out) != 0 || counter.Count() != 1 {
				t.Fatalf("status=%d want=%d queued=%d; invalid request must not allocate IDs", w.Code, tc.status, len(out))
			}
		})
	}
}

type eventsBrokenReader struct{}

func (eventsBrokenReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestComponentHTTPEventsReceiptFailuresAndCancellation(t *testing.T) {
	for _, cause := range []string{"storage", "closed receipt", "ack timeout", "cancel after publish", "cancel before publish", "queue timeout", "unwired"} {
		t.Run(cause, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, e, out := eventsReceiver(t, "ndjson", nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cause == "queue timeout" {
					r.SetSyncOutChan(make(chan *library.FluentMsg))
				}
				if cause == "cancel before publish" {
					cancel()
				}
				if cause == "unwired" {
					r.SetSyncOutChan(nil)
				}
				done := eventsStart(e, eventsRequest("{}\n", "application/x-ndjson").WithContext(ctx))
				switch cause {
				case "storage", "closed receipt", "ack timeout", "cancel after publish":
					msg := <-out
					eventsNotComplete(t, done)
					switch cause {
					case "storage":
						msg.CompleteAcceptance(errors.New("disk full"))
					case "closed receipt":
						close(msg.DurableAck)
					case "cancel after publish":
						cancel()
					case "ack timeout":
						time.Sleep(2 * time.Second)
					}
					if w := <-done; w.Code != 503 {
						t.Fatal(w.Code)
					}
					if cause == "cancel after publish" || cause == "ack timeout" {
						// A late receipt cannot block the journal after the HTTP
						// handler has left, nor has it recycled pipeline-owned msg.
						if msg.Message == nil || msg.Tag != "events.test" {
							t.Fatal("published message was reclaimed by receiver")
						}
						msg.CompleteAcceptance(nil)
					}
					return
				}
				if w := <-done; w.Code != 503 || len(out) != 0 {
					t.Fatalf("status=%d queued=%d", w.Code, len(out))
				}
			})
		})
	}
}

func TestComponentHTTPEventsConfigAndEmptyBatch(t *testing.T) {
	for _, edit := range []func(*HTTPEventsRecvCfg){
		func(c *HTTPEventsRecvCfg) { c.HTTPSrv = nil }, func(c *HTTPEventsRecvCfg) { c.Name = "" },
		func(c *HTTPEventsRecvCfg) { c.Tag = "" }, func(c *HTTPEventsRecvCfg) { c.Path = "relative" },
		func(c *HTTPEventsRecvCfg) { c.Path = "/:dynamic" }, func(c *HTTPEventsRecvCfg) { c.Format = "otlp" },
		func(c *HTTPEventsRecvCfg) { c.MaxBodySize = -1 }, func(c *HTTPEventsRecvCfg) { c.MaxRecords = -1 },
		func(c *HTTPEventsRecvCfg) { c.AckTimeout = -1 }, func(c *HTTPEventsRecvCfg) { c.BearerToken = "a\nb" },
	} {
		e := gin.New()
		cfg := HTTPEventsRecvCfg{HTTPSrv: e, Name: "a", Tag: "a", Path: "/events", Format: "ndjson"}
		edit(&cfg)
		if _, err := NewHTTPEventsRecv(cfg); err == nil || len(e.Routes()) != 0 {
			t.Fatal("invalid configuration registered a route")
		}
	}
	r, e, out := eventsReceiver(t, "cloudevents", func(c *HTTPEventsRecvCfg) { c.AckTimeout = 0 })
	if r.cfg.MaxBodySize != 4<<20 || r.cfg.MaxRecords != 1024 || r.cfg.AckTimeout != 30*time.Second {
		t.Fatal("defaults changed")
	}
	if _, err := NewHTTPEventsRecv(r.cfg); err == nil {
		t.Fatal("duplicate route accepted")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, eventsRequest("[]", "application/cloudevents-batch+json"))
	if w.Code != 204 || len(out) != 0 {
		t.Fatal("empty batch must not invent messages")
	}
}
