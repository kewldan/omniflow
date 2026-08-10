package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

const defaultTelemetryEndpoint = "https://telemetry.omniflw.xyz/v1/telemetry/events"

type Config struct {
	HTTPAddr                  string
	DatabaseURL               string
	ValkeyURL                 string
	TelemetryEnabled          bool
	TelemetryCollectorEnabled bool
	TelemetryEndpoint         string
	OperatorToken             string
	DataEncryptionKey         []byte
	RemnawaveURL              string
	RemnawaveToken            string
	TelegramToken             string
	CryptoBotToken            string
	CryptoBotTestnet          bool
	YooKassaShopID            string
	YooKassaSecret            string
}

type BotConfig struct {
	DatabaseURL      string
	ValkeyURL        string
	RemnawaveURL     string
	RemnawaveToken   string
	TelegramToken    string
	SupportURL       string
	TermsURL         string
	PublicURL        string
	DefaultCurrency  string
	CryptoBotToken   string
	CryptoBotTestnet bool
	YooKassaShopID   string
	YooKassaSecret   string
	// MarketingFrequencyCap bounds marketing messages per MarketingWindow. Zero
	// disables the cap.
	MarketingFrequencyCap int
	MarketingWindow       time.Duration
	// RecoveryWindow is how long an expired subscription keeps offering a
	// one-tap recovery checkout.
	RecoveryWindow time.Duration
	// MinimumTrialAccountAge is the abuse control for freshly created accounts.
	MinimumTrialAccountAge time.Duration
}

type WorkerConfig struct {
	DatabaseURL    string
	RemnawaveURL   string
	RemnawaveToken string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  envOr("APP_HTTP_ADDR", ":8080"),
		DatabaseURL:               os.Getenv("APP_DATABASE_URL"),
		ValkeyURL:                 os.Getenv("APP_VALKEY_URL"),
		TelemetryEnabled:          boolEnvOr("APP_TELEMETRY_ENABLED", true),
		TelemetryCollectorEnabled: boolEnvOr("APP_TELEMETRY_COLLECTOR_ENABLED", false),
		TelemetryEndpoint:         envOr("APP_TELEMETRY_ENDPOINT", defaultTelemetryEndpoint),
		OperatorToken:             os.Getenv("APP_OPERATOR_TOKEN"),
		RemnawaveURL:              os.Getenv("APP_REMNAWAVE_URL"),
		RemnawaveToken:            os.Getenv("APP_REMNAWAVE_TOKEN"),
		TelegramToken:             os.Getenv("APP_TELEGRAM_TOKEN"),
		CryptoBotToken:            os.Getenv("APP_CRYPTOBOT_TOKEN"),
		CryptoBotTestnet:          boolEnvOr("APP_CRYPTOBOT_TESTNET", true),
		YooKassaShopID:            os.Getenv("APP_YOOKASSA_SHOP_ID"),
		YooKassaSecret:            os.Getenv("APP_YOOKASSA_SECRET"),
	}
	if encodedKey := os.Getenv("APP_DATA_ENCRYPTION_KEY"); encodedKey != "" {
		key, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil || len(key) != 32 {
			return Config{}, errors.New("APP_DATA_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
		cfg.DataEncryptionKey = key
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
		DatabaseURL:            os.Getenv("APP_DATABASE_URL"),
		ValkeyURL:              os.Getenv("APP_VALKEY_URL"),
		RemnawaveURL:           os.Getenv("APP_REMNAWAVE_URL"),
		RemnawaveToken:         os.Getenv("APP_REMNAWAVE_TOKEN"),
		TelegramToken:          os.Getenv("APP_TELEGRAM_TOKEN"),
		SupportURL:             os.Getenv("APP_SUPPORT_URL"),
		TermsURL:               os.Getenv("APP_TERMS_URL"),
		PublicURL:              os.Getenv("APP_PUBLIC_URL"),
		DefaultCurrency:        strings.ToUpper(envOr("APP_DEFAULT_CURRENCY", "RUB")),
		CryptoBotToken:         os.Getenv("APP_CRYPTOBOT_TOKEN"),
		CryptoBotTestnet:       boolEnvOr("APP_CRYPTOBOT_TESTNET", true),
		YooKassaShopID:         os.Getenv("APP_YOOKASSA_SHOP_ID"),
		YooKassaSecret:         os.Getenv("APP_YOOKASSA_SECRET"),
		MarketingFrequencyCap:  intEnvOr("APP_MARKETING_FREQUENCY_CAP", 3),
		MarketingWindow:        durationEnvOr("APP_MARKETING_WINDOW_HOURS", 7*24*time.Hour),
		RecoveryWindow:         durationEnvOr("APP_RECOVERY_WINDOW_HOURS", 14*24*time.Hour),
		MinimumTrialAccountAge: durationEnvOr("APP_TRIAL_MINIMUM_ACCOUNT_AGE_HOURS", 0),
	}
	if cfg.DatabaseURL == "" {
		return BotConfig{}, errors.New("APP_DATABASE_URL is required")
	}
	if cfg.ValkeyURL == "" {
		return BotConfig{}, errors.New("APP_VALKEY_URL is required for bot rate limits and callback replay protection")
	}
	if cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" {
		return BotConfig{}, errors.New("APP_REMNAWAVE_URL and APP_REMNAWAVE_TOKEN are required")
	}
	if cfg.TelegramToken == "" {
		return BotConfig{}, errors.New("APP_TELEGRAM_TOKEN is required")
	}
	if !currencyPattern.MatchString(cfg.DefaultCurrency) {
		return BotConfig{}, errors.New("APP_DEFAULT_CURRENCY must be a three-letter ISO currency code")
	}
	for name, value := range map[string]string{"APP_SUPPORT_URL": cfg.SupportURL, "APP_TERMS_URL": cfg.TermsURL, "APP_PUBLIC_URL": cfg.PublicURL} {
		if value == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(value)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return BotConfig{}, errors.New(name + " must be an HTTP(S) URL")
		}
	}
	if (cfg.YooKassaShopID != "") != (cfg.YooKassaSecret != "") {
		return BotConfig{}, errors.New("APP_YOOKASSA_SHOP_ID and APP_YOOKASSA_SECRET must be configured together")
	}
	if cfg.YooKassaShopID != "" && cfg.PublicURL == "" {
		return BotConfig{}, errors.New("APP_PUBLIC_URL is required for YooKassa hosted checkout return links")
	}
	return cfg, nil
}

func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{DatabaseURL: os.Getenv("APP_DATABASE_URL"), RemnawaveURL: os.Getenv("APP_REMNAWAVE_URL"), RemnawaveToken: os.Getenv("APP_REMNAWAVE_TOKEN")}
	if cfg.DatabaseURL == "" || cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" {
		return WorkerConfig{}, errors.New("APP_DATABASE_URL, APP_REMNAWAVE_URL, and APP_REMNAWAVE_TOKEN are required")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnvOr(key string, fallback int) int {
	parsed, err := strconv.Atoi(os.Getenv(key))
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

// durationEnvOr reads a whole number of hours so operators never have to encode
// a Go duration string in the environment.
func durationEnvOr(key string, fallback time.Duration) time.Duration {
	parsed, err := strconv.Atoi(os.Getenv(key))
	if err != nil || parsed < 0 {
		return fallback
	}
	return time.Duration(parsed) * time.Hour
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
