package alerts

import (
	"fmt"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"gofluentd/library/otlpwire"
	"math"
	"time"
)

// ObserveOTLP uses only the original admitted request, never the storage projection.
// Oversized requests are skipped to bound alert-side memory and decoding work.
func (o *Observer) ObserveOTLP(r *otlpwire.Request) {
	if o == nil || r == nil || r.Signal() != otlpwire.Logs {
		return
	}
	o.lifecycle.RLock()
	defer o.lifecycle.RUnlock()
	if o.stopped {
		o.shutdownRequests.Add(1)
		return
	}
	if r.PayloadSize() > 1<<20 {
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
	p := otlpPayload{r.ContentType(), r.Payload()}
	o.otlp <- p
}

type otlpPayload struct {
	contentType string
	data        []byte
}

func (o *Observer) parseOTLP(p otlpPayload) {
	req := plogotlp.NewExportRequest()
	var err error
	if p.contentType == otlpwire.JSON {
		err = req.UnmarshalJSON(p.data)
	} else {
		err = req.UnmarshalProto(p.data)
	}
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
		source := "otlp/" + cleanMetadata(service.Str(), 64)
		if v, ok := r.Resource().Attributes().Get("host.name"); ok && v.Type() == pcommon.ValueTypeStr {
			source += "/" + cleanMetadata(v.Str(), 64)
		}
		scopes := r.ScopeLogs()
		for j := 0; j < scopes.Len(); j++ {
			scope := scopes.At(j)
			scopeSource := source + "/" + cleanMetadata(scope.Scope().Name(), 64)
			records := scope.LogRecords()
			for k := 0; k < records.Len(); k++ {
				if o.ctx != nil && o.ctx.Err() != nil {
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
				message := ""
				if o.cfg.IncludeMessage {
					message = rec.Body().AsString()
				}
				var timestamp time.Time
				if ns := uint64(rec.Timestamp()); ns > 0 && ns <= math.MaxInt64 {
					timestamp = time.Unix(0, int64(ns)).UTC()
				}
				o.Observe(Event{Source: scopeSource, Severity: level, OriginalSeverity: fmt.Sprintf("%d", n), Message: message, Timestamp: timestamp})
			}
		}
	}
}
