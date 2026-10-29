package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

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
