package monitor

import (
	"github.com/prometheus/client_golang/prometheus"
	"sync"
)

// StorageMetrics contains fixed low-cardinality operational values only. No
// payloads, URLs, credentials, destination IDs or arbitrary monitor fields can
// become labels. It shares the already-existing management /metrics listener.
type StorageMetrics struct {
	SharedConfigured, SharedKnown, RecoveryReady, FilesystemAvailable, PackingPending                                                          bool
	LimitBytes, ChargedBytes, ReservedBytes, PendingUpperBound, InventoryFiles, InventoryBytes                                                 int64
	AvailableBytes, FreeInodes, AdmissionRejected, CodecRejected, ScanRejected, SharedRejected, CompactionDeferred, PackedRecords, PackedParts uint64
}
type storageMetric struct {
	name, help string
	counter    bool
	kind       prometheus.ValueType
	value      func(StorageMetrics) float64
	desc       *prometheus.Desc
}
type storageCollector struct {
	get     func() StorageMetrics
	metrics []storageMetric
}

func flag(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
func newStorageCollector(get func() StorageMetrics) prometheus.Collector {
	specs := []storageMetric{
		{name: "shared_budget_configured", help: "Whether the overall storage budget is configured.", value: func(s StorageMetrics) float64 { return flag(s.SharedConfigured) }},
		{name: "shared_budget_known", help: "Whether authoritative storage accounting is currently readable.", value: func(s StorageMetrics) float64 { return flag(s.SharedKnown) }},
		{name: "shared_limit_bytes", help: "Configured aggregate storage ceiling; use only when accounting is known.", value: func(s StorageMetrics) float64 { return float64(s.LimitBytes) }},
		{name: "shared_charged_bytes", help: "Retained and conservatively charged storage bytes.", value: func(s StorageMetrics) float64 { return float64(s.ChargedBytes) }},
		{name: "shared_reserved_bytes", help: "Durable future-work reservations inside the same ceiling.", value: func(s StorageMetrics) float64 { return float64(s.ReservedBytes) }},
		{name: "pending_upper_bound", help: "Conservative uncheckpointed envelope count.", value: func(s StorageMetrics) float64 { return float64(s.PendingUpperBound) }},
		{name: "recovery_ready", help: "Whether the retained replay frontier is known.", value: func(s StorageMetrics) float64 { return flag(s.RecoveryReady) }},
		{name: "filesystem_available", help: "Whether filesystem capacity metadata is readable.", value: func(s StorageMetrics) float64 { return flag(s.FilesystemAvailable) }},
		{name: "filesystem_available_bytes", help: "Application-available filesystem bytes.", value: func(s StorageMetrics) float64 { return float64(s.AvailableBytes) }},
		{name: "filesystem_free_inodes", help: "Available filesystem inodes.", value: func(s StorageMetrics) float64 { return float64(s.FreeInodes) }},
		{name: "inventory_files", help: "Last complete admission inventory entry count; may be a lower bound on rejection.", value: func(s StorageMetrics) float64 { return float64(s.InventoryFiles) }},
		{name: "inventory_bytes", help: "Last admission inventory bytes.", value: func(s StorageMetrics) float64 { return float64(s.InventoryBytes) }},
		{name: "accepted_archive_packing_pending", help: "Whether accepted loose originals still need funded lossless compaction.", value: func(s StorageMetrics) float64 { return flag(s.PackingPending) }},
		{name: "accepted_archive_packed_records", help: "Committed original receipts in normal logging storage parts.", value: func(s StorageMetrics) float64 { return float64(s.PackedRecords) }},
		{name: "accepted_archive_parts", help: "Bounded committed normal logging storage part count.", value: func(s StorageMetrics) float64 { return float64(s.PackedParts) }},
		{name: "admission_rejected_total", help: "Whole-root admission refusals.", counter: true, value: func(s StorageMetrics) float64 { return float64(s.AdmissionRejected) }},
		{name: "codec_admission_rejected_total", help: "Requests refused before persistence because their exact destination representation cannot fit.", counter: true, value: func(s StorageMetrics) float64 { return float64(s.CodecRejected) }},
		{name: "scan_budget_rejected_total", help: "Admissions refused on bounded inventory uncertainty.", counter: true, value: func(s StorageMetrics) float64 { return float64(s.ScanRejected) }},
		{name: "shared_budget_rejected_total", help: "Mutations refused by the global budget.", counter: true, value: func(s StorageMetrics) float64 { return float64(s.SharedRejected) }},
		{name: "archive_compaction_deferred_total", help: "Lossless packing passes deferred before mutation for unavailable funded capacity.", counter: true, value: func(s StorageMetrics) float64 { return float64(s.CompactionDeferred) }},
	}
	for i := range specs {
		specs[i].kind = prometheus.GaugeValue
		if specs[i].counter {
			specs[i].kind = prometheus.CounterValue
		}
		specs[i].desc = prometheus.NewDesc("gofluentd_logging_"+specs[i].name, specs[i].help, nil, nil)
	}
	return &storageCollector{get: get, metrics: specs}
}
func (c *storageCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.metrics {
		ch <- m.desc
	}
}
func (c *storageCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.get()
	for _, m := range c.metrics {
		ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value(s))
	}
}

var storagePromOnce sync.Once
var storageGetter func() StorageMetrics

// RegisterStorageMetrics replaces only the fixed operational source. Existing
// arbitrary /monitor callbacks are not evaluated or exported by this collector.
func RegisterStorageMetrics(get func() StorageMetrics) {
	metricMu.Lock()
	storageGetter = get
	metricMu.Unlock()
	storagePromOnce.Do(func() {
		prometheus.MustRegister(newStorageCollector(func() StorageMetrics {
			metricMu.RLock()
			f := storageGetter
			metricMu.RUnlock()
			if f == nil {
				return StorageMetrics{}
			}
			return f()
		}))
	})
}
