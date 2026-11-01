package alerts

import (
	"fmt"
	"math"
	"time"

	"gofluentd/library/otlpwire"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
)

// logsDecoder is the queued form of an admitted request. Keeping the immutable
// request, rather than a copy, leaves the acknowledgement path free of work
// proportional to its size.
type logsDecoder interface {
	DecodeLogs() (plogotlp.ExportRequest, error)
}

// ObserveOTLP uses only the original admitted request, never the storage
// projection. It does nothing without selected services, so a tag-only
// configuration costs admission no more than this check. Oversized requests are
// skipped to bound alert-side memory and decoding work.
func (o *Observer) ObserveOTLP(r *otlpwire.Request) {
	if o == nil || r == nil || len(o.cfg.Services) == 0 || r.Signal() != otlpwire.Logs {
		return
	}
	o.lifecycle.RLock()
	defer o.lifecycle.RUnlock()
	if o.stopped {
		o.shutdownRequests.Add(1)
		return
	}
	if r.PayloadSize() > maxOTLPRequestBytes {
		o.requestTooLarge.Add(1)
		o.dropped.Add(1)
		o.droppedRequests.Add(1)
		return
	}
	select {
	case o.otlpSlots <- struct{}{}:
	default:
		o.requestQueueFull.Add(1)
		o.dropped.Add(1)
		o.droppedRequests.Add(1)
		return
	}
	o.otlp <- r
}

func (o *Observer) parseOTLP(request logsDecoder) {
	req, err := request.DecodeLogs()
	if err != nil {
		o.requestDecodeError.Add(1)
		o.dropped.Add(1)
		o.droppedRequests.Add(1)
		return
	}
	rs := req.Logs().ResourceLogs()
	for i := 0; i < rs.Len(); i++ {
		r := rs.At(i)
		service, ok := r.Resource().Attributes().Get("service.name")
		if !ok || service.Type() != pcommon.ValueTypeStr || !contains(o.cfg.Services, service.Str()) {
			continue
		}
		origin := "otlp/" + cleanMetadata(service.Str(), 64)
		source := origin
		if v, ok := r.Resource().Attributes().Get("host.name"); ok && v.Type() == pcommon.ValueTypeStr {
			source += "/" + cleanMetadata(v.Str(), 64)
		}
		scopes := r.ScopeLogs()
		for j := 0; j < scopes.Len(); j++ {
			scope := scopes.At(j)
			scopeSource := source + "/" + cleanMetadata(scope.Scope().Name(), 64)
			records := scope.LogRecords()
			for k := 0; k < records.Len(); k++ {
				if o.ctx.Err() != nil {
					o.shutdownRequests.Add(1)
					return
				}
				rec := records.At(k)
				n := int32(rec.SeverityNumber())
				level := rec.SeverityText()
				if len(level) > 64 {
					level = ""
				}
				if n != 0 {
					if n < 17 || n > 24 {
						continue
					}
					level = "error"
					if n >= 21 {
						level = "fatal"
					}
				} else if !IsError(level) {
					continue
				}
				var timestamp time.Time
				if ns := uint64(rec.Timestamp()); ns > 0 && ns <= math.MaxInt64 {
					timestamp = time.Unix(0, int64(ns)).UTC()
				}
				o.Observe(Event{Source: scopeSource, origin: origin, Severity: level, OriginalSeverity: fmt.Sprintf("%d", n), Message: rec.Body().AsString(), Timestamp: timestamp})
			}
		}
	}
}
