package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
)

// AlertMetrics contains only aggregate, low-cardinality operational data.
type AlertMetrics struct {
	EventsQueued, EventsDropped, RequestsDropped, Suppressed, Sent, Failed, Retries, Evicted, ShutdownEvents, ShutdownRequests uint64
	RequestTooLarge, RequestQueueFull, RequestDecodeError                                                                      uint64
	EventQueueDepth, RequestQueueDepth                                                                                         int
	ParserAlive, DeliveryAlive                                                                                                 bool
}
type alertMetric struct {
	name, help string
	counter    bool
	value      func(AlertMetrics) float64
	desc       *prometheus.Desc
}
type alertCollector struct {
	get   func() AlertMetrics
	specs []alertMetric
}

func newAlertCollector(get func() AlertMetrics) prometheus.Collector {
	specs := []alertMetric{
		{name: "events_queued_total", help: "Alert events admitted to the delivery queue.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.EventsQueued) }},
		{name: "events_dropped_total", help: "Alert events dropped by policy or delivery queue saturation.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.EventsDropped) }},
		{name: "requests_dropped_total", help: "OTLP requests skipped by the observer before event processing.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.RequestsDropped) }},
		{name: "requests_too_large_total", help: "OTLP requests skipped because the observer payload limit was exceeded.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.RequestTooLarge) }},
		{name: "requests_queue_full_total", help: "OTLP requests skipped because the observer request queue was full.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.RequestQueueFull) }},
		{name: "requests_decode_failed_total", help: "OTLP requests that could not be decoded by the observer.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.RequestDecodeError) }},
		{name: "events_suppressed_total", help: "Events suppressed by the deduplication window.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.Suppressed) }},
		{name: "deliveries_total", help: "Notifications accepted by the configured transport.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.Sent) }},
		{name: "delivery_failures_total", help: "Notifications abandoned after delivery failure.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.Failed) }},
		{name: "retries_total", help: "Delivery retry attempts scheduled.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.Retries) }},
		{name: "dedup_evictions_total", help: "Deduplication entries evicted to admit a new group.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.Evicted) }},
		{name: "shutdown_events_dropped_total", help: "Queued or in-flight alert events abandoned at shutdown.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.ShutdownEvents) }},
		{name: "shutdown_requests_dropped_total", help: "Pending OTLP observation requests abandoned at shutdown.", counter: true, value: func(s AlertMetrics) float64 { return float64(s.ShutdownRequests) }},
		{name: "event_queue_depth", help: "Current delivery queue depth.", value: func(s AlertMetrics) float64 { return float64(s.EventQueueDepth) }},
		{name: "request_queue_depth", help: "Current OTLP observation queue depth.", value: func(s AlertMetrics) float64 { return float64(s.RequestQueueDepth) }},
		{name: "parser_up", help: "Whether the alert parser worker is running.", value: func(s AlertMetrics) float64 { return flag(s.ParserAlive) }},
		{name: "delivery_up", help: "Whether the alert delivery worker is running.", value: func(s AlertMetrics) float64 { return flag(s.DeliveryAlive) }},
	}
	for i := range specs {
		specs[i].desc = prometheus.NewDesc("gofluentd_logging_alert_"+specs[i].name, specs[i].help, nil, nil)
	}
	return &alertCollector{get: get, specs: specs}
}
func (c *alertCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.specs {
		ch <- m.desc
	}
}
func (c *alertCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.get()
	for _, m := range c.specs {
		kind := prometheus.GaugeValue
		if m.counter {
			kind = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(m.desc, kind, m.value(s))
	}
}

var alertPromOnce sync.Once
var alertGetter func() AlertMetrics

func RegisterAlertMetrics(get func() AlertMetrics) {
	metricMu.Lock()
	alertGetter = get
	metricMu.Unlock()
	alertPromOnce.Do(func() {
		prometheus.MustRegister(newAlertCollector(func() AlertMetrics {
			metricMu.RLock()
			f := alertGetter
			metricMu.RUnlock()
			if f == nil {
				return AlertMetrics{}
			}
			return f()
		}))
	})
}
