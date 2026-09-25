package controller_test

import (
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"gofluentd/internal/controller"
)

// Decode the same operator input using the real file-format parser. JSON
// numbers arrive as float64; YAML integer scalars arrive as integer types.
func TestRegressionOTLPJSONConfigurationMatchesYAML(t *testing.T) {
	text := `{"settings":{"otlp":{"enabled":true,"max_connections":17,"max_wire_bytes":65536,"max_wal_bytes":268435456,"replay_batch":16,"idle_timeout":"75ms","destinations":[{"id":"sink","max_attempts":3}]}}}`
	var configs []*controller.OTLPServiceConfig
	for _, format := range []string{"yaml", "json"} {
		v := viper.New()
		v.SetConfigType(format)
		if err := v.ReadConfig(strings.NewReader(text)); err != nil {
			t.Fatal(err)
		}
		cfg, err := controller.ParseOTLPServiceConfig(v.Get("settings.otlp"))
		if err != nil {
			t.Fatalf("valid %s configuration rejected: %v", format, err)
		}
		configs = append(configs, cfg)
	}
	if !reflect.DeepEqual(configs[0], configs[1]) {
		t.Fatalf("JSON/YAML configuration differs: %+v / %+v", configs[0], configs[1])
	}
	cfg := configs[1]
	if cfg.MaxConnections != 17 || cfg.MaxWireBytes != 65536 || cfg.MaxWALBytes != 268435456 || cfg.ReplayBatch != 16 || cfg.Destinations[0].MaxAttempts != 3 {
		t.Fatalf("integer configuration changed: %+v", cfg)
	}
}

func TestOTLPConfigurationRejectsUnsafeNumericConversions(t *testing.T) {
	for name, value := range map[string]interface{}{
		"fraction": 1.5, "negative-fraction": -1.5, "nan": math.NaN(),
		"infinity": math.Inf(1), "negative-infinity": math.Inf(-1),
		"rounded-float64": float64(1 << 53), "negative-rounded-float64": -float64(1 << 53),
		"rounded-float32": float32(1 << 24), "overflow": float64(1 << 63),
		"numeric-string": "17", "boolean": true,
	} {
		t.Run(name, func(t *testing.T) {
			raw := map[string]interface{}{"enabled": true, "max_wal_bytes": value}
			if _, err := controller.ParseOTLPServiceConfig(raw); err == nil {
				t.Fatal("unsafe numeric conversion accepted")
			}
		})
	}
	for _, value := range []interface{}{float64((1 << 53) - 1), float32((1 << 24) - 1)} {
		cfg, err := controller.ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "max_wal_bytes": value})
		if err != nil || cfg == nil {
			t.Fatalf("safe integer rejected: %v", err)
		}
	}
	if _, err := controller.ParseOTLPServiceConfig(map[string]interface{}{"enabled": true, "idle_timeout": float64(75)}); err == nil {
		t.Fatal("unitless duration accepted")
	}
}
