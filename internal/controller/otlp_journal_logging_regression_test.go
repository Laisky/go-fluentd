//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"gofluentd/internal/controller"
	"gofluentd/internal/otlpstate"
	collectorlog "gofluentd/library/log"
)

// Real OTLP journal construction must inherit the collector's configured
// logger, so alert hooks and dynamic level controls see WAL failures.
func TestRegressionOTLPJournalUsesCollectorLogger(t *testing.T) {
	const childFlag = "GOFLUENTD_OTLP_LOGGER_CHILD"
	if os.Getenv(childFlag) == "1" {
		var observed atomic.Int64
		original := collectorlog.Logger
		collectorlog.Logger = original.WithOptions(zap.HooksWithFields(func(entry zapcore.Entry, _ []zapcore.Field) error {
			if entry.Message == "new journal" && entry.LoggerName == "gofluentd.journal.otlp" {
				observed.Add(1)
			}
			return nil
		}))
		defer func() { collectorlog.Logger = original }()
		p, err := controller.OpenOTLPJournal(context.Background(), controller.OTLPJournalConfig{
			Directory: t.TempDir(),
		}, []controller.OTLPDestination{{ID: "sink", Send: func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
			return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
		if got := observed.Load(); got != 1 {
			t.Fatalf("OTLP journal escaped collector logging: got %d named init events, want 1", got)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRegressionOTLPJournalUsesCollectorLogger$", "-test.count=1")
	cmd.Env = append(os.Environ(), childFlag+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated OTLP journal logging fixture: %v; output=%s", err, strings.TrimSpace(string(output)))
	}
}
