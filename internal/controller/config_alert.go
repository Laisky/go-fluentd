package controller

import (
	"context"
	"errors"
	"os"

	"gofluentd/internal/alerts"
)

type alertSettings interface {
	GetBool(string) bool
	GetString(string) string
	GetStringSlice(string) []string
}

func newConfiguredTelegramObserver(ctx context.Context, settings alertSettings) (*alerts.Observer, error) {
	if !settings.GetBool("settings.telegram_alert.enabled") {
		return nil, nil
	}
	tags := settings.GetStringSlice("settings.telegram_alert.tags")
	services := settings.GetStringSlice("settings.telegram_alert.services")
	if len(tags) == 0 && len(services) == 0 {
		return nil, errors.New("telegram alert requires explicit tags or services")
	}
	tokenEnv := settings.GetString("settings.telegram_alert.token_env")
	chatEnv := settings.GetString("settings.telegram_alert.chat_id_env")
	return alerts.New(ctx, alerts.Config{Token: os.Getenv(tokenEnv), ChatID: os.Getenv(chatEnv), Tags: tags, Services: services, IncludeMessage: settings.GetBool("settings.telegram_alert.include_message")}, "", nil)
}
