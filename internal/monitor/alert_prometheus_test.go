package monitor

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAlertMetricsFixedAndPrivate(t *testing.T) {
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(newAlertCollector(func() AlertMetrics {
		return AlertMetrics{EventsDropped: 2, RequestsDropped: 3, SourceLimited: 5, EventQueueDepth: 4, ParserAlive: true}
	}))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]float64{}
	for _, f := range families {
		for _, m := range f.Metric {
			if len(m.Label) != 0 {
				t.Fatal("unexpected labels")
			}
			if m.Counter != nil {
				found[f.GetName()] = m.Counter.GetValue()
			}
			if m.Gauge != nil {
				found[f.GetName()] = m.Gauge.GetValue()
			}
		}
	}
	if found["gofluentd_logging_alert_events_dropped_total"] != 2 || found["gofluentd_logging_alert_requests_dropped_total"] != 3 || found["gofluentd_logging_alert_events_source_limited_total"] != 5 || found["gofluentd_logging_alert_event_queue_depth"] != 4 || found["gofluentd_logging_alert_parser_up"] != 1 {
		t.Fatal(found)
	}
}
