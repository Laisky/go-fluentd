package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gofluentd/internal/global"
	"gofluentd/library/log"

	gutils "github.com/Laisky/go-utils"
)

const (
	jsonStartupChild     = "GOFLUENTD_JSON_STARTUP_CHILD"
	jsonPushTokenCanary  = "sentinel-json-push-token"
	jsonNestedCanary     = "sentinel-json-nested-secret"
	jsonOTLPCanary       = "sentinel-json-otlp-secret"
	jsonDecodeCanary     = "sentinel-json-invalid-bool"
	jsonFixtureAlertType = "fixture-json-startup"
)

// TestRegressionJSONStartupAndAlertKeepSecretsPrivate exercises the production
// JSON configuration boundary and real opt-in SDK serialization using loopback.
func TestRegressionJSONStartupAndAlertKeepSecretsPrivate(t *testing.T) {
	if mode := os.Getenv(jsonStartupChild); mode != "" {
		runJSONStartupChild(t, mode)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot locate startup fixture subprocess")
	}
	for _, scenario := range []string{"valid", "invalid", "alert"} {
		for _, level := range []string{"info", "debug"} {
			mode := scenario + ":" + level
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, executable, "-test.run=^TestRegressionJSONStartupAndAlertKeepSecretsPrivate$")
				child.Env = append(os.Environ(), jsonStartupChild+"="+mode)
				output, err := child.CombinedOutput()
				if err != nil {
					t.Fatalf("JSON startup subprocess failed: %v\n%s", err, output)
				}
				for _, canary := range []string{jsonPushTokenCanary, jsonNestedCanary, jsonOTLPCanary, jsonDecodeCanary} {
					if strings.Contains(string(output), canary) {
						t.Error("JSON startup exposed a synthetic credential")
					}
				}
				if !strings.Contains(string(output), "success load configuration") {
					t.Error("missing useful configuration-load diagnostic")
				}
				expected := "JSON_STARTUP_VALIDATED"
				if scenario == "invalid" {
					expected = "JSON_DECODE_REJECTED"
					if !strings.Contains(string(output), "invalid configuration structure") {
						t.Error("missing fixed decoder-failure diagnostic")
					}
				}
				if scenario == "alert" {
					expected = "JSON_ALERT_PAYLOAD_VALIDATED"
				}
				if !strings.Contains(string(output), expected) {
					t.Error("startup fixture did not reach its positive control")
				}
			})
		}
	}
}

// runJSONStartupChild keeps each startup mode's flags, globals and hooks isolated.
func runJSONStartupChild(t *testing.T, mode string) {
	parts := strings.Split(mode, ":")
	if len(parts) != 2 || (parts[1] != "info" && parts[1] != "debug") {
		t.Fatal("invalid JSON startup fixture mode")
	}
	scenario, level := parts[0], parts[1]
	if scenario != "valid" && scenario != "invalid" && scenario != "alert" {
		t.Fatal("unknown JSON startup fixture scenario")
	}

	requests := make(chan map[string]string, 2)
	failures := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			failures <- "unexpected alert request method"
			http.Error(w, "fixture rejected", http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&payload); err != nil {
			failures <- "invalid alert request body"
			http.Error(w, "fixture rejected", http.StatusBadRequest)
			return
		}
		if !strings.Contains(payload.Query, "TelegramMonitorAlert") {
			failures <- "unexpected alert mutation"
			http.Error(w, "fixture rejected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{\"data\":{\"TelegramMonitorAlert\":{\"name\":\"fixture-json-startup\"}}}")
		requests <- payload.Variables
	}))
	defer server.Close()

	compress := any(false)
	if scenario == "invalid" {
		compress = jsonDecodeCanary
	}
	fixture := map[string]any{
		"settings": map[string]any{
			"logger": map[string]any{
				"push_api": server.URL, "alert_type": jsonFixtureAlertType, "push_token": jsonPushTokenCanary,
			},
			"journal": map[string]any{"buf_dir_path": filepath.Join(t.TempDir(), "legacy"), "is_compress": compress},
			"otlp":    map[string]any{"enabled": true, "bearer_token": jsonOTLPCanary},
			"acceptor": map[string]any{"recvs": map[string]any{"plugins": map[string]any{
				"custom": map[string]any{"arbitrary_unknown_key": map[string]any{"nested": jsonNestedCanary}},
			}}},
		},
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal("cannot encode synthetic JSON configuration")
	}
	config := filepath.Join(t.TempDir(), "edge.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal("cannot write private synthetic JSON configuration")
	}
	flags := []string{"--config=" + config, "--log-level=" + level}
	if level == "debug" {
		flags = append(flags, "--debug")
	}
	if scenario == "alert" {
		flags = append(flags, "--log-alert")
	}
	if err := rootCmd.ParseFlags(flags); err != nil {
		t.Fatal("actual startup flags were not accepted")
	}
	if err := gutils.Settings.BindPFlags(rootCmd.Flags()); err != nil {
		t.Fatal("cannot bind actual startup flags")
	}

	if scenario == "invalid" {
		defer func() {
			if recover() == nil {
				t.Error("invalid local decoder fixture was accepted")
				return
			}
			if gutils.Settings.GetString("settings.logger.push_token") != jsonPushTokenCanary {
				t.Error("invalid fixture was not loaded before decoding")
				return
			}
			fmt.Println("JSON_DECODE_REJECTED")
		}()
		setupSettings()
		return
	}

	setupSettings()
	if global.Config.Settings.Logger.PushAPI != server.URL ||
		global.Config.Settings.Logger.AlertType != jsonFixtureAlertType ||
		global.Config.Settings.Logger.PushToken != jsonPushTokenCanary ||
		gutils.Settings.GetString("settings.otlp.bearer_token") != jsonOTLPCanary ||
		gutils.Settings.GetString("settings.acceptor.recvs.plugins.custom.arbitrary_unknown_key.nested") != jsonNestedCanary {
		t.Fatal("JSON configuration fields were silently ignored or changed")
	}
	if global.Config.CMDArgs.EnableLogAlert != (scenario == "alert") {
		t.Fatal("actual log-alert flag binding changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setupLogger(ctx)

	if scenario == "valid" {
		log.Logger.Error("synthetic disabled-hook positive control")
		select {
		case <-requests:
			t.Fatal("alert hook sent despite absent opt-in flag")
		case <-failures:
			t.Fatal("disabled alert hook attempted a request")
		case <-time.After(100 * time.Millisecond):
		}
		fmt.Println("JSON_STARTUP_VALIDATED")
		return
	}

	log.Logger.Error("synthetic JSON alert positive control")
	select {
	case variables := <-requests:
		if variables["type"] != jsonFixtureAlertType || variables["token"] != jsonPushTokenCanary ||
			!strings.Contains(variables["msg"], "synthetic JSON alert positive control") {
			t.Fatal("SDK changed the synthetic alert payload")
		}
		if len(variables) != 3 {
			t.Fatal("SDK added unexpected alert fields")
		}
		fmt.Println("JSON_ALERT_PAYLOAD_VALIDATED")
	case <-failures:
		t.Fatal("local alert server rejected the SDK request")
	case <-time.After(5 * time.Second):
		t.Fatal("opt-in SDK hook did not deliver the local positive control")
	}
}
