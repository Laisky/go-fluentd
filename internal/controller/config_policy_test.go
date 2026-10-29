package controller

import (
	"context"
	"github.com/spf13/viper"
	"testing"
)

func TestTelegramAlertPolicyValidation(t *testing.T) {
	t.Setenv("SYNTHETIC_ALERT_TOKEN", "123:synthetic")
	t.Setenv("SYNTHETIC_ALERT_CHAT", "synthetic")
	base := func() *viper.Viper {
		v := viper.New()
		v.Set("settings.telegram_alert.enabled", true)
		v.Set("settings.telegram_alert.tags", []string{"app"})
		v.Set("settings.telegram_alert.token_env", "SYNTHETIC_ALERT_TOKEN")
		v.Set("settings.telegram_alert.chat_id_env", "SYNTHETIC_ALERT_CHAT")
		return v
	}
	for _, tc := range []struct {
		key   string
		value interface{}
	}{{"queue_size", 0}, {"queue_size", 4097}, {"otlp_queue_size", 33}, {"dedup_entries", 16385}, {"max_attempts", 6}, {"send_interval", "1ms"}, {"dedup_window", "bad"}, {"queue_size", "not-a-number"}, {"queue_szie", 10}} {
		v := base()
		v.Set("settings.telegram_alert."+tc.key, tc.value)
		if _, err := newConfiguredTelegramObserver(context.Background(), v); err == nil {
			t.Errorf("accepted %s", tc.key)
		}
	}
	v := base()
	v.Set("settings.telegram_alert.queue_size", 32)
	v.Set("settings.telegram_alert.otlp_queue_size", 2)
	v.Set("settings.telegram_alert.dedup_entries", 64)
	v.Set("settings.telegram_alert.dedup_window", "30s")
	v.Set("settings.telegram_alert.send_interval", "200ms")
	v.Set("settings.telegram_alert.max_attempts", 2)
	ctx, cancel := context.WithCancel(context.Background())
	o, err := newConfiguredTelegramObserver(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	o.Wait()
}
