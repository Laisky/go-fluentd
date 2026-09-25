package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// parseProcStatus rejects unrepresentable counters instead of silently wrapping
// them. Linux reports memory in KiB, while our saved observations use bytes.
// Threads uses Atoi so its bound follows the driver's actual int width.
func parseProcStatus(data []byte, s *resources) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if key != "VmRSS:" && key != "VmHWM:" && key != "Threads:" {
			continue
		}
		if seen[key] || len(fields) < 2 {
			return fmt.Errorf("invalid /proc status field %s", key)
		}
		seen[key] = true
		if key == "Threads:" {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 0 || len(fields) != 2 {
				return fmt.Errorf("invalid /proc thread count %q", line)
			}
			s.Threads = n
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || n < 0 || n > math.MaxInt64/1024 || len(fields) != 3 || fields[2] != "kB" {
			return fmt.Errorf("invalid /proc memory counter %q", line)
		}
		if key == "VmRSS:" {
			s.RSS = n * 1024
		} else {
			s.HWM = n * 1024
		}
	}
	if len(seen) != 3 {
		return fmt.Errorf("incomplete /proc memory/thread status")
	}
	return nil
}
