// Package config читает настройки бота из окружения.
package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Config — настройки процесса.
type Config struct {
	BotToken     string        // BOT_TOKEN, обязателен
	ChromePath   string        // CHROME_PATH, по умолчанию "/usr/bin/chromium"
	FetchTimeout time.Duration // FETCH_TIMEOUT, по умолчанию 60s
	LogLevel     string        // LOG_LEVEL, по умолчанию "info"
	// TelegramIPs — TELEGRAM_IPS, список адресов api.telegram.org через запятую.
	// Нужен там, где DNS отдаёт заблокированные адреса; пусто — обычное
	// разрешение имени.
	TelegramIPs []string
}

// Load собирает конфигурацию из переменных окружения.
func Load() (*Config, error) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		return nil, errors.New("config: BOT_TOKEN is required")
	}

	cfg := &Config{
		BotToken:     token,
		ChromePath:   envOr("CHROME_PATH", "/usr/bin/chromium"),
		FetchTimeout: 60 * time.Second,
		LogLevel:     envOr("LOG_LEVEL", "info"),
		TelegramIPs:  parseList(os.Getenv("TELEGRAM_IPS")),
	}

	if raw := os.Getenv("FETCH_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("config: FETCH_TIMEOUT %q: %w", raw, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: FETCH_TIMEOUT %q must be positive", raw)
		}
		cfg.FetchTimeout = d
	}

	return cfg, nil
}

func parseList(raw string) []string {
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return slices.DeleteFunc(items, func(s string) bool { return s == "" })
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
