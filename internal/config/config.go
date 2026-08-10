package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
)

const defaultTelemetryEndpoint = "https://telemetry.omniflw.xyz/v1/telemetry/events"

type Config struct {
	HTTPAddr                  string
	DatabaseURL               string
	ValkeyURL                 string
	TelemetryEnabled          bool
	TelemetryCollectorEnabled bool
	TelemetryEndpoint         string
}

type BotConfig struct {
	DatabaseURL    string
	RemnawaveURL   string
	RemnawaveToken string
	TelegramToken  string
	SupportURL     string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  envOr("APP_HTTP_ADDR", ":8080"),
		DatabaseURL:               os.Getenv("APP_DATABASE_URL"),
		ValkeyURL:                 os.Getenv("APP_VALKEY_URL"),
		TelemetryEnabled:          boolEnvOr("APP_TELEMETRY_ENABLED", true),
		TelemetryCollectorEnabled: boolEnvOr("APP_TELEMETRY_COLLECTOR_ENABLED", false),
		TelemetryEndpoint:         envOr("APP_TELEMETRY_ENDPOINT", defaultTelemetryEndpoint),
	}

	if cfg.TelemetryEnabled {
		parsed, err := url.ParseRequestURI(cfg.TelemetryEndpoint)
		if err != nil || parsed.Scheme != "https" {
			return Config{}, fmt.Errorf("APP_TELEMETRY_ENDPOINT must be an HTTPS URL")
		}
	}
	return cfg, nil
}

func LoadBot() (BotConfig, error) {
	cfg := BotConfig{
		DatabaseURL:    os.Getenv("APP_DATABASE_URL"),
		RemnawaveURL:   os.Getenv("APP_REMNAWAVE_URL"),
		RemnawaveToken: os.Getenv("APP_REMNAWAVE_TOKEN"),
		TelegramToken:  os.Getenv("APP_TELEGRAM_TOKEN"),
		SupportURL:     os.Getenv("APP_SUPPORT_URL"),
	}
	if cfg.DatabaseURL == "" {
		return BotConfig{}, errors.New("APP_DATABASE_URL is required")
	}
	if cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" {
		return BotConfig{}, errors.New("APP_REMNAWAVE_URL and APP_REMNAWAVE_TOKEN are required")
	}
	if cfg.TelegramToken == "" {
		return BotConfig{}, errors.New("APP_TELEGRAM_TOKEN is required")
	}
	if cfg.SupportURL != "" {
		parsed, err := url.ParseRequestURI(cfg.SupportURL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return BotConfig{}, errors.New("APP_SUPPORT_URL must be an HTTP(S) URL")
		}
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func boolEnvOr(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
