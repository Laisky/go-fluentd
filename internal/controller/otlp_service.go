package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mitchellh/mapstructure"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
	"golang.org/x/net/netutil"
)

// OTLPServiceConfig is an opt-in, isolated pipeline. Secrets are named by
// environment variable, never embedded in the persisted destination plan.
type OTLPServiceConfig struct {
	Enabled           bool                     `mapstructure:"enabled"`
	ListenAddress     string                   `mapstructure:"listen_addr"`
	StorageDirectory  string                   `mapstructure:"storage_dir"`
	BearerTokenEnv    string                   `mapstructure:"bearer_token_env"`
	TLSCertFile       string                   `mapstructure:"tls_cert_file"`
	TLSKeyFile        string                   `mapstructure:"tls_key_file"`
	ClientCAFile      string                   `mapstructure:"client_ca_file"`
	MaxConnections    int                      `mapstructure:"max_connections"`
	MaxHeaderBytes    int                      `mapstructure:"max_header_bytes"`
	ReadHeaderTimeout time.Duration            `mapstructure:"read_header_timeout"`
	IdleTimeout       time.Duration            `mapstructure:"idle_timeout"`
	ShutdownTimeout   time.Duration            `mapstructure:"shutdown_timeout"`
	RequestTimeout    time.Duration            `mapstructure:"request_timeout"`
	BodyReadTimeout   time.Duration            `mapstructure:"body_read_timeout"`
	MaxConcurrent     int                      `mapstructure:"max_concurrent"`
	MaxWireBytes      int64                    `mapstructure:"max_wire_bytes"`
	MaxDecodedBytes   int64                    `mapstructure:"max_decoded_bytes"`
	MaxItems          int                      `mapstructure:"max_items"`
	MaxResponseBytes  int64                    `mapstructure:"max_response_bytes"`
	MaxWALBytes       int64                    `mapstructure:"max_wal_bytes"`
	JournalGzip       bool                     `mapstructure:"journal_gzip"`
	ReplayBatch       int                      `mapstructure:"replay_batch"`
	ReplayInterval    time.Duration            `mapstructure:"replay_interval"`
	Destinations      []OTLPServiceDestination `mapstructure:"destinations"`
}

type OTLPServiceDestination struct {
	ID              string        `mapstructure:"id"`
	LogsEndpoint    string        `mapstructure:"logs_endpoint"`
	MetricsEndpoint string        `mapstructure:"metrics_endpoint"`
	TracesEndpoint  string        `mapstructure:"traces_endpoint"`
	BearerTokenEnv  string        `mapstructure:"bearer_token_env"`
	CAFile          string        `mapstructure:"ca_file"`
	Gzip            bool          `mapstructure:"gzip"`
	Timeout         time.Duration `mapstructure:"timeout"`
	MaxAttempts     int           `mapstructure:"max_attempts"`
	InitialBackoff  time.Duration `mapstructure:"initial_backoff"`
	MaxBackoff      time.Duration `mapstructure:"max_backoff"`
}

// ParseOTLPServiceConfig rejects misspelled fields and implicit string/bool
// coercion. Durations are strings such as "30s", not unlabeled integer units.
func ParseOTLPServiceConfig(raw interface{}) (*OTLPServiceConfig, error) {
	if raw == nil {
		return nil, nil
	}
	var c OTLPServiceConfig
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result: &c, TagName: "mapstructure", ErrorUnused: true,
		DecodeHook: func(from, to reflect.Type, value interface{}) (interface{}, error) {
			if to == reflect.TypeOf(time.Duration(0)) {
				text, ok := value.(string)
				if !ok {
					return nil, errors.New("duration requires a unit-bearing string")
				}
				return time.ParseDuration(text)
			}
			if (from.Kind() == reflect.Float32 || from.Kind() == reflect.Float64) && to.Kind() >= reflect.Int && to.Kind() <= reflect.Uint64 {
				// Viper's JSON decoder uses floats even for integer literals.
				// Refuse fractions, non-finite values and the region where
				// adjacent integers can have rounded to the same float.
				number := reflect.ValueOf(value).Float()
				safeBits := 53
				if from.Kind() == reflect.Float32 {
					safeBits = 24
				}
				if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || math.Abs(number) >= math.Ldexp(1, safeBits) {
					return nil, errors.New("integer configuration requires an exact safe integer")
				}
				converted := reflect.New(to).Elem()
				if to.Kind() <= reflect.Int64 {
					bound := math.Ldexp(1, to.Bits()-1)
					if number < -bound || number >= bound {
						return nil, errors.New("integer configuration overflows target type")
					}
					converted.SetInt(int64(number))
				} else {
					if number < 0 || number >= math.Ldexp(1, to.Bits()) {
						return nil, errors.New("integer configuration overflows target type")
					}
					converted.SetUint(uint64(number))
				}
				return converted.Interface(), nil
			}
			return value, nil
		},
	})
	if err != nil {
		return nil, err
	}
	if err = dec.Decode(raw); err != nil {
		return nil, fmt.Errorf("invalid settings.otlp: %w", err)
	}
	if !c.Enabled {
		return nil, nil
	}
	return &c, nil
}

func (c *OTLPServiceConfig) defaults() {
	if c.ListenAddress == "" {
		c.ListenAddress = "127.0.0.1:4318"
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 128
	}
	if c.MaxHeaderBytes == 0 {
		c.MaxHeaderBytes = 32 << 10
	}
	if c.ReadHeaderTimeout == 0 {
		c.ReadHeaderTimeout = 5 * time.Second
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = 30 * time.Second
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 30 * time.Second
	}
	if c.BodyReadTimeout == 0 {
		c.BodyReadTimeout = 10 * time.Second
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 16
	}
	if c.MaxWireBytes == 0 {
		c.MaxWireBytes = 4 << 20
	}
	if c.MaxDecodedBytes == 0 {
		c.MaxDecodedBytes = 4 << 20
	}
	if c.MaxItems == 0 {
		c.MaxItems = 10000
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 1 << 20
	}
	if c.MaxWALBytes == 0 {
		c.MaxWALBytes = 256 << 20
	}
	if c.ReplayBatch == 0 {
		c.ReplayBatch = 64
	}
	if c.ReplayInterval == 0 {
		c.ReplayInterval = time.Second
	}
}

func envToken(name string, required bool) (string, error) {
	if name == "" {
		if required {
			return "", errors.New("OTLP bearer_token_env is required")
		}
		return "", nil
	}
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("OTLP token environment variable %q is unset or empty", name)
	}
	// Match transport token policy without putting the value in an error.
	if len(v) > 4096 {
		return "", errors.New("invalid OTLP token")
	}
	for _, r := range v {
		if r < 33 || r > 126 {
			return "", errors.New("invalid OTLP token")
		}
	}
	return v, nil
}
func loadOTLPCA(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read OTLP CA file: %w", err)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, errors.New("OTLP CA file contains no certificates")
	}
	return p, nil
}

// OTLPService owns its listener, HTTP server, scheduler, WAL and exporters.
// Start validates and binds synchronously. Wait reports a serve/storage fault;
// Stop cancels requests and waits for storage ownership to be released. Sync
// syscalls cannot be interrupted and can exceed the HTTP shutdown budget.
type OTLPService struct {
	owner     *OTLPJournal
	server    *http.Server
	listener  net.Listener
	exporters []*otlphttp.Exporter
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	err       error
}

func (s *OTLPService) Addr() net.Addr         { return s.listener.Addr() }
func (s *OTLPService) Done() <-chan struct{}  { return s.done }
func (s *OTLPService) Wait() error            { <-s.done; s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *OTLPService) Stop() error            { s.cancel(); return s.Wait() }
func (s *OTLPService) Counters() OTLPCounters { return s.owner.Counters() }

func StartOTLPService(parent context.Context, c OTLPServiceConfig) (_ *OTLPService, err error) {
	if parent == nil {
		return nil, errors.New("nil OTLP service context")
	}
	if err = parent.Err(); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, errors.New("OTLP service is not enabled")
	}
	c.defaults()
	host, port, e := net.SplitHostPort(c.ListenAddress)
	if e != nil {
		return nil, errors.New("OTLP listen_addr requires a numeric IP and port")
	}
	ip := net.ParseIP(host)
	n, e := strconv.Atoi(port)
	if ip == nil || e != nil || n < 0 || n > 65535 {
		return nil, errors.New("OTLP listen_addr requires a numeric IP and port")
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return nil, errors.New("OTLP TLS requires both certificate and key")
	}
	if !ip.IsLoopback() && c.TLSCertFile == "" {
		return nil, errors.New("non-loopback OTLP listener requires TLS")
	}
	if c.ClientCAFile != "" && c.TLSCertFile == "" {
		return nil, errors.New("OTLP client CA requires TLS")
	}
	if c.StorageDirectory == "" || len(c.Destinations) < 1 || len(c.Destinations) > 64 {
		return nil, errors.New("OTLP requires storage_dir and 1 to 64 destinations")
	}
	if c.MaxConnections < 1 || c.MaxConnections > 65536 || c.MaxHeaderBytes < 1024 || c.MaxHeaderBytes > 1<<20 || c.ReadHeaderTimeout <= 0 || c.ReadHeaderTimeout > time.Minute || c.IdleTimeout <= 0 || c.IdleTimeout > time.Hour || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute {
		return nil, errors.New("invalid OTLP listener limits")
	}
	if c.MaxResponseBytes < 1 || c.MaxResponseBytes > 8<<20 || c.MaxWALBytes < 4096 || c.MaxWALBytes > 1<<50 || c.ReplayBatch < 1 || c.ReplayBatch > 1024 || c.ReplayInterval < 10*time.Millisecond || c.ReplayInterval > time.Hour {
		return nil, errors.New("invalid OTLP storage/replay limits")
	}
	token, err := envToken(c.BearerTokenEnv, true)
	if err != nil {
		return nil, err
	}
	var tc *tls.Config
	if c.TLSCertFile != "" {
		cert, e := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
		if e != nil {
			return nil, fmt.Errorf("load OTLP TLS identity: %w", e)
		}
		tc = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
		if c.ClientCAFile != "" {
			tc.ClientCAs, err = loadOTLPCA(c.ClientCAFile)
			if err != nil {
				return nil, err
			}
			tc.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}
	ctx, cancel := context.WithCancel(parent)
	s := &OTLPService{cancel: cancel, done: make(chan struct{})}
	defer func() {
		if err != nil {
			cancel()
			if s.listener != nil {
				_ = s.listener.Close()
			}
			if s.owner != nil {
				_ = s.owner.Close()
			}
			for _, x := range s.exporters {
				x.Close()
			}
		}
	}()
	limits := otlpwire.Limits{WireBytes: c.MaxWireBytes, DecodedBytes: c.MaxDecodedBytes, Items: c.MaxItems}
	receiver, err := otlphttp.NewReceiver(otlphttp.ReceiverConfig{Limits: limits, BearerToken: token, MaxConcurrent: c.MaxConcurrent, Timeout: c.RequestTimeout, BodyReadTimeout: c.BodyReadTimeout}, func(ctx context.Context, r *otlpwire.Request) error { return s.owner.Admit(ctx, r) })
	if err != nil {
		return nil, err
	}
	peers := make([]OTLPDestination, 0, len(c.Destinations))
	ids := map[string]bool{}
	for _, d := range c.Destinations {
		if d.ID == "" || ids[d.ID] || d.LogsEndpoint == "" || d.MetricsEndpoint == "" || d.TracesEndpoint == "" {
			return nil, errors.New("OTLP destinations require unique IDs and all three signal endpoints")
		}
		ids[d.ID] = true
		auth, e := envToken(d.BearerTokenEnv, false)
		if e != nil {
			return nil, e
		}
		var ca *x509.CertPool
		if d.CAFile != "" {
			ca, e = loadOTLPCA(d.CAFile)
			if e != nil {
				return nil, e
			}
		}
		x, e := otlphttp.NewExporter(otlphttp.ExporterConfig{Endpoints: map[otlpwire.Signal]string{otlpwire.Logs: d.LogsEndpoint, otlpwire.Metrics: d.MetricsEndpoint, otlpwire.Traces: d.TracesEndpoint}, BearerToken: auth, RootCAs: ca, Gzip: d.Gzip, RequestLimits: limits, ResponseBytes: c.MaxResponseBytes, Timeout: d.Timeout, MaxAttempts: d.MaxAttempts, InitialBackoff: d.InitialBackoff, MaxBackoff: d.MaxBackoff})
		if e != nil {
			return nil, fmt.Errorf("invalid OTLP destination configuration: %w", e)
		}
		s.exporters = append(s.exporters, x)
		peers = append(peers, OTLPDestination{ID: d.ID, Send: x.Send})
	}
	// Bind first: an occupied port must not create a new storage generation.
	ln, e := net.Listen("tcp", c.ListenAddress)
	if e != nil {
		return nil, fmt.Errorf("bind OTLP listener: %w", e)
	}
	s.listener = netutil.LimitListener(ln, c.MaxConnections)
	s.owner, err = OpenOTLPJournal(ctx, OTLPJournalConfig{Directory: c.StorageDirectory, Compress: c.JournalGzip, Limits: otlpstate.Limits{PayloadBytes: int(c.MaxDecodedBytes), ResponseBytes: int(c.MaxResponseBytes)}, MaxWALBytes: c.MaxWALBytes, ReplayBatch: c.ReplayBatch, ReplayInterval: c.ReplayInterval}, peers)
	if err != nil {
		return nil, err
	}
	s.server = &http.Server{Handler: receiver, ReadHeaderTimeout: c.ReadHeaderTimeout, ReadTimeout: c.BodyReadTimeout, WriteTimeout: c.RequestTimeout + time.Second, IdleTimeout: c.IdleTimeout, MaxHeaderBytes: c.MaxHeaderBytes, TLSConfig: tc, BaseContext: func(net.Listener) context.Context { return ctx }, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
	// HTTP/1.1 is deliberate: max_connections is not an HTTP/2 stream quota.
	go s.run(ctx, c.ShutdownTimeout)
	return s, nil
}

func (s *OTLPService) run(ctx context.Context, shutdown time.Duration) {
	serve, worker := make(chan error, 1), make(chan error, 1)
	go func() {
		if s.server.TLSConfig != nil {
			serve <- s.server.ServeTLS(s.listener, "", "")
		} else {
			serve <- s.server.Serve(s.listener)
		}
	}()
	go func() { worker <- s.owner.Run(ctx) }()
	var se, we error
	serveDone, workerDone := false, false
	select {
	case se = <-serve:
		serveDone = true
	case we = <-worker:
		workerDone = true
	case <-ctx.Done():
	}
	s.cancel()
	drain, cancel := context.WithTimeout(context.Background(), shutdown)
	de := s.server.Shutdown(drain)
	cancel()
	if de != nil {
		_ = s.server.Close()
	}
	if !serveDone {
		se = <-serve
	}
	if !workerDone {
		we = <-worker
	}
	ce := s.owner.Close()
	for _, x := range s.exporters {
		x.Close()
	}
	if errors.Is(se, http.ErrServerClosed) {
		se = nil
	}
	if errors.Is(we, context.Canceled) || errors.Is(we, context.DeadlineExceeded) || errors.Is(we, ErrOTLPJournalClosed) {
		we = nil
	}
	s.mu.Lock()
	s.err = errors.Join(se, we, de, ce)
	s.mu.Unlock()
	close(s.done)
}

// CheckOTLPStorageSeparation prevents either replay engine from discovering the
// other's files. Existing symlink ancestors are resolved; storage and config are
// trusted and must not be changed concurrently with startup.
func CheckOTLPStorageSeparation(otlpDir, legacyDir string) error {
	if otlpDir == "" || legacyDir == "" {
		return errors.New("OTLP requires explicit, separate OTLP and legacy journal directories")
	}
	resolve := func(path string) (string, error) {
		p, e := filepath.Abs(path)
		if e != nil {
			return "", e
		}
		var missing []string
		for {
			real, e := filepath.EvalSymlinks(p)
			if e == nil {
				for i := len(missing) - 1; i >= 0; i-- {
					real = filepath.Join(real, missing[i])
				}
				return real, nil
			}
			if !os.IsNotExist(e) {
				return "", e
			}
			parent := filepath.Dir(p)
			if parent == p {
				return "", e
			}
			missing = append(missing, filepath.Base(p))
			p = parent
		}
	}
	a, e := resolve(otlpDir)
	if e != nil {
		return e
	}
	b, e := resolve(legacyDir)
	if e != nil {
		return e
	}
	contains := func(parent, child string) bool {
		r, e := filepath.Rel(parent, child)
		return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
	}
	if contains(a, b) || contains(b, a) {
		return errors.New("OTLP and legacy journal directories must not overlap")
	}
	return nil
}
