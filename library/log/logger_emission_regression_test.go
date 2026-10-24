package log_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	collectorlog "gofluentd/library/log"
)

var legacyConsoleTimestamp = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}`)

// This is the old SDK's externally observable emission contract, not merely
// a test of a replacement core with arbitrary configuration.
func TestRegressionCollectorLegacyEmissionAndAlerts(t *testing.T) {
	const childFlag = "GOFLUENTD_LOGGER_EMISSION_CHILD"
	const errorMessage = "synthetic-collector-error-burst"
	const errorsToEmit = 180
	if os.Getenv(childFlag) == "1" {
		hooks := 0
		logger := collectorlog.Logger.WithOptions(zap.HooksWithFields(func(entry zapcore.Entry, _ []zapcore.Field) error {
			if entry.Message == errorMessage {
				hooks++
			}
			return nil
		}))
		logger.Info("synthetic-collector-console-info")
		for i := 0; i < errorsToEmit; i++ {
			logger.Error(errorMessage, zap.Int("sequence", i))
		}
		fmt.Printf("synthetic-collector-hook-count=%d\n", hooks)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestRegressionCollectorLegacyEmissionAndAlerts$", "-test.count=1")
	cmd.Env = append(os.Environ(), childFlag+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("isolated logger fixture: %v; stdout tail=%q stderr tail=%q", err, lastBytes(stdout.String(), 512), lastBytes(stderr.String(), 512))
	}
	out := stdout.String()
	if !strings.Contains(out, "synthetic-collector-console-info") {
		t.Error("ordinary collector log moved away from legacy stdout destination")
	}
	if strings.Contains(stderr.String(), "synthetic-collector-console-info") || strings.Contains(stderr.String(), errorMessage) {
		t.Error("collector emitted ordinary/error records on stderr instead of historical stdout")
	}
	if got := strings.Count(out, errorMessage); got != errorsToEmit {
		t.Errorf("identical error messages dropped: got %d, want %d", got, errorsToEmit)
	}
	if !strings.Contains(out, fmt.Sprintf("synthetic-collector-hook-count=%d", errorsToEmit)) {
		t.Error("error alert hooks did not receive every emitted event")
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "synthetic-collector-console-info") {
			continue
		}
		found = true
		if !legacyConsoleTimestamp.MatchString(line) {
			t.Errorf("RFC3339 timestamp was replaced: %q", line)
		}
		if !strings.Contains(line, "INFO") {
			t.Errorf("legacy uppercase severity was replaced: %q", line)
		}
		break
	}
	if !found {
		t.Error("no collector info entry found for format assertion")
	}
}

func lastBytes(s string, limit int) string {
	if len(s) > limit {
		return s[len(s)-limit:]
	}
	return s
}
