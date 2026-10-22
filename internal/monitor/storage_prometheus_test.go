package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"testing"
)

func TestStorageMetricsAreFixedPrivateAndRepresentUnknown(t *testing.T) {
	AddMetric("unrelated-private", func() map[string]interface{} { t.Fatal("unrelated private getter evaluated by Prometheus"); return nil })
	t.Cleanup(func() { metricMu.Lock(); delete(metricGetter, "unrelated-private"); metricMu.Unlock() })
	value := StorageMetrics{SharedConfigured: true, SharedKnown: true, RecoveryReady: true, LimitBytes: 3000000000, ChargedBytes: 100, ReservedBytes: 200, AdmissionRejected: 7, PackedRecords: 12000}
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(newStorageCollector(func() StorageMetrics { return value }))
	families, e := registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]float64{}
	for _, f := range families {
		for _, m := range f.Metric {
			if len(m.Label) != 0 {
				t.Fatal("dynamic or private labels")
			}
			if f.GetName() == "gofluentd_logging_admission_rejected_total" && m.Counter == nil {
				t.Fatal("refusal counter exported as gauge")
			}
			if m.Gauge != nil {
				seen[f.GetName()] = m.Gauge.GetValue()
			} else {
				seen[f.GetName()] = m.Counter.GetValue()
			}
		}
	}
	if len(seen) != 20 || seen["gofluentd_logging_shared_limit_bytes"] != 3000000000 || seen["gofluentd_logging_admission_rejected_total"] != 7 || seen["gofluentd_logging_accepted_archive_packed_records"] != 12000 {
		t.Fatal(seen)
	}
	value.SharedKnown = false
	families, e = registry.Gather()
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range families {
		if f.GetName() == "gofluentd_logging_shared_budget_known" && f.Metric[0].Gauge.GetValue() != 0 {
			t.Fatal("unknown accounting falsely reported healthy")
		}
	}
}
