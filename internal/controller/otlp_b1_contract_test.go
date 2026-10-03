//go:build linux || darwin

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
)

type b1AdmissionOutcome string

const (
	b1AdmissionAccepted   b1AdmissionOutcome = "accepted"
	b1AdmissionByteLimit  b1AdmissionOutcome = "byte_limit"
	b1AdmissionScanBudget b1AdmissionOutcome = "scan_budget"
	b1AdmissionUnexpected b1AdmissionOutcome = "unexpected"
)

// These helpers belong to the test harness, not the production admission path.
// Keep the contracts in the default suite even when the measurement tag is off.
func b1ClassifyAdmission(err error) b1AdmissionOutcome {
	switch {
	case err == nil:
		return b1AdmissionAccepted
	case errors.Is(err, ErrOTLPJournalScanBudget):
		// rejectScanBudget joins both sentinels. The more specific cause wins.
		return b1AdmissionScanBudget
	case errors.Is(err, ErrOTLPJournalCapacity):
		return b1AdmissionByteLimit
	default:
		return b1AdmissionUnexpected
	}
}

func b1CheckByteCapacityEvidence(classifiedScanRefusals, journalScanRefusals uint64) error {
	if classifiedScanRefusals != 0 || journalScanRefusals != 0 {
		return fmt.Errorf("byte-capacity qualification invalid: classified scan refusals=%d journal scan refusals=%d", classifiedScanRefusals, journalScanRefusals)
	}
	return nil
}

func b1CheckDeliverySet(admitted map[string]bool, delivered map[string]int) error {
	if len(delivered) != len(admitted) {
		return fmt.Errorf("delivery set differs: admitted=%d delivered=%d", len(admitted), len(delivered))
	}
	for payload := range admitted {
		if delivered[payload] != 1 {
			return fmt.Errorf("admitted original payload delivery count=%d, want 1", delivered[payload])
		}
	}
	return nil
}

func TestRegressionB1AdmissionClassification(t *testing.T) {
	joined := errors.Join(ErrOTLPJournalCapacity, ErrOTLPJournalScanBudget)
	for _, tc := range []struct {
		name string
		err  error
		want b1AdmissionOutcome
	}{
		{"accepted", nil, b1AdmissionAccepted},
		{"byte_limit", ErrOTLPJournalCapacity, b1AdmissionByteLimit},
		{"wrapped_byte_limit", fmt.Errorf("admit: %w", ErrOTLPJournalCapacity), b1AdmissionByteLimit},
		{"scan_budget", ErrOTLPJournalScanBudget, b1AdmissionScanBudget},
		{"joined_scan_budget", joined, b1AdmissionScanBudget},
		{"reverse_joined_scan_budget", errors.Join(ErrOTLPJournalScanBudget, ErrOTLPJournalCapacity), b1AdmissionScanBudget},
		{"wrapped_joined_scan_budget", fmt.Errorf("admit: %w", joined), b1AdmissionScanBudget},
		{"canceled", context.Canceled, b1AdmissionUnexpected},
		{"deadline", context.DeadlineExceeded, b1AdmissionUnexpected},
		{"storage_fault", ErrOTLPJournalFault, b1AdmissionUnexpected},
		{"closed", ErrOTLPJournalClosed, b1AdmissionUnexpected},
		{"same_text_not_sentinel", errors.New(ErrOTLPJournalCapacity.Error()), b1AdmissionUnexpected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := b1ClassifyAdmission(tc.err); got != tc.want {
				t.Fatalf("classification=%q, want %q: %v", got, tc.want, tc.err)
			}
		})
	}
}

func TestRegressionB1ByteCapacityEvidenceGate(t *testing.T) {
	for _, tc := range []struct {
		name                string
		classified, journal uint64
		valid               bool
	}{
		{"no_scan_refusal", 0, 0, true},
		{"classified_only", 1, 0, false},
		{"counter_only", 0, 1, false},
		{"both", 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b1CheckByteCapacityEvidence(tc.classified, tc.journal); (err == nil) != tc.valid {
				t.Fatalf("evidence validity=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestRegressionB1RealAdmissionScanBudget(t *testing.T) {
	for _, cause := range []string{"entry_limit", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			p := journalBarrierOwner(t, t.TempDir(), barrierAccepted)
			p.cfg.StorageScanTimeout = time.Millisecond
			p.scanWAL = func(ctx context.Context) (int64, error) {
				if cause == "deadline" {
					<-ctx.Done()
					return 0, ctx.Err()
				}
				return 0, ErrOTLPJournalScanBudget
			}
			writes := 0
			p.writeWAL = func(*journal.Data) error { writes++; return nil }
			frontier := p.frontier
			err := p.Admit(context.Background(), journalBarrierRequest(t))
			if !errors.Is(err, ErrOTLPJournalCapacity) || !errors.Is(err, ErrOTLPJournalScanBudget) {
				t.Fatalf("real admission did not return both sentinels: %v", err)
			}
			if got := b1ClassifyAdmission(err); got != b1AdmissionScanBudget {
				t.Fatalf("real scan refusal misclassified as %q", got)
			}
			if writes != 0 || p.frontier != frontier || p.Err() != nil || p.ScanBudgetRejected() != 1 {
				t.Fatalf("scan refusal mutated admission: writes=%d frontier=%d scans=%d fault=%v", writes, p.frontier, p.ScanBudgetRejected(), p.Err())
			}
			if err := b1CheckByteCapacityEvidence(1, p.ScanBudgetRejected()); err == nil {
				t.Fatal("scan-limited run qualified as byte-capacity evidence")
			}
		})
	}
}

func TestRegressionB1DeliverySet(t *testing.T) {
	admitted := map[string]bool{"log-original": true, "metric-original": true}
	for _, tc := range []struct {
		name      string
		delivered map[string]int
		valid     bool
	}{
		{"exact", map[string]int{"log-original": 1, "metric-original": 1}, true},
		{"lost", map[string]int{"log-original": 1}, false},
		{"duplicated", map[string]int{"log-original": 2, "metric-original": 1}, false},
		{"refused_delivered", map[string]int{"log-original": 1, "metric-original": 1, "refused": 1}, false},
		{"payload_changed_same_count", map[string]int{"log-original": 1, "metric-mutated": 1}, false},
		{"zero_count", map[string]int{"log-original": 1, "metric-original": 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b1CheckDeliverySet(admitted, tc.delivered); (err == nil) != tc.valid {
				t.Fatalf("delivery validity=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
