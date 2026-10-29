package alerts

import (
	"fmt"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"gofluentd/library/otlpwire"
)

// ObserveOTLP uses only the original admitted request, never the storage projection.
// Oversized requests are skipped to bound alert-side memory and decoding work.
func (o *Observer) ObserveOTLP(r *otlpwire.Request) {
	if o == nil || r == nil || r.Signal() != otlpwire.Logs {
		return
	}
	if r.PayloadSize() > 1<<20 {
		o.dropped.Add(1)
		return
	}
	select {
	case o.otlpSlots <- struct{}{}:
	default:
		o.dropped.Add(1)
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
		o.dropped.Add(1)
		return
	}
	rs := req.Logs().ResourceLogs()
	for i := 0; i < rs.Len(); i++ {
		r := rs.At(i)
		service, ok := r.Resource().Attributes().Get("service.name")
		if !ok || !contains(o.cfg.Services, service.AsString()) {
			continue
		}
		source := "otlp/" + service.AsString()
		if v, ok := r.Resource().Attributes().Get("host.name"); ok {
			source += "/" + v.AsString()
		}
		scopes := r.ScopeLogs()
		for j := 0; j < scopes.Len(); j++ {
			scope := scopes.At(j)
			records := scope.LogRecords()
			for k := 0; k < records.Len(); k++ {
				rec := records.At(k)
				n := int32(rec.SeverityNumber())
				level := rec.SeverityText()
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
				o.Observe(Event{Source: source + "/" + scope.Scope().Name(), Severity: level, OriginalSeverity: fmt.Sprintf("%d/%s", n, rec.SeverityText()), Message: rec.Body().AsString()})
			}
		}
	}
}
