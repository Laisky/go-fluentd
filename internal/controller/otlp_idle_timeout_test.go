package controller

import (
	"net/http"
	"testing"
	"time"
)

// TestOTLPDefaultIdleTimeoutOutlastsGoClients pins the keep-alive ordering that
// stops OTLP batches from being lost to connection reuse.
//
// OpenTelemetry Go exporters keep idle connections for net/http's default
// IdleConnTimeout and treat a transport error as final. When the receiver's
// idle timeout was the shorter one (30s against 90s), a client exporting on a
// 30-second cadence reused connections the receiver was closing; production
// lost one batch per such collision, every failure landing 30 seconds after the
// previous export. A scaled reproduction (30ms server, 90ms client, 30ms gaps)
// failed 75 of 500 requests; with the server outlasting the client, 0 of 500.
func TestOTLPDefaultIdleTimeoutOutlastsGoClients(t *testing.T) {
	cfg, err := ParseOTLPServiceConfig(map[string]interface{}{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	cfg.defaults()
	client := http.DefaultTransport.(*http.Transport).IdleConnTimeout
	if cfg.IdleTimeout <= client {
		t.Fatalf("default idle_timeout %s must exceed the Go client default %s", cfg.IdleTimeout, client)
	}
	if cfg.IdleTimeout != 120*time.Second {
		t.Fatalf("default idle_timeout = %s, documented as 120s", cfg.IdleTimeout)
	}

	explicit, err := ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "idle_timeout": "30s"})
	if err != nil {
		t.Fatal(err)
	}
	explicit.defaults()
	if explicit.IdleTimeout != 30*time.Second {
		t.Fatalf("an explicit idle_timeout must still win, got %s", explicit.IdleTimeout)
	}
}
