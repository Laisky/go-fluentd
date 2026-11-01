package controller

import (
	"context"
	"strings"
	"testing"

	"gofluentd/internal/alerts"

	"github.com/spf13/viper"
)

func TestTelegramAlertTagsExpandEnv(t *testing.T) {
	t.Setenv("SYNTHETIC_ALERT_TOKEN", "123:synthetic")
	t.Setenv("SYNTHETIC_ALERT_CHAT", "synthetic")
	v := viper.New()
	v.Set("env", "sit")
	v.Set("settings.telegram_alert.enabled", true)
	v.Set("settings.telegram_alert.tags", []string{"app.{env}", "literal"})
	v.Set("settings.telegram_alert.token_env", "SYNTHETIC_ALERT_TOKEN")
	v.Set("settings.telegram_alert.chat_id_env", "SYNTHETIC_ALERT_CHAT")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o, err := newConfiguredTelegramObserver(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	for _, tc := range []struct {
		tag      string
		selected bool
	}{{"app.sit", true}, {"literal", true}, {"app.{env}", false}, {"app.prod", false}} {
		before := o.Stats().Queued
		o.ObserveFluent(tc.tag, map[string]interface{}{"level": "error", "msg": "failure in " + tc.tag})
		if got := o.Stats().Queued > before; got != tc.selected {
			t.Errorf("tag %q: selected=%v, want %v", tc.tag, got, tc.selected)
		}
	}
}

func TestAlertMonitorSeparatesLossReasons(t *testing.T) {
	fields := alertMonitorFields(alerts.Stats{Dropped: 3, RequestTooLarge: 1, RequestQueueFull: 2, SourceLimited: 4})
	if fields["dropped"] != uint64(3) || fields["requestTooLarge"] != uint64(1) || fields["requestQueueFull"] != uint64(2) || fields["sourceLimited"] != uint64(4) {
		t.Fatalf("monitor fields: %v", fields)
	}
}

func TestTelegramAlertConfigurationOptIn(t *testing.T) {
	for _, config := range []string{`{"settings":{"logger":{"push_api":"https://example.invalid"}}}`, `{"settings":{"telegram_alert":{"enabled":false,"token_env":"MISSING"}}}`} {
		v := viper.New()
		v.SetConfigType("json")
		if err := v.ReadConfig(strings.NewReader(config)); err != nil {
			t.Fatal(err)
		}
		o, err := newConfiguredTelegramObserver(context.Background(), v)
		if err != nil || o != nil {
			t.Fatal("old or disabled configuration started observer")
		}
	}
	v := viper.New()
	v.Set("settings.telegram_alert.enabled", true)
	if _, err := newConfiguredTelegramObserver(context.Background(), v); err == nil {
		t.Fatal("enabled without source accepted")
	}
	v.Set("settings.telegram_alert.tags", []string{"app"})
	v.Set("settings.telegram_alert.token_env", "SYNTHETIC_ALERT_TOKEN")
	v.Set("settings.telegram_alert.chat_id_env", "SYNTHETIC_ALERT_CHAT")
	t.Setenv("SYNTHETIC_ALERT_TOKEN", "123:synthetic")
	t.Setenv("SYNTHETIC_ALERT_CHAT", "synthetic")
	ctx, cancel := context.WithCancel(context.Background())
	o, err := newConfiguredTelegramObserver(ctx, v)
	if err != nil || o == nil {
		t.Fatalf("valid opt-in: %v", err)
	}
	cancel()
	o.Wait()
}
