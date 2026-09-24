package otlphttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gofluentd/internal/otlphttp"
	"gofluentd/library/otlpwire"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func receiver(t testing.TB, c otlphttp.ReceiverConfig, a otlphttp.Admission) *otlphttp.Receiver {
	t.Helper()
	h, err := otlphttp.NewReceiver(c, a)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func request(path, ct string, b []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer test-token")
	return r
}
func TestOTLPHTTPReceiverAdmission(t *testing.T) {
	for _, s := range signals {
		for _, ct := range encodings {
			for _, gz := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/gzip=%v", s, ct, gz), func(t *testing.T) {
					env := fixture(t, s, ct)
					entered, release := make(chan *otlpwire.Request, 1), make(chan struct{})
					h := receiver(t, otlphttp.ReceiverConfig{BearerToken: "test-token"}, func(ctx context.Context, r *otlpwire.Request) error {
						entered <- r
						select {
						case <-release:
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					})
					srv := httptest.NewServer(h)
					defer srv.Close()
					defer close(release)
					body := env.Payload
					if gz {
						body = compressed(t, body)
					}
					r, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/"+string(s), bytes.NewReader(body))
					r.Header.Set("Content-Type", ct)
					r.Header.Set("Authorization", "Bearer test-token")
					if gz {
						r.Header.Set("Content-Encoding", "gzip")
					}
					r.ContentLength = -1
					type result struct {
						resp *http.Response
						err  error
					}
					done := make(chan result, 1)
					go func() { resp, err := srv.Client().Do(r); done <- result{resp, err} }()
					var got *otlpwire.Request
					select {
					case got = <-entered:
					case res := <-done:
						t.Fatalf("response without admission: %v %v", res.resp, res.err)
					case <-time.After(3 * time.Second):
						t.Fatal("admission not reached")
					}
					if got.Signal() != s || got.ContentType() != ct || int64(got.Items()) != env.Items || !bytes.Equal(got.Payload(), env.Payload) {
						t.Fatal("admitted telemetry changed")
					}
					select {
					case <-done:
						t.Fatal("success before durable admission completed")
					case <-time.After(10 * time.Millisecond):
					}
					release <- struct{}{}
					var res result
					select {
					case res = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("no response after admission")
					}
					if res.err != nil {
						t.Fatal(res.err)
					}
					defer res.resp.Body.Close()
					b, _ := io.ReadAll(res.resp.Body)
					expected := []byte{}
					if ct == otlpwire.JSON {
						expected = []byte(`{}`)
					}
					if res.resp.StatusCode != 200 || res.resp.Header.Get("Content-Type") != ct || !bytes.Equal(b, expected) {
						t.Fatalf("wrong OTLP success: %d %q", res.resp.StatusCode, b)
					}
				})
			}
		}
	}
}
func TestOTLPHTTPReceiverRejection(t *testing.T) {
	for _, ct := range encodings {
		for _, name := range []string{"admission-error", "bad-body", "oversize", "bad-media", "bad-encoding", "bad-gzip", "auth", "duplicate-auth", "duplicate-type", "wrong-path", "wrong-method", "empty-envelope"} {
			t.Run(ct+"/"+name, func(t *testing.T) {
				var called atomic.Int32
				h := receiver(t, otlphttp.ReceiverConfig{BearerToken: "test-token", Limits: otlpwire.Limits{WireBytes: 128, DecodedBytes: 128, Items: 1}}, func(ctx context.Context, r *otlpwire.Request) error {
					called.Add(1)
					if name == "admission-error" {
						return errors.New("private storage diagnostic")
					}
					return nil
				})
				body := []byte{}
				if ct == otlpwire.JSON {
					body = []byte(`{}`)
				}
				r := request("/v1/logs", ct, body)
				status := 400
				wantCalls := int32(0)
				switch name {
				case "admission-error":
					status = 503
					wantCalls = 1
				case "bad-body":
					r.Body = io.NopCloser(strings.NewReader("bad"))
				case "oversize":
					r.ContentLength = -1
					r.Body = io.NopCloser(strings.NewReader(strings.Repeat(" ", 129)))
					status = 413
				case "bad-media":
					r.Header.Set("Content-Type", "text/plain")
					status = 415
				case "bad-encoding":
					r.Header.Set("Content-Encoding", "br")
					status = 415
				case "bad-gzip":
					r.Header.Set("Content-Encoding", "gzip")
				case "auth":
					r.Header.Set("Authorization", "Bearer wrong")
					status = 401
				case "duplicate-auth":
					r.Header.Add("Authorization", "Bearer test-token")
					status = 401
				case "duplicate-type":
					r.Header.Add("Content-Type", ct)
					status = 415
				case "wrong-path":
					r.URL.Path = "/v1/logs/"
					status = 404
				case "wrong-method":
					r.Method = "GET"
					status = 405
				case "empty-envelope":
					status = 200
					wantCalls = 1
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != status || called.Load() != wantCalls {
					t.Fatalf("admission rejection: got status=%d calls=%d want=%d/%d", w.Code, called.Load(), status, wantCalls)
				}
				if status != 200 {
					var p statuspb.Status
					var err error
					if w.Header().Get("Content-Type") == otlpwire.JSON {
						err = protojson.Unmarshal(w.Body.Bytes(), &p)
					} else {
						err = proto.Unmarshal(w.Body.Bytes(), &p)
					}
					if err != nil || p.Message == "" || strings.Contains(p.Message, "private") {
						t.Fatalf("invalid/leaky google.rpc.Status: %q %v", w.Body.Bytes(), err)
					}
				}
			})
		}
	}
}
func TestOTLPHTTPReceiverBackpressureAndCancel(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	h := receiver(t, otlphttp.ReceiverConfig{MaxConcurrent: 1, Timeout: time.Second, BodyReadTimeout: time.Second}, func(ctx context.Context, r *otlpwire.Request) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := request("/v1/logs", otlpwire.JSON, []byte(`{}`)).WithContext(ctx)
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(first, r); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("admission not entered")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request("/v1/logs", otlpwire.JSON, []byte(`{}`)))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatal("capacity did not backpressure")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not return")
	}
	if first.Code != 503 {
		t.Fatal("canceled admission acknowledged")
	}
	close(release)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("/v1/logs", otlpwire.JSON, []byte(`{}`)))
	if w.Code != 200 {
		t.Fatal("capacity not released after cancellation")
	}
}
func TestOTLPHTTPReceiverSlowBodyDeadline(t *testing.T) {
	var calls atomic.Int32
	h := receiver(t, otlphttp.ReceiverConfig{BodyReadTimeout: 30 * time.Millisecond}, func(context.Context, *otlpwire.Request) error { calls.Add(1); return nil })
	s := httptest.NewServer(h)
	defer s.Close()
	conn, err := net.Dial("tcp", s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "POST /v1/logs HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 10\r\nConnection: close\r\n\r\n{")
	b := make([]byte, 4096)
	n, err := conn.Read(b)
	if err != nil || n == 0 || calls.Load() != 0 || !bytes.Contains(b[:n], []byte("503 Service Unavailable")) {
		t.Fatalf("slow body deadline: %q %v calls=%d", b[:n], err, calls.Load())
	}
}
func TestOTLPHTTPReceiverConfig(t *testing.T) {
	a := func(context.Context, *otlpwire.Request) error { return nil }
	if _, err := otlphttp.NewReceiver(otlphttp.ReceiverConfig{}, nil); err == nil {
		t.Fatal("nil admission accepted")
	}
	for _, c := range []otlphttp.ReceiverConfig{{MaxConcurrent: -1}, {BearerToken: "bad\nvalue"}, {Timeout: -1}, {Limits: otlpwire.Limits{WireBytes: 1}}, {Timeout: time.Second, BodyReadTimeout: 2 * time.Second}} {
		if _, err := otlphttp.NewReceiver(c, a); err == nil {
			t.Fatal("invalid config accepted", c)
		}
	}
}
