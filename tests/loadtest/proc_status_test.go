package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestProcStatusPreservesValidCounters(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		rss, hwm   int64
		threads    int
	}{
		{"normal", "Name:\tgo-fluentd\nVmRSS:\t1234 kB\nVmHWM: 5678 kB\nThreads: 12\n", 1234 * 1024, 5678 * 1024, 12},
		{"zero", "VmRSS: 0 kB\nVmHWM: 0 kB\nThreads: 0\n", 0, 0, 0},
		{"bounds", fmt.Sprintf("VmRSS: %d kB\nVmHWM: %d kB\nThreads: %d\n", int64(math.MaxInt64/1024), int64(math.MaxInt64/1024), int(^uint(0)>>1)), math.MaxInt64 / 1024 * 1024, math.MaxInt64 / 1024 * 1024, int(^uint(0) >> 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got resources
			if err := parseProcStatus([]byte(tc.text), &got); err != nil {
				t.Fatal(err)
			}
			if got.RSS != tc.rss || got.HWM != tc.hwm || got.Threads != tc.threads {
				t.Fatalf("changed counters: %+v", got)
			}
		})
	}
}

func TestProcStatusRejectsInvalidCounters(t *testing.T) {
	const valid = "VmRSS: 123 kB\nVmHWM: 456 kB\nThreads: 12\n"
	threadOverflow := strconv.FormatUint(uint64(^uint(0)>>1)+1, 10)
	for _, tc := range []struct{ name, from, to string }{
		{"thread-overflow", "Threads: 12", "Threads: " + threadOverflow},
		{"thread-negative", "Threads: 12", "Threads: -1"},
		{"thread-malformed", "Threads: 12", "Threads: NaN"},
		{"thread-missing-value", "Threads: 12", "Threads:"},
		{"thread-extra-value", "Threads: 12", "Threads: 12 34"},
		{"rss-byte-overflow", "123 kB", strconv.FormatInt(math.MaxInt64/1024+1, 10) + " kB"},
		{"hwm-byte-overflow", "456 kB", strconv.FormatInt(math.MaxInt64/1024+1, 10) + " kB"},
		{"rss-negative", "123 kB", "-1 kB"},
		{"hwm-negative", "456 kB", "-1 kB"},
		{"rss-malformed", "123 kB", "NaN kB"},
		{"rss-integer-overflow", "123 kB", "9223372036854775808 kB"},
		{"wrong-unit", "123 kB", "123 MB"},
		{"missing-unit", "123 kB", "123"},
		{"missing-field", "VmHWM: 456 kB\n", ""},
		{"duplicate-field", "Threads: 12", "Threads: 12\nThreads: 13"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got resources
			if err := parseProcStatus([]byte(strings.Replace(valid, tc.from, tc.to, 1)), &got); err == nil {
				t.Fatalf("accepted invalid %s: %+v", tc.name, got)
			}
		})
	}
}

func TestProcStatsReadsCurrentProcess(t *testing.T) {
	got, err := procStats(os.Getpid(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.RSS <= 0 || got.HWM < got.RSS || got.Threads <= 0 || got.CPU < 0 {
		t.Fatalf("invalid live counters: %+v", got)
	}
}
