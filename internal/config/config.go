package config

import (
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

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  envOr("OMNIFLOW_HTTP_ADDR", ":8080"),
		DatabaseURL:               os.Getenv("OMNIFLOW_DATABASE_URL"),
		ValkeyURL:                 os.Getenv("OMNIFLOW_VALKEY_URL"),
		TelemetryEnabled:          boolEnvOr("OMNIFLOW_TELEMETRY_ENABLED", true),
		TelemetryCollectorEnabled: boolEnvOr("OMNIFLOW_TELEMETRY_COLLECTOR_ENABLED", false),
		TelemetryEndpoint:         envOr("OMNIFLOW_TELEMETRY_ENDPOINT", defaultTelemetryEndpoint),
	}

	if cfg.TelemetryEnabled {
		parsed, err := url.ParseRequestURI(cfg.TelemetryEndpoint)
		if err != nil || parsed.Scheme != "https" {
			return Config{}, fmt.Errorf("OMNIFLOW_TELEMETRY_ENDPOINT must be an HTTPS URL")
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
