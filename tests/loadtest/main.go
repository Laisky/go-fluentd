// Command loadtest measures the real executable through independent local HTTP
// peers. It deliberately imports no go-fluentd packages. Linux is required for
// process CPU/RSS observations. No server-side counter is a delivery oracle.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const token = "local-loadtest-only"

type options struct {
	Binary, Out, Protocol, Replay, Profile                             string
	Requests, Warmup, Concurrency, Payload, Destinations, Group, Batch int
	Rate                                                               float64
	Delay                                                              time.Duration
	Gzip, WireGzip                                                     bool
	Timeout                                                            time.Duration
}
type record struct {
	ID                    int    `json:"id"`
	Protocol              string `json:"protocol"`
	SHA                   string `json:"sha256"`
	Scheduled, Start, Ack int64
	Status                int
	Error                 string `json:"error,omitempty"`
	Dropped               bool
	Sink                  []int64
	Duplicates            int
}
type fixture struct {
	Body, Wire         []byte
	Path, CT, Protocol string
	Canonical          string
}
type resources struct {
	AtNS                  int64   `json:"at_ns"`
	CPU                   float64 `json:"cpu_seconds"`
	RSS, HWM              int64
	Threads               int
	ReadBytes, WriteBytes int64
}
type trial struct {
	opts                         options
	root, mgmt, ingest, sinkBase string
	start, epoch                 time.Time
	mu                           sync.Mutex
	records                      []record
	fixtures                     []fixture
	lookup                       map[string]int
	failures                     []string
	samples                      []resources
	completed                    atomic.Int64
}

func jsonBytes(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func canonical(b []byte) (string, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return "", err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return "", errors.New("trailing JSON")
	}
	return string(jsonBytes(v)), nil
}
func makeFixture(protocol string, id, payload int) fixture {
	text := fmt.Sprintf("item-%08d 世界 café ", id) + strings.Repeat("0123456789abcdef", (payload+15)/16)
	text = text[:len(fmt.Sprintf("item-%08d 世界 café ", id))+payload]
	f := fixture{Protocol: protocol, CT: "application/json"}
	switch protocol {
	case "ndjson", "cloudevents":
		doc := map[string]any{"bench_id": id, "text": text, "msgid": fmt.Sprint(id), "a.b": "preserve", "": true, "int64": json.Number("9223372036854775807")}
		f.Path = "/events/" + protocol
		if protocol == "cloudevents" {
			doc = map[string]any{"specversion": "1.0", "id": fmt.Sprint(id), "source": "/loadtest", "type": "loadtest.event", "datacontenttype": "application/json", "data": doc}
			f.CT = "application/cloudevents+json"
		} else {
			f.CT = "application/x-ndjson"
		}
		f.Body = jsonBytes(doc)
		f.Canonical = string(f.Body)
		if protocol == "ndjson" {
			f.Body = append(f.Body, '\n')
		}
	case "logs", "metrics", "traces":
		f.Path = "/v1/" + protocol
		var item map[string]any
		var resourceKey, scopeKey, itemKey string
		switch protocol {
		case "logs":
			resourceKey, scopeKey, itemKey = "resourceLogs", "scopeLogs", "logRecords"
			item = map[string]any{"timeUnixNano": "1780000000000000000", "body": map[string]any{"stringValue": text}}
		case "metrics":
			resourceKey, scopeKey, itemKey = "resourceMetrics", "scopeMetrics", "metrics"
			point := map[string]any{"timeUnixNano": "1780000000000000000", "asInt": fmt.Sprint(id)}
			item = map[string]any{"name": "loadtest", "description": text, "gauge": map[string]any{"dataPoints": []any{point}}}
		case "traces":
			resourceKey, scopeKey, itemKey = "resourceSpans", "scopeSpans", "spans"
			item = map[string]any{"traceId": fmt.Sprintf("%032x", id+1), "spanId": fmt.Sprintf("%016x", id+1), "name": text, "startTimeUnixNano": "1780000000000000000", "endTimeUnixNano": "1780000000000001000"}
		}
		scope := map[string]any{itemKey: []any{item}}
		resource := map[string]any{scopeKey: []any{scope}}
		f.Body = jsonBytes(map[string]any{resourceKey: []any{resource}})
	default:
		panic("unknown protocol")
	}
	return f
}
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	ys := append([]float64(nil), xs...)
	sort.Float64s(ys)
	return ys[int(math.Ceil(q*float64(len(ys))))-1]
}
func distribution(xs []float64) map[string]float64 {
	return map[string]float64{"p50": percentile(xs, .5), "p95": percentile(xs, .95), "p99": percentile(xs, .99), "max": percentile(xs, 1)}
}
func save(path string, v any) error {
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	e = enc.Encode(v)
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}
func freeAddr() (string, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", e
	}
	a := l.Addr().String()
	return a, l.Close()
}
func (t *trial) config() map[string]any {
	o := t.opts
	q := 1024
	recv, send := map[string]any{}, map[string]any{}
	for _, p := range []string{"ndjson", "cloudevents"} {
		format := p
		if p == "cloudevents" {
			format = "cloudevents"
		}
		recv[p] = map[string]any{"type": "http_events", "active_env": []string{"prod"}, "path": "/events/" + p, "tag": p + ".{env}", "format": format, "bearer_token": token, "ack_timeout_sec": 30, "max_body_byte": 4 << 20}
		for i := 0; i < o.Destinations; i++ {
			mode := ""
			batch := o.Batch
			if p == "cloudevents" {
				mode = "structured"
				batch = 1
			}
			send[fmt.Sprintf("%s-%d", p, i)] = map[string]any{"type": "http_events", "active_env": []string{"prod"}, "addr": fmt.Sprintf("%s/%d/events/%s", t.sinkBase, i, p), "tags": []string{p + ".{env}"}, "format": format, "mode": mode, "bearer_token": token, "msg_batch_size": batch, "forks": 4, "max_attempts": 1, "request_timeout_sec": 30}
		}
	}
	dest := []any{}
	for i := 0; i < o.Destinations; i++ {
		d := map[string]any{"id": fmt.Sprintf("destination-%d", i), "bearer_token_env": "LOADTEST_TOKEN", "gzip": o.WireGzip, "max_attempts": 1, "timeout": "30s"}
		for _, s := range []string{"logs", "metrics", "traces"} {
			d[s+"_endpoint"] = fmt.Sprintf("%s/%d/v1/%s", t.sinkBase, i, s)
		}
		dest = append(dest, d)
	}
	return map[string]any{"settings": map[string]any{
		"journal":          map[string]any{"buf_dir_path": filepath.Join(t.root, "wal"), "buf_file_bytes": 64 << 20, "is_compress": o.Gzip, "committed_id_sec": 3600, "journal_out_chan_len": q, "child_data_chan_len": q, "child_id_chan_len": q, "commit_id_chan_len": q, "gc_inteval_sec": 3600, "group_commit_max_messages": o.Group},
		"acceptor":         map[string]any{"async_out_chan_size": q, "sync_out_chan_size": q, "recvs": map[string]any{"plugins": recv}},
		"acceptor_filters": map[string]any{"fork": 4, "out_buf_len": q}, "tag_filters": map[string]any{"internal_chan_size": q}, "dispatcher": map[string]any{"nfork": 4, "out_chan_size": q}, "post_filters": map[string]any{"fork": 4, "out_chan_size": q, "plugins": map[string]any{}}, "producer": map[string]any{"forks": 4, "discard_chan_size": q, "sender_inchan_size": q, "plugins": send},
		"otlp": map[string]any{"max_concurrent": 128, "max_connections": 256, "enabled": true, "listen_addr": strings.TrimPrefix(t.ingest, "http://"), "storage_dir": filepath.Join(t.root, "otlp"), "bearer_token_env": "LOADTEST_TOKEN", "journal_gzip": o.Gzip, "replay_interval": o.Replay, "replay_batch": 64, "max_wal_bytes": 256 << 20, "destinations": dest},
	}}
}
func (t *trial) sink(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token {
		t.fail("sink method/auth")
		http.Error(w, "auth", 401)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 3 {
		t.fail("sink route")
		http.Error(w, "route", 400)
		return
	}
	dest, e := strconv.Atoi(parts[0])
	if e != nil || dest < 0 || dest >= t.opts.Destinations {
		t.fail("sink destination")
		http.Error(w, "route", 400)
		return
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if e != nil {
		t.fail(e.Error())
		http.Error(w, "read", 400)
		return
	}
	if r.Header.Get("Content-Encoding") == "gzip" {
		z, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.fail(err.Error())
			http.Error(w, "gzip", 400)
			return
		}
		raw, e = io.ReadAll(io.LimitReader(z, 8<<20))
		z.Close()
		if e != nil {
			t.fail(e.Error())
			http.Error(w, "gzip", 400)
			return
		}
	}
	var keys []string
	switch parts[1] + "/" + parts[2] {
	case "events/ndjson":
		if r.Header.Get("Content-Type") != "application/x-ndjson" {
			t.fail("NDJSON content type")
		}
		scan := bufio.NewScanner(bytes.NewReader(raw))
		scan.Buffer(make([]byte, 4096), 8<<20)
		for scan.Scan() {
			s, err := canonical(scan.Bytes())
			if err != nil {
				t.fail(err.Error())
				continue
			}
			keys = append(keys, "ndjson:"+s)
		}
		if err := scan.Err(); err != nil {
			t.fail(err.Error())
		}
	case "events/cloudevents":
		if r.Header.Get("Content-Type") != "application/cloudevents+json" {
			t.fail("CloudEvents content type")
		}
		s, err := canonical(raw)
		if err != nil {
			t.fail(err.Error())
		}
		keys = []string{"cloudevents:" + s}
	case "v1/logs", "v1/metrics", "v1/traces":
		if r.Header.Get("Content-Type") != "application/json" {
			t.fail("OTLP content type")
		}
		keys = []string{parts[2] + ":" + hash(raw)}
	default:
		t.fail("unknown sink route")
	}
	if t.opts.Delay > 0 {
		time.Sleep(t.opts.Delay)
	}
	now := time.Since(t.epoch).Nanoseconds()
	t.mu.Lock()
	for _, key := range keys {
		id, ok := t.lookup[key]
		if !ok {
			t.failures = append(t.failures, "changed or invented sink payload")
			continue
		}
		rec := &t.records[id]
		if rec.Sink[dest] != 0 {
			rec.Duplicates++
		} else {
			rec.Sink[dest] = now
			t.completed.Add(1)
		}
	}
	t.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if parts[1] == "events" {
		w.WriteHeader(204)
	} else {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("{}"))
	}
}
func (t *trial) fail(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.failures) < 100 {
		t.failures = append(t.failures, s)
	}
}
func procStats(pid int, hz float64) (resources, error) {
	var s resources
	s.AtNS = time.Now().UnixNano()
	b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if e != nil {
		return s, e
	}
	str := string(b)
	p := strings.LastIndex(str, ")")
	if p < 0 {
		return s, errors.New("bad stat")
	}
	fs := strings.Fields(str[p+1:])
	if len(fs) < 22 {
		return s, errors.New("short stat")
	}
	u, e := strconv.ParseFloat(fs[11], 64)
	if e != nil {
		return s, e
	}
	v, e := strconv.ParseFloat(fs[12], 64)
	if e != nil {
		return s, e
	}
	s.CPU = (u + v) / hz
	b, e = os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if e != nil {
		return s, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "VmRSS:":
			s.RSS = n * 1024
		case "VmHWM:":
			s.HWM = n * 1024
		case "Threads:":
			s.Threads = int(n)
		}
	}
	b, e = os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if e != nil {
		return s, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			if f[0] == "read_bytes:" {
				s.ReadBytes = n
			}
			if f[0] == "write_bytes:" {
				s.WriteBytes = n
			}
		}
	}
	return s, nil
}
func getMem(client *http.Client, url string) map[string]uint64 {
	result := map[string]uint64{}
	r, e := client.Get(url + "/pprof/heap?debug=1")
	if e != nil {
		return result
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if r.StatusCode != 200 {
		return result
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[0] == "#" && f[2] == "=" {
			n, e := strconv.ParseUint(f[3], 10, 64)
			if e == nil {
				result[f[1]] = n
			}
		}
	}
	return result
}
func (t *trial) send(client *http.Client, id int, scheduled int64) {
	f := t.fixtures[id]
	body := f.Body
	if f.Wire != nil {
		body = f.Wire
	}

	endpoint := t.ingest
	if strings.HasPrefix(f.Path, "/events/") {
		endpoint = t.mgmt
	}
	req, e := http.NewRequest("POST", endpoint+f.Path, bytes.NewReader(body))
	if e != nil {
		t.fail(e.Error())
		return
	}
	req.Header.Set("Content-Type", f.CT)
	req.Header.Set("Authorization", "Bearer "+token)
	if t.opts.WireGzip && !strings.HasPrefix(f.Path, "/events/") {
		req.Header.Set("Content-Encoding", "gzip")
	}
	start := time.Since(t.epoch).Nanoseconds()
	status := 0
	errmsg := ""
	res, e := client.Do(req)
	if e != nil {
		errmsg = e.Error()
	} else {
		_, e = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		status = res.StatusCode
		if e != nil {
			errmsg = e.Error()
		}
	}
	end := time.Since(t.epoch).Nanoseconds()
	t.mu.Lock()
	r := &t.records[id]
	r.Start = start
	r.Scheduled = scheduled
	if scheduled == 0 {
		r.Scheduled = start
	}
	r.Ack = end
	r.Status = status
	r.Error = errmsg
	t.mu.Unlock()
}
func (t *trial) load(client *http.Client, first, last int, rate float64) {
	if rate == 0 {
		var next atomic.Int64
		next.Store(int64(first))
		var wg sync.WaitGroup
		for n := 0; n < t.opts.Concurrency; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					id := int(next.Add(1) - 1)
					if id >= last {
						return
					}
					t.send(client, id, 0)
				}
			}()
		}
		wg.Wait()
		return
	}
	origin := time.Now()
	slots := make(chan struct{}, t.opts.Concurrency)
	var wg sync.WaitGroup
	for id := first; id < last; id++ {
		due := origin.Add(time.Duration(float64(id-first) * float64(time.Second) / rate))
		if d := time.Until(due); d > 0 {
			time.Sleep(d)
		}
		select {
		case slots <- struct{}{}:
			wg.Add(1)
			go func(id int, when int64) { defer wg.Done(); defer func() { <-slots }(); t.send(client, id, when) }(id, due.Sub(t.epoch).Nanoseconds())
		default:
			t.mu.Lock()
			t.records[id].Dropped = true
			t.records[id].Scheduled = due.Sub(t.epoch).Nanoseconds()
			t.mu.Unlock()
		}
	}
	wg.Wait()
}
func (t *trial) waitDelivered(n int, timeout time.Duration) error {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if t.completed.Load() >= int64(n*t.opts.Destinations) {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("drain deadline: have %d/%d destination deliveries", t.completed.Load(), n*t.opts.Destinations)
}
func run(o options) (err error) {
	var result map[string]any
	defer func() {
		if result != nil {
			result["passed"] = err == nil
			if err != nil {
				result["failure"] = err.Error()
			}
			err = errors.Join(err, save(filepath.Join(o.Out, "summary.json"), result))
			fmt.Println(string(jsonBytes(result)))
		}
	}()
	if runtime.GOOS != "linux" {
		return errors.New("Linux /proc required")
	}
	info, e := buildinfo.ReadFile(o.Binary)
	if e != nil {
		return fmt.Errorf("inspect application build: %w", e)
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			return errors.New("race binaries are for correctness, not capacity measurements")
		}
		if setting.Key == "-gcflags" && strings.Contains(setting.Value, "-N") {
			return errors.New("unoptimized binary refused")
		}
	}
	root, e := filepath.Abs(o.Out)
	if e != nil {
		return e
	}
	if e = os.Mkdir(root, 0700); e != nil {
		return fmt.Errorf("new output directory required: %w", e)
	}
	for _, s := range []string{"wal", "otlp"} {
		if e = os.Mkdir(filepath.Join(root, s), 0700); e != nil {
			return e
		}
	}
	t := &trial{opts: o, root: root, epoch: time.Now(), lookup: map[string]int{}}
	for id := 0; id < o.Requests+o.Warmup; id++ {
		p := o.Protocol
		if p == "mixed" {
			p = []string{"ndjson", "cloudevents", "logs", "metrics", "traces"}[id%5]
		}
		f := makeFixture(p, id, o.Payload)
		if o.WireGzip && strings.HasPrefix(f.Path, "/v1/") {
			var b bytes.Buffer
			z := gzip.NewWriter(&b)
			if _, e := z.Write(f.Body); e != nil {
				return e
			}
			if e := z.Close(); e != nil {
				return e
			}
			f.Wire = b.Bytes()
		}

		key := p + ":" + hash(f.Body)
		if f.Canonical != "" {
			key = p + ":" + f.Canonical
		}
		if _, exists := t.lookup[key]; exists {
			return errors.New("fixture collision")
		}
		t.lookup[key] = id
		t.fixtures = append(t.fixtures, f)
		t.records = append(t.records, record{ID: id, Protocol: p, SHA: hash(f.Body), Sink: make([]int64, o.Destinations)})
	}
	if e = save(filepath.Join(root, "options.json"), o); e != nil {
		return e
	}
	defer func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		err = errors.Join(err, save(filepath.Join(root, "requests.json"), t.records), save(filepath.Join(root, "resources.json"), t.samples), save(filepath.Join(root, "errors.json"), t.failures))
	}()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	server := &http.Server{Handler: http.HandlerFunc(t.sink), ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(l)
	defer server.Close()
	t.sinkBase = "http://" + l.Addr().String()
	a, e := freeAddr()
	if e != nil {
		return e
	}
	b, e := freeAddr()
	if e != nil {
		return e
	}
	if a == b {
		return errors.New("application ports collided")
	}
	t.mgmt = "http://" + a
	t.ingest = "http://" + b
	if e = save(filepath.Join(root, "config.json"), t.config()); e != nil {
		return e
	}
	log, e := os.Create(filepath.Join(root, "application.log"))
	if e != nil {
		return e
	}
	defer log.Close()
	cmd := exec.Command(o.Binary, "--config", filepath.Join(root, "config.json"), "--env", "prod", "--addr", a, "--log-level", "error")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = append(os.Environ(), "LOADTEST_TOKEN="+token)
	if e = cmd.Start(); e != nil {
		return e
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		graceful := true
		select {
		case e := <-exited:
			err = errors.Join(err, e)
		case <-time.After(10 * time.Second):
			graceful = false
			_ = cmd.Process.Kill()
			<-exited
			err = errors.Join(err, errors.New("application did not shut down"))
		}
		err = errors.Join(err, save(filepath.Join(root, "process.json"), map[string]any{
			"pid": cmd.Process.Pid, "exit_code": cmd.ProcessState.ExitCode(), "graceful": graceful}))
	}()
	client := &http.Client{Timeout: o.Timeout, Transport: &http.Transport{Proxy: nil, MaxIdleConns: o.Concurrency*2 + 4, MaxIdleConnsPerHost: o.Concurrency + 2, DisableCompression: true}}
	defer client.CloseIdleConnections()
	ready := false
	for i := 0; i < 500; i++ {
		r, e := client.Get(t.mgmt + "/health")
		if e == nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		return errors.New("application not ready; see application.log")
	}
	hzData, e := exec.Command("getconf", "CLK_TCK").Output()
	if e != nil {
		return e
	}
	hz, e := strconv.ParseFloat(strings.TrimSpace(string(hzData)), 64)
	if e != nil || hz <= 0 {
		return errors.New("invalid CLK_TCK")
	}
	t.load(client, 0, o.Warmup, 0)
	t.mu.Lock()
	warmupOK := true
	for _, r := range t.records[:o.Warmup] {
		if r.Status != 200 && r.Status != 204 {
			warmupOK = false
		}
	}
	t.mu.Unlock()
	if !warmupOK {
		return errors.New("warmup rejected; see requests.json")
	}
	if e = t.waitDelivered(o.Warmup, o.Timeout); e != nil {
		return e
	}
	beforeMem := getMem(client, t.mgmt)
	before, e := procStats(cmd.Process.Pid, hz)
	if e != nil {
		return e
	}
	driverBefore, _ := procStats(os.Getpid(), hz)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s, e := procStats(cmd.Process.Pid, hz)
				if e != nil {
					t.fail(e.Error())
					continue
				}
				t.mu.Lock()
				t.samples = append(t.samples, s)
				t.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	t.start = time.Now()
	var profiling sync.WaitGroup
	if o.Profile != "" {
		profiling.Add(1)
		go func() {
			defer profiling.Done()
			r, e := client.Get(t.mgmt + "/pprof/profile?seconds=5")
			if e == nil {
				defer r.Body.Close()
				b, _ := io.ReadAll(r.Body)
				_ = os.WriteFile(filepath.Join(root, "cpu.pprof"), b, 0600)
			}
		}()
	}
	t.load(client, o.Warmup, len(t.records), o.Rate)
	t.mu.Lock()
	accepted := 0
	for i := o.Warmup; i < len(t.records); i++ {
		r := t.records[i]
		if r.Status == 200 || r.Status == 204 {
			accepted++
		}
	}
	t.mu.Unlock()
	drainErr := t.waitDelivered(o.Warmup+accepted, o.Timeout)
	elapsed := time.Since(t.start).Seconds()
	after, e := procStats(cmd.Process.Pid, hz)
	if e != nil {
		close(stop)
		<-done
		return e
	}
	driverAfter, _ := procStats(os.Getpid(), hz)
	close(stop)
	<-done
	profiling.Wait()
	afterMem := getMem(client, t.mgmt)
	t.mu.Lock()
	defer t.mu.Unlock()
	result, e = summarize(t.records[o.Warmup:], t.start.Sub(t.epoch).Nanoseconds(), elapsed, o.Destinations)
	if e != nil {
		err = errors.Join(err, e)
	}
	result["options"] = o
	result["app_cpu_seconds"] = after.CPU - before.CPU
	result["app_cpu_us_per_delivered"] = (after.CPU - before.CPU) * 1e6 / float64(max(accepted, 1))
	result["driver_cpu_seconds"] = driverAfter.CPU - driverBefore.CPU
	result["app_peak_rss_bytes"] = after.HWM
	result["app_rss_before_bytes"] = before.RSS
	result["app_rss_after_bytes"] = after.RSS
	result["app_write_bytes"] = after.WriteBytes - before.WriteBytes
	result["app_cpu_cores"] = (after.CPU - before.CPU) / elapsed
	mem := map[string]int64{}
	for _, k := range []string{"TotalAlloc", "Mallocs", "NumGC", "PauseTotalNs"} {
		v, ok := afterMem[k]
		b, ok2 := beforeMem[k]
		if ok && ok2 {
			mem[k] = int64(v) - int64(b)
		}
	}
	result["runtime_deltas"] = mem
	result["runtime_after"] = afterMem
	exe, _ := os.ReadFile(o.Binary)
	result["binary_sha256"] = hash(exe)
	result["toolchain"] = runtime.Version()
	result["epoch_unix_ns"] = t.epoch.UnixNano()
	result["clock"] = "process-monotonic nanoseconds from epoch; wall epoch is metadata only"
	result["gomaxprocs"] = os.Getenv("GOMAXPROCS")
	result["profiling_enabled"] = o.Profile != ""
	for _, f := range []string{"cpu.max", "memory.max", "cpu.stat"} {
		b, _ := os.ReadFile("/sys/fs/cgroup/" + f)
		result[f] = string(b)
	}
	err = errors.Join(err, save(filepath.Join(root, "measurement.json"), map[string]any{
		"begin_ns": t.start.Sub(t.epoch).Nanoseconds(), "elapsed_seconds": elapsed,
		"before": before, "after": after, "memory_before": beforeMem, "memory_after": afterMem}))
	if len(t.failures) > 0 {
		err = errors.Join(err, fmt.Errorf("sink errors: %v", t.failures))
	}
	return errors.Join(err, drainErr)
}
func summarize(rs []record, begin int64, elapsed float64, destinations int) (map[string]any, error) {
	var ack, e2e, scheduled, lag []float64
	accepted, dropped, failed, missing, dups := 0, 0, 0, 0, 0
	lastAck := begin
	for _, r := range rs {
		if r.Dropped {
			dropped++
			continue
		}
		wantStatus := 200
		if r.Protocol == "ndjson" || r.Protocol == "cloudevents" {
			wantStatus = 204
		}
		if r.Error != "" || r.Status != wantStatus {
			failed++
			continue
		}
		accepted++
		dups += r.Duplicates
		ack = append(ack, float64(r.Ack-r.Start)/1e6)
		lag = append(lag, float64(r.Start-r.Scheduled)/1e6)
		lastAck = max(lastAck, r.Ack)
		finish := int64(0)
		if len(r.Sink) != destinations {
			missing++
			continue
		}
		ok := true
		for _, v := range r.Sink {
			if v == 0 {
				ok = false
			}
			finish = max(finish, v)
		}
		if !ok {
			missing++
			continue
		}
		e2e = append(e2e, float64(finish-r.Start)/1e6)
		scheduled = append(scheduled, float64(finish-r.Scheduled)/1e6)
	}
	result := map[string]any{"offered": len(rs), "accepted": accepted, "generator_dropped": dropped, "request_failed": failed, "missing": missing, "duplicates": dups, "elapsed_seconds": elapsed, "accepted_rps": float64(accepted) / (float64(lastAck-begin) / 1e9), "delivered_rps": float64(len(e2e)) / elapsed, "ack_ms": distribution(ack), "e2e_ms": distribution(e2e), "scheduled_e2e_ms": distribution(scheduled), "dispatch_lag_ms": distribution(lag)}
	if missing > 0 || failed > 0 || dropped > 0 {
		return result, fmt.Errorf("invalid capacity sample: missing=%d failed=%d generator_dropped=%d", missing, failed, dropped)
	}
	return result, nil
}
func main() {
	var auditOnly string
	flag.StringVar(&auditOnly, "audit-only", "", "verify a saved trial without starting processes")
	var o options
	flag.StringVar(&o.Binary, "binary", "", "actual application executable")
	flag.StringVar(&o.Out, "out", "", "new artifact directory")
	flag.StringVar(&o.Protocol, "protocol", "logs", "ndjson|cloudevents|logs|metrics|traces|mixed")
	flag.StringVar(&o.Replay, "replay-interval", "10ms", "same explicit OTLP cadence for both binaries")
	flag.StringVar(&o.Profile, "profile", "", "nonempty enables a separate diagnostic CPU-profile run")
	flag.IntVar(&o.Requests, "requests", 2048, "measured request count")
	flag.IntVar(&o.Warmup, "warmup", 64, "untimed real requests retained in the receipt directory")
	flag.IntVar(&o.Concurrency, "concurrency", 32, "max upstream in-flight requests")
	flag.IntVar(&o.Payload, "payload", 512, "synthetic text bytes, excluding protocol overhead")
	flag.IntVar(&o.Destinations, "destinations", 1, "independent required local destinations")
	flag.IntVar(&o.Group, "group", 1, "legacy journal group maximum, identical for A/B")
	flag.IntVar(&o.Batch, "batch", 1, "NDJSON sender batch size")
	flag.Float64Var(&o.Rate, "rate", 0, "open-loop offered requests/sec; zero selects closed-loop")
	flag.DurationVar(&o.Delay, "sink-delay", 0, "mock downstream service time")
	flag.DurationVar(&o.Timeout, "timeout", 90*time.Second, "HTTP/drain bound")
	flag.BoolVar(&o.Gzip, "wal-gzip", false, "gzip journals")
	flag.BoolVar(&o.WireGzip, "wire-gzip", false, "gzip OTLP HTTP in both directions")
	flag.Parse()
	if auditOnly != "" {
		if e := audit(auditOnly); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		fmt.Println("saved trial audit passed")
		return
	}
	valid := map[string]bool{"ndjson": true, "cloudevents": true, "logs": true, "metrics": true, "traces": true, "mixed": true}
	if o.Binary == "" || o.Out == "" || !valid[o.Protocol] || o.Requests < 1 || o.Warmup < 1 || o.Concurrency < 1 || o.Concurrency > 1024 || o.Payload < 0 || o.Payload > 1<<20 || o.Destinations < 1 || o.Destinations > 16 || o.Rate < 0 || math.IsInf(o.Rate, 0) || math.IsNaN(o.Rate) || o.Delay < 0 || o.Timeout <= 0 {
		fmt.Fprintln(os.Stderr, "invalid options")
		os.Exit(2)
	}
	p, e := filepath.Abs(o.Binary)
	if e == nil {
		o.Binary = p
		e = run(o)
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
