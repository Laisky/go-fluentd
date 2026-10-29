package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"gofluentd/internal/alerts"
)

type alertSettings interface {
	Get(string) interface{}
	GetBool(string) bool
	GetString(string) string
	GetStringSlice(string) []string
}

func newConfiguredTelegramObserver(ctx context.Context, settings alertSettings) (*alerts.Observer, error) {
	if !settings.GetBool("settings.telegram_alert.enabled") {
		return nil, nil
	}
	if values, ok := settings.Get("settings.telegram_alert").(map[string]interface{}); ok {
		allowed := map[string]bool{"enabled": true, "token_env": true, "chat_id_env": true, "tags": true, "services": true, "include_message": true, "queue_size": true, "otlp_queue_size": true, "dedup_entries": true, "dedup_window": true, "send_interval": true, "max_attempts": true}
		for key := range values {
			if !allowed[key] {
				return nil, errors.New("invalid Telegram alert configuration field")
			}
		}
	}
	tags := settings.GetStringSlice("settings.telegram_alert.tags")
	services := settings.GetStringSlice("settings.telegram_alert.services")
	if len(tags) == 0 && len(services) == 0 {
		return nil, errors.New("telegram alert requires explicit tags or services")
	}
	tokenEnv := settings.GetString("settings.telegram_alert.token_env")
	chatEnv := settings.GetString("settings.telegram_alert.chat_id_env")
	cfg := alerts.Config{Token: os.Getenv(tokenEnv), ChatID: os.Getenv(chatEnv), Tags: tags, Services: services, IncludeMessage: settings.GetBool("settings.telegram_alert.include_message")}
	for _, option := range []struct {
		key    string
		target *int
	}{{"queue_size", &cfg.EventQueueSize}, {"otlp_queue_size", &cfg.OTLPQueueSize}, {"dedup_entries", &cfg.DedupEntries}, {"max_attempts", &cfg.MaxAttempts}} {
		if v := settings.Get("settings.telegram_alert." + option.key); v != nil {
			n, e := strconv.Atoi(fmt.Sprint(v))
			if e != nil || n <= 0 {
				return nil, errors.New("invalid Telegram alert policy limits")
			}
			*option.target = n
		}
	}
	for _, option := range []struct {
		key    string
		target *time.Duration
	}{{"dedup_window", &cfg.DedupWindow}, {"send_interval", &cfg.SendInterval}} {
		if v := settings.Get("settings.telegram_alert." + option.key); v != nil {
			text, ok := v.(string)
			if !ok {
				return nil, errors.New("invalid Telegram alert policy limits")
			}
			d, e := time.ParseDuration(text)
			if e != nil || d <= 0 {
				return nil, errors.New("invalid Telegram alert policy limits")
			}
			*option.target = d
		}
	}
	return alerts.New(ctx, cfg, "", nil)
}
