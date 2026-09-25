//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/viper"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"gofluentd/internal/controller"
)

func serviceConfig(t *testing.T, endpoint string) controller.OTLPServiceConfig {
	t.Helper()
	t.Setenv("TEST_OTLP_INGRESS_TOKEN", "private-test-ingress")
	return controller.OTLPServiceConfig{Enabled: true, ListenAddress: "127.0.0.1:0", StorageDirectory: t.TempDir(), BearerTokenEnv: "TEST_OTLP_INGRESS_TOKEN", ReplayInterval: 10 * time.Millisecond, Destinations: []controller.OTLPServiceDestination{{ID: "archive", LogsEndpoint: endpoint + "/v1/logs", MetricsEndpoint: endpoint + "/v1/metrics", TracesEndpoint: endpoint + "/v1/traces", MaxAttempts: 1}}}
}
func serviceStart(t *testing.T, c controller.OTLPServiceConfig) *controller.OTLPService {
	t.Helper()
	s, e := controller.StartOTLPService(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Stop(); e != nil {
			t.Errorf("stop: %v", e)
		}
	})
	return s
}
func servicePost(t *testing.T, client *http.Client, url, ct, encoding, token string, body []byte) (int, []byte) {
	t.Helper()
	r, e := http.NewRequest("POST", url, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", ct)
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	return resp.StatusCode, b
}
func TestOTLPServiceConfigurationContracts(t *testing.T) {
	for _, raw := range []interface{}{nil, map[string]interface{}{}, map[string]interface{}{"enabled": false}} {
		c, e := controller.ParseOTLPServiceConfig(raw)
		if c != nil || e != nil {
			t.Fatal(c, e)
		}
	}
	for name, raw := range map[string]interface{}{
		"enabled-string":   map[string]interface{}{"enabled": "true"},
		"misspelled":       map[string]interface{}{"enabled": true, "max_connection": 4},
		"unitless-time":    map[string]interface{}{"enabled": true, "idle_timeout": 100},
		"fractional-limit": map[string]interface{}{"enabled": true, "max_connections": 1.5},
		"nested-typo":      map[string]interface{}{"enabled": true, "destinations": []interface{}{map[string]interface{}{"log_endpoint": "http://localhost"}}},
		"wrong-root":       "enabled",
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := controller.ParseOTLPServiceConfig(raw); e == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	c, e := controller.ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "idle_timeout": "75ms", "max_connections": 17})
	if e != nil || c.IdleTimeout != 75*time.Millisecond || c.MaxConnections != 17 {
		t.Fatal(c, e)
	}
}
func TestOTLPServiceInvalidConfigurationHasNoStorageEffects(t *testing.T) {
	cases := map[string]func(*controller.OTLPServiceConfig){
		"disabled":              func(c *controller.OTLPServiceConfig) { c.Enabled = false },
		"missing-token":         func(c *controller.OTLPServiceConfig) { c.BearerTokenEnv = "" },
		"unset-token":           func(c *controller.OTLPServiceConfig) { c.BearerTokenEnv = "UNSET_TEST_OTLP_TOKEN" },
		"public-cleartext":      func(c *controller.OTLPServiceConfig) { c.ListenAddress = "0.0.0.0:0" },
		"hostname":              func(c *controller.OTLPServiceConfig) { c.ListenAddress = "localhost:0" },
		"bad-port":              func(c *controller.OTLPServiceConfig) { c.ListenAddress = "127.0.0.1:65536" },
		"tls-pair":              func(c *controller.OTLPServiceConfig) { c.TLSKeyFile = "missing.pem" },
		"mtls-without-tls":      func(c *controller.OTLPServiceConfig) { c.ClientCAFile = "missing.pem" },
		"no-destinations":       func(c *controller.OTLPServiceConfig) { c.Destinations = nil },
		"missing-signal":        func(c *controller.OTLPServiceConfig) { c.Destinations[0].TracesEndpoint = "" },
		"duplicate-destination": func(c *controller.OTLPServiceConfig) { c.Destinations = append(c.Destinations, c.Destinations[0]) },
		"credentials-in-url": func(c *controller.OTLPServiceConfig) {
			c.Destinations[0].LogsEndpoint = "https://user:secret@example.test/v1/logs"
		},
		"invalid-ca":             func(c *controller.OTLPServiceConfig) { c.Destinations[0].CAFile = "/missing/test-ca" },
		"negative-connections":   func(c *controller.OTLPServiceConfig) { c.MaxConnections = -1 },
		"negative-timeout":       func(c *controller.OTLPServiceConfig) { c.IdleTimeout = -time.Second },
		"negative-replay":        func(c *controller.OTLPServiceConfig) { c.ReplayInterval = -time.Second },
		"oversized-body-limit":   func(c *controller.OTLPServiceConfig) { c.MaxDecodedBytes = 65 << 20 },
		"invalid-response-limit": func(c *controller.OTLPServiceConfig) { c.MaxResponseBytes = -1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := serviceConfig(t, "http://127.0.0.1:1")
			change(&c)
			s, e := controller.StartOTLPService(context.Background(), c)
			if e == nil {
				s.Stop()
				t.Fatal("invalid service accepted")
			}
			if strings.Contains(e.Error(), "private-test-ingress") || strings.Contains(e.Error(), "user:secret") {
				t.Fatal("secret exposed in error")
			}
			files, e := os.ReadDir(c.StorageDirectory)
			if e != nil || len(files) != 0 {
				t.Fatal("invalid startup modified storage", files, e)
			}
		})
	}
}
func TestOTLPServiceStartupFailureReleasesResources(t *testing.T) {
	c := serviceConfig(t, "http://127.0.0.1:1")
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	c.ListenAddress = ln.Addr().String()
	if s, e := controller.StartOTLPService(context.Background(), c); e == nil {
		s.Stop()
		t.Fatal("occupied port accepted")
	}
	entries, _ := os.ReadDir(c.StorageDirectory)
	if len(entries) != 0 {
		t.Fatal("bind failure initialized WAL")
	}
	ln.Close()
	missing := filepath.Join(c.StorageDirectory, "missing")
	c.StorageDirectory = missing
	if s, e := controller.StartOTLPService(context.Background(), c); e == nil {
		s.Stop()
		t.Fatal("missing storage accepted")
	}
	ln, e = net.Listen("tcp", c.ListenAddress)
	if e != nil {
		t.Fatal("failed startup leaked listener", e)
	}
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, e := controller.StartOTLPService(ctx, c); e == nil {
		s.Stop()
		t.Fatal("canceled startup accepted")
	}
	if _, e := controller.StartOTLPService(nil, c); e == nil {
		t.Fatal("nil context accepted")
	}
}

type serviceCodec interface {
	UnmarshalJSON([]byte) error
	MarshalProto() ([]byte, error)
}

func serviceFixture(t *testing.T, signal, ct string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "..", "library", "otlpwire", "testdata", signal+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if ct == "application/json" {
		return append([]byte(`{"future":{"precision":"18446744073709551615"},`), bytes.TrimSpace(b)[1:]...)
	}
	var codec serviceCodec
	switch signal {
	case "logs":
		codec = plogotlp.NewExportRequest()
	case "metrics":
		codec = pmetricotlp.NewExportRequest()
	case "traces":
		codec = ptraceotlp.NewExportRequest()
	}
	if e = codec.UnmarshalJSON(b); e != nil {
		t.Fatal(e)
	}
	b, e = codec.MarshalProto()
	if e != nil {
		t.Fatal(e)
	}
	return append(b, 0xf8, 0x7f, 1) // Unknown future protobuf field retained verbatim.
}
func TestOTLPServiceThreeSignalsSurviveListenerAndStorage(t *testing.T) {
	for _, signal := range []string{"logs", "metrics", "traces"} {
		for _, ct := range []string{"application/json", "application/x-protobuf"} {
			for _, compressed := range []bool{false, true} {
				for _, walGzip := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/gzip=%v/wal=%v", signal, ct, compressed, walGzip), func(t *testing.T) {
						original := serviceFixture(t, signal, ct)
						received := make(chan []byte, 4)
						peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path != "/v1/"+signal || r.Header.Get("Content-Type") != ct {
								t.Error("signal or encoding changed")
							}
							var reader io.Reader = r.Body
							if r.Header.Get("Content-Encoding") == "gzip" {
								z, e := gzip.NewReader(r.Body)
								if e != nil {
									t.Error(e)
									w.WriteHeader(400)
									return
								}
								defer z.Close()
								reader = z
							}
							b, e := io.ReadAll(reader)
							if e != nil {
								t.Error(e)
							}
							received <- b
							w.Header().Set("Content-Type", ct)
							if ct == "application/json" {
								io.WriteString(w, "{}")
							} else {
								w.WriteHeader(200)
							}
						}))
						defer peer.Close()
						c := serviceConfig(t, peer.URL)
						c.JournalGzip = walGzip
						c.Destinations[0].Gzip = compressed
						s := serviceStart(t, c)
						body, encoding := original, ""
						if compressed {
							var b bytes.Buffer
							z := gzip.NewWriter(&b)
							z.Write(original)
							z.Close()
							body, encoding = b.Bytes(), "gzip"
						}
						client := &http.Client{Timeout: 5 * time.Second}
						code, resp := servicePost(t, client, "http://"+s.Addr().String()+"/v1/"+signal, ct, encoding, "private-test-ingress", body)
						if code != 200 || (ct == "application/json" && string(resp) != "{}") || (ct != "application/json" && len(resp) != 0) {
							t.Fatalf("not OTLP success: %d %s", code, resp)
						}
						select {
						case b := <-received:
							if !bytes.Equal(b, original) {
								t.Fatal("payload changed between ingress and destination")
							}
						case <-time.After(5 * time.Second):
							t.Fatal("accepted payload never delivered")
						}
						// Receiving bytes is not yet a saved acceptance receipt. Wait for
						// the public post-Sync observation before requiring no resend.
						deadline := time.Now().Add(3 * time.Second)
						for s.Counters().Accepted != 1 && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
						}
						if s.Counters().Accepted != 1 {
							t.Fatal("peer acceptance was not durably recorded")
						}
						if e := s.Stop(); e != nil {
							t.Fatal(e)
						}
						reopened := serviceStart(t, c)
						time.Sleep(30 * time.Millisecond)
						select {
						case <-received:
							t.Fatal("durably completed destination replayed after restart")
						default:
						}
						if e := reopened.Stop(); e != nil {
							t.Fatal(e)
						}
					})
				}
			}
		}
	}
}
func TestOTLPServiceHTTPRejectionsAndRouteIsolation(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		io.WriteString(w, "{}")
	}))
	defer peer.Close()
	c := serviceConfig(t, peer.URL)
	c.MaxWireBytes = 128
	c.MaxDecodedBytes = 128
	s := serviceStart(t, c)
	client := &http.Client{Timeout: 3 * time.Second}
	base := "http://" + s.Addr().String()
	for _, v := range []struct {
		path, token, ct, body string
		code                  int
	}{
		{"/v1/logs", "", "application/json", "{}", 401},
		{"/v1/logs", "wrong", "application/json", "{}", 401},
		{"/v1/logs", "private-test-ingress", "application/json", "{", 400},
		{"/v1/logs", "private-test-ingress", "text/plain", "{}", 415},
		{"/v1/logs", "private-test-ingress", "application/json", strings.Repeat(" ", 129), 413},
		{"/pprof", "private-test-ingress", "application/json", "{}", 404},
		{"/metrics", "private-test-ingress", "application/json", "{}", 404},
		{"/health", "private-test-ingress", "application/json", "{}", 404},
		{"/v1/logs/", "private-test-ingress", "application/json", "{}", 404},
	} {
		code, b := servicePost(t, client, base+v.path, v.ct, "", v.token, []byte(v.body))
		if code != v.code {
			t.Fatalf("%s got %d wanted %d: %s", v.path, code, v.code, b)
		}
	}
	code, _ := servicePost(t, client, base+"/v1/logs", "application/json", "", "private-test-ingress", []byte("{}"))
	if code != 200 {
		t.Fatal(code)
	}
}

func serviceCertificates(t *testing.T) (certFile, keyFile, caFile string, pool *x509.CertPool, client tls.Certificate) {
	t.Helper()
	dir := t.TempDir()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if e != nil {
		t.Fatal(e)
	}
	pool = x509.NewCertPool()
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	pool.AppendCertsFromPEM(caPEM)
	caFile = filepath.Join(dir, "ca.pem")
	os.WriteFile(caFile, caPEM, 0600)
	makeCert := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test peer"}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, e := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, caKey)
		if e != nil {
			t.Fatal(e)
		}
		key, e := x509.MarshalPKCS8PrivateKey(k)
		if e != nil {
			t.Fatal(e)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	}
	cert, key := makeCert(2, x509.ExtKeyUsageServerAuth)
	certFile, keyFile = filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	os.WriteFile(certFile, cert, 0600)
	os.WriteFile(keyFile, key, 0600)
	cert, key = makeCert(3, x509.ExtKeyUsageClientAuth)
	client, e = tls.X509KeyPair(cert, key)
	if e != nil {
		t.Fatal(e)
	}
	return
}
func TestOTLPServiceTLSAndClientAuthentication(t *testing.T) {
	cert, key, ca, pool, identity := serviceCertificates(t)
	for _, mutual := range []bool{false, true} {
		t.Run(fmt.Sprint(mutual), func(t *testing.T) {
			c := serviceConfig(t, "http://127.0.0.1:1")
			c.TLSCertFile, c.TLSKeyFile = cert, key
			if mutual {
				c.ClientCAFile = ca
			}
			s := serviceStart(t, c)
			url := "https://" + s.Addr().String() + "/v1/logs"
			untrusted := &http.Client{Timeout: time.Second}
			if r, e := untrusted.Post(url, "application/json", strings.NewReader("{}")); e == nil {
				r.Body.Close()
				t.Fatal("untrusted TLS accepted")
			}
			tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			tr := &http.Transport{TLSClientConfig: tlsCfg}
			defer tr.CloseIdleConnections()
			client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
			if mutual {
				if r, e := client.Post(url, "application/json", strings.NewReader("{}")); e == nil {
					r.Body.Close()
					t.Fatal("missing client certificate accepted")
				}
				tlsCfg = tlsCfg.Clone()
				tlsCfg.Certificates = []tls.Certificate{identity}
				tr = &http.Transport{TLSClientConfig: tlsCfg}
				defer tr.CloseIdleConnections()
				client.Transport = tr
			}
			code, _ := servicePost(t, client, url, "application/json", "", "wrong", []byte("{}"))
			if code != 401 {
				t.Fatal("TLS bypassed bearer authentication", code)
			}
			code, _ = servicePost(t, client, url, "application/json", "", "private-test-ingress", []byte("{}"))
			if code != 200 {
				t.Fatal(code)
			}
		})
	}
}
func TestOTLPServiceConnectionAndReadDeadlines(t *testing.T) {
	c := serviceConfig(t, "http://127.0.0.1:1")
	c.MaxConnections = 1
	c.ReadHeaderTimeout = 100 * time.Millisecond
	c.BodyReadTimeout = time.Second
	c.IdleTimeout = 50 * time.Millisecond
	s := serviceStart(t, c)
	conn, e := net.Dial("tcp", s.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	// Reading 100 Continue establishes that this connection occupies the handler.
	fmt.Fprintf(conn, "POST /v1/logs HTTP/1.1\r\nHost: test\r\nAuthorization: Bearer private-test-ingress\r\nContent-Type: application/json\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n")
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil || resp.StatusCode != 100 {
		t.Fatal("first connection not admitted", resp, e)
	}
	client := &http.Client{Timeout: 100 * time.Millisecond}
	if r, e := client.Post("http://"+s.Addr().String()+"/v1/logs", "application/json", strings.NewReader("{}")); e == nil {
		r.Body.Close()
		t.Fatal("connection limit bypassed")
	}
	conn.Close()
	code, _ := servicePost(t, &http.Client{Timeout: 3 * time.Second}, "http://"+s.Addr().String()+"/v1/logs", "application/json", "", "private-test-ingress", []byte("{}"))
	if code != 200 {
		t.Fatal("slot not released", code)
	}
	slow, e := net.Dial("tcp", s.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer slow.Close()
	slow.Write([]byte("POS"))
	slow.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 1000)
	_, e = slow.Read(b)
	if ne, ok := e.(net.Error); ok && ne.Timeout() {
		t.Fatal("slow headers did not time out at server")
	}
}
func TestOTLPServiceConcurrentStopReleasesPortAndStorage(t *testing.T) {
	c := serviceConfig(t, "http://127.0.0.1:1")
	s := serviceStart(t, c)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.Stop(); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	ln, e := net.Listen("tcp", s.Addr().String())
	if e != nil {
		t.Fatal("listener leaked", e)
	}
	ln.Close()
	x := serviceStart(t, c)
	if e = x.Stop(); e != nil {
		t.Fatal("storage ownership leaked", e)
	}
}
func TestOTLPServiceStorageFaultStopsListener(t *testing.T) {
	c := serviceConfig(t, "http://127.0.0.1:1")
	s, e := controller.StartOTLPService(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Stop()
	if e = os.Rename(filepath.Join(c.StorageDirectory, "wal"), filepath.Join(c.StorageDirectory, "wal-evidence")); e != nil {
		t.Fatal(e)
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("fatal storage error did not stop service")
	}
	if s.Wait() == nil {
		t.Fatal("fatal storage error reported as success")
	}
	if conn, e := net.DialTimeout("tcp", s.Addr().String(), time.Second); e == nil {
		conn.Close()
		t.Fatal("faulted listener still accepts connections")
	}
}
func TestOTLPServiceDocumentedConfiguration(t *testing.T) {
	v := viper.New()
	v.SetConfigFile("../../docs/settings/otlp.yml")
	if e := v.ReadInConfig(); e != nil {
		t.Fatal(e)
	}
	c, e := controller.ParseOTLPServiceConfig(v.Get("settings.otlp"))
	if e != nil || c == nil {
		t.Fatal(c, e)
	}
	// Only environment-specific resources change; exercise the actual template.
	t.Setenv(c.BearerTokenEnv, "test-doc-token")
	c.ListenAddress = "127.0.0.1:0"
	c.StorageDirectory = t.TempDir()
	s := serviceStart(t, *c)
	if e = s.Stop(); e != nil {
		t.Fatal(e)
	}
}

func TestOTLPServiceStorageNamespacesCannotOverlap(t *testing.T) {
	root := t.TempDir()
	otel := filepath.Join(root, "otel")
	if e := os.Mkdir(otel, 0700); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(root, "alias")
	if e := os.Symlink(otel, alias); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{otel, otel}, {otel, root}, {otel, filepath.Join(otel, "logs")}, {otel, filepath.Join(alias, "logs")}, {alias, otel}, {"", otel}, {otel, ""}} {
		if e := controller.CheckOTLPStorageSeparation(pair[0], pair[1]); e == nil {
			t.Fatal("overlapping replay roots accepted", pair)
		}
	}
	if e := controller.CheckOTLPStorageSeparation(otel, filepath.Join(root, "legacy", "not-created-yet")); e != nil {
		t.Fatal(e)
	}
}
