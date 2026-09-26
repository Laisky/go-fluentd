package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// An exact match against a generated valid fixture is already a full content
// check. Avoid re-decoding large unchanged strings in the load generator. The
// independent canonical parser remains the fallback for reordered JSON keys.
// This optimizes the mock, not the application; use this same driver for A/B.
func (t *trial) eventKey(protocol string, body []byte) (string, error) {
	key := protocol + ":" + hash(bytes.TrimSpace(body))
	if _, ok := t.lookup[key]; ok {
		return key, nil
	}
	v, err := canonical(body)
	return protocol + ":" + hash([]byte(v)), err
}

// Capture the live application's CPU and two heap snapshots. Their alloc_space
// difference measures allocation churn in the same interval; inuse_space shows
// retained Go objects (not process RSS). No forced GC is requested. Profiles
// are diagnostic and must not be pooled with unprofiled performance trials.
func (t *trial) captureProfiles() error {
	time.Sleep(t.opts.ProfileDelay)
	seconds := t.opts.ProfileSeconds
	if seconds == 0 {
		seconds = 5
	} // compatibility with programmatic callers
	client := &http.Client{Timeout: time.Duration(seconds+15) * time.Second,
		Transport: &http.Transport{Proxy: nil, DisableCompression: true}}
	defer client.CloseIdleConnections()
	fetch := func(path, name string) error {
		r, err := client.Get(t.mgmt + path)
		if err != nil {
			return err
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", path, r.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 64<<20+1))
		if err != nil {
			return err
		}
		if len(b) > 64<<20 || len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
			return fmt.Errorf("%s: invalid/oversized profile", path)
		}
		return os.WriteFile(filepath.Join(t.root, name), b, 0600)
	}
	begin := time.Since(t.epoch).Nanoseconds()
	if err := fetch("/pprof/heap", "heap-start.pprof"); err != nil {
		return err
	}
	if err := fetch(fmt.Sprintf("/pprof/profile?seconds=%d", seconds), "cpu.pprof"); err != nil {
		return err
	}
	if err := fetch("/pprof/heap", "heap-end.pprof"); err != nil {
		return err
	}
	return save(filepath.Join(t.root, "profile-window.json"), map[string]any{
		"begin_ns": begin, "end_ns": time.Since(t.epoch).Nanoseconds(), "cpu_seconds": seconds,
		"forced_gc": false, "interpretation": "diagnostic only; heap difference alloc_space is sampled allocation, not RSS"})
}
