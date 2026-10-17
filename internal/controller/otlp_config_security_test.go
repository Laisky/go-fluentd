package controller_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Laisky/zap"
	"github.com/Laisky/zap/zapcore"
	"github.com/go-viper/mapstructure/v2"
	"gofluentd/internal/controller"
)

// TestOTLPConfigDiagnosticsDoNotRenderValues checks malformed top-level and nested configuration without accessing live settings.
func TestOTLPConfigDiagnosticsDoNotRenderValues(t *testing.T) {
	const canary = "synthetic-otlp-config-secret"
	cases := map[string]map[string]interface{}{
		"duration":           {"enabled": true, "idle_timeout": canary},
		"nested_duration":    {"enabled": true, "destinations": []interface{}{map[string]interface{}{"timeout": canary}}},
		"wrong_bool_type":    {"enabled": canary},
		"wrong_integer_type": {"enabled": true, "max_connections": canary},
		"nested_wrong_type":  {"enabled": true, "destinations": []interface{}{map[string]interface{}{"logs_endpoint": []interface{}{canary}}}},
		"unknown_field":      {"enabled": true, canary: "unused"},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := controller.ParseOTLPServiceConfig(raw)
			if err == nil || cfg != nil {
				t.Fatal("malformed configuration was accepted")
			}
			fields := zapcore.NewMapObjectEncoder()
			zap.Error(err).AddTo(fields)
			encoded, encodeErr := json.Marshal(fields.Fields)
			if encodeErr != nil {
				t.Fatal("could not encode diagnostic fields")
			}
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), string(encoded)} {
				if strings.Contains(rendered, canary) {
					t.Error("configuration diagnostic exposed the synthetic secret")
				}
				if !strings.Contains(rendered, "invalid settings.otlp") {
					t.Error("configuration diagnostic lost its subsystem")
				}
			}
		})
	}
}

// TestOTLPConfigDiagnosticPreservesDecoderCause verifies typed error inspection remains available without logging the cause.
func TestOTLPConfigDiagnosticPreservesDecoderCause(t *testing.T) {
	_, err := controller.ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "idle_timeout": "synthetic-invalid-duration"})
	if err == nil {
		t.Fatal("malformed duration was accepted")
	}
	var decodeError *mapstructure.DecodeError
	if !errors.As(err, &decodeError) {
		t.Fatal("decoder error type was lost")
	}
	cause := errors.Unwrap(err)
	if cause == nil || !errors.Is(err, cause) {
		t.Fatal("decoder error identity was lost")
	}
}

// TestOTLPConfigPrivacyPreservesValidBehavior checks disabled, nil, valid duration and default-value behavior.
func TestOTLPConfigPrivacyPreservesValidBehavior(t *testing.T) {
	for _, raw := range []interface{}{nil, map[string]interface{}{"enabled": false}} {
		cfg, err := controller.ParseOTLPServiceConfig(raw)
		if cfg != nil || err != nil {
			t.Fatal("absent or disabled configuration behavior changed")
		}
	}
	cfg, err := controller.ParseOTLPServiceConfig(map[string]interface{}{
		"enabled": true, "idle_timeout": "75ms", "max_connections": float64(17),
		"destinations": []interface{}{map[string]interface{}{"id": "test", "timeout": "125ms", "logs_endpoint": "http://example.invalid/v1/logs"}},
	})
	if err != nil || cfg == nil {
		t.Fatal("valid configuration was rejected")
	}
	if !cfg.Enabled || !cfg.ReceiptGC || cfg.IdleTimeout != 75*time.Millisecond || cfg.MaxConnections != 17 || len(cfg.Destinations) != 1 || cfg.Destinations[0].Timeout != 125*time.Millisecond || cfg.Destinations[0].LogsEndpoint != "http://example.invalid/v1/logs" {
		t.Fatal("valid configuration values changed")
	}
}
