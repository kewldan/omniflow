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

	"github.com/omniflow/omniflow/internal/commerce"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

const defaultTelemetryEndpoint = "https://telemetry.omniflw.xyz/v1/telemetry/events"

// TopUpConfig is the operator's wallet top-up policy. Until the admin panel
// exposes it in v0.7 it is configured entirely from the environment.
type TopUpConfig struct {
	Enabled          bool
	Presets          []int64
	MinimumMinor     int64
	MaximumMinor     int64
	WindowLimitMinor int64
	Window           time.Duration
}

// Limits converts the environment-configured top-up policy into the domain
// value the commerce store enforces.
func (cfg TopUpConfig) Limits() commerce.TopUpLimits {
	return commerce.TopUpLimits{
		Enabled: cfg.Enabled, Presets: cfg.Presets, MinimumMinor: cfg.MinimumMinor,
		MaximumMinor: cfg.MaximumMinor, WindowLimitMinor: cfg.WindowLimitMinor, Window: cfg.Window,
	}
}

// SubscriptionConfig switches concurrent subscriptions on and bounds them.
// The default keeps one subscription per customer, which is what every
// installation upgraded from v0.4 already has.
type SubscriptionConfig struct {
	MultiEnabled   bool
	MaxPerCustomer int
}

// Policy converts the environment-configured concurrency settings into the
// domain policy the commerce store enforces.
func (cfg SubscriptionConfig) Policy() commerce.SubscriptionPolicy {
	return commerce.SubscriptionPolicy{MultiEnabled: cfg.MultiEnabled, MaxPerCustomer: cfg.MaxPerCustomer}
}

// MaintenanceConfig controls automatic dependency detection. Manual activation
// always works regardless of these values.
type MaintenanceConfig struct {
	AutoDetect     bool
	ProbeInterval  time.Duration
	FailureStreak  int
	RecoveryStreak int
}

// BackupConfig describes scheduled PostgreSQL backups. Backups are written to
// Directory, encrypted with EncryptionKey, and pruned once Retention elapses.
type BackupConfig struct {
	Enabled       bool
	Directory     string
	Interval      time.Duration
	Retention     time.Duration
	EncryptionKey []byte
	PgDumpPath    string
	PgRestorePath string
}

// RetentionConfig bounds how long disposable operational data is kept.
type RetentionConfig struct {
	Outbox    time.Duration
	Telemetry time.Duration
	Drift     time.Duration
	Interval  time.Duration
}

// OperatorConfig binds the Telegram group that receives operator notifications
// and bounds how many messages one topic may receive in a window.
type OperatorConfig struct {
	ChatID          int64
	NotificationCap int
	Window          time.Duration
	// OperatorIDs are the Telegram accounts allowed to run backup and restore
	// actions from the bot.
	OperatorIDs []int64
}

// TelegramWebhookConfig selects how the bot receives updates. Long polling is
// the default and stays supported as an explicit development and fallback mode.
type TelegramWebhookConfig struct {
	Enabled     bool
	URL         string
	SecretToken string
	ListenAddr  string
	// MetricsAddr serves the bot's liveness, readiness, and metrics endpoints.
	// It is empty by default, which keeps the bot off HTTP entirely.
	MetricsAddr string
}

type Config struct {
	HTTPAddr                  string
	DatabaseURL               string
	ValkeyURL                 string
	TelemetryEnabled          bool
	TelemetryCollectorEnabled bool
	TelemetryEndpoint         string
	MetricsEnabled            bool
	OperatorToken             string
	DataEncryptionKey         []byte
	RemnawaveURL              string
	RemnawaveToken            string
	TelegramToken             string
	CryptoBotToken            string
	CryptoBotTestnet          bool
	YooKassaShopID            string
	YooKassaSecret            string
	DefaultCurrency           string
	TopUp                     TopUpConfig
	Subscriptions             SubscriptionConfig
	Maintenance               MaintenanceConfig
	AdminPanel                AdminPanelConfig
	CustomerPanel             CustomerPanelConfig
	PublicURL                 string
}

// CustomerPanelConfig configures the customer web panel API introduced in v0.9.
type CustomerPanelConfig struct {
	// Enabled mounts /v1/account. It needs APP_DATA_ENCRYPTION_KEY, which seals
	// OIDC client secrets and the sign-in flow cookie, so a Telegram-only
	// installation can leave it off.
	Enabled bool
	// CookieSecure marks the session cookie Secure. It defaults to true and
	// should only be turned off for a plain-HTTP local stack, where a browser
	// would otherwise refuse the cookie and sign-in could never complete.
	CookieSecure bool
	// MagicLinkEnabled offers the bot-delivered sign-in link as a fallback for
	// installations where the Telegram login widget is not usable — typically
	// because the domain has not been bound in BotFather. It is off by default:
	// it is a second bearer credential travelling through a chat, and an
	// operator should switch it on deliberately.
	MagicLinkEnabled bool
}

// AdminPanelConfig configures the operator panel API introduced in v0.6.
type AdminPanelConfig struct {
	// Enabled mounts /v1/panel. It requires APP_DATA_ENCRYPTION_KEY, which
	// seals TOTP secrets, so a bot-only installation can leave it off.
	Enabled bool
	// CookieSecure marks the session cookie Secure. It defaults to true and
	// should only be turned off for a plain-HTTP local stack, where a browser
	// would otherwise refuse the cookie and sign-in could never complete.
	CookieSecure bool
	// TrustedProxies lists the IPs or CIDR blocks whose X-Forwarded-For header
	// may be believed. Empty means the header is ignored entirely, which is
	// correct when the API is exposed directly.
	TrustedProxies []string
	// Issuer labels operator accounts inside an authenticator app.
	Issuer string
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
	// DataEncryptionKey unseals the digital-goods provider credential the shop
	// needs to quote a price. It is optional: an installation with no shop never
	// needs it, and the bot starts without the shop rather than refusing to
	// start at all.
	DataEncryptionKey []byte
	// MarketingFrequencyCap bounds marketing messages per MarketingWindow. Zero
	// disables the cap.
	MarketingFrequencyCap int
	MarketingWindow       time.Duration
	// RecoveryWindow is how long an expired subscription keeps offering a
	// one-tap recovery checkout.
	RecoveryWindow time.Duration
	// MinimumTrialAccountAge is the abuse control for freshly created accounts.
	MinimumTrialAccountAge time.Duration
	// CartTTL is how long a saved cart waits for the balance to cover it.
	CartTTL       time.Duration
	TopUp         TopUpConfig
	Subscriptions SubscriptionConfig
	Operator      OperatorConfig
	Backup        BackupConfig
	Webhook       TelegramWebhookConfig
	// CustomerPanel tells the bot whether to offer the web sign-in link. The bot
	// is the delivery channel for it, so it needs the same switch the API reads.
	CustomerPanel CustomerPanelConfig
}

type WorkerConfig struct {
	DatabaseURL    string
	ValkeyURL      string
	RemnawaveURL   string
	RemnawaveToken string
	MetricsAddr    string
	MetricsEnabled bool
	Maintenance    MaintenanceConfig
	Retention      RetentionConfig
	Backup         BackupConfig
	Subscriptions  SubscriptionConfig
	// DataEncryptionKey unseals the provider credentials the worker needs to
	// call an external service on the operator's behalf. It is the same key the
	// API uses; the worker holds it because it is the process that delivers
	// digital goods, not because it stores anything of its own.
	DataEncryptionKey []byte
	DefaultCurrency   string
	// Payment credentials. The worker holds them because it is the process that
	// charges automatic renewals: a renewal is a charge against a provider, and
	// a worker without the adapter could only ever record that it failed.
	TelegramToken    string
	CryptoBotToken   string
	CryptoBotTestnet bool
	YooKassaShopID   string
	YooKassaSecret   string
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  envOr("APP_HTTP_ADDR", ":8080"),
		DatabaseURL:               os.Getenv("APP_DATABASE_URL"),
		ValkeyURL:                 os.Getenv("APP_VALKEY_URL"),
		TelemetryEnabled:          boolEnvOr("APP_TELEMETRY_ENABLED", true),
		TelemetryCollectorEnabled: boolEnvOr("APP_TELEMETRY_COLLECTOR_ENABLED", false),
		TelemetryEndpoint:         envOr("APP_TELEMETRY_ENDPOINT", defaultTelemetryEndpoint),
		MetricsEnabled:            boolEnvOr("APP_METRICS_ENABLED", true),
		OperatorToken:             os.Getenv("APP_OPERATOR_TOKEN"),
		RemnawaveURL:              os.Getenv("APP_REMNAWAVE_URL"),
		RemnawaveToken:            os.Getenv("APP_REMNAWAVE_TOKEN"),
		TelegramToken:             os.Getenv("APP_TELEGRAM_TOKEN"),
		CryptoBotToken:            os.Getenv("APP_CRYPTOBOT_TOKEN"),
		CryptoBotTestnet:          boolEnvOr("APP_CRYPTOBOT_TESTNET", true),
		YooKassaShopID:            os.Getenv("APP_YOOKASSA_SHOP_ID"),
		YooKassaSecret:            os.Getenv("APP_YOOKASSA_SECRET"),
		DefaultCurrency:           strings.ToUpper(envOr("APP_DEFAULT_CURRENCY", "RUB")),
		Subscriptions:             loadSubscriptions(),
		Maintenance:               loadMaintenance(),
		AdminPanel:                loadAdminPanel(),
		CustomerPanel:             loadCustomerPanel(),
		PublicURL:                 os.Getenv("APP_PUBLIC_URL"),
	}
	if encodedKey := os.Getenv("APP_DATA_ENCRYPTION_KEY"); encodedKey != "" {
		key, err := decodeKey(encodedKey)
		if err != nil {
			return Config{}, errors.New("APP_DATA_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
		cfg.DataEncryptionKey = key
	}
	topUp, err := loadTopUp()
	if err != nil {
		return Config{}, err
	}
	cfg.TopUp = topUp
	if !currencyPattern.MatchString(cfg.DefaultCurrency) {
		return Config{}, errors.New("APP_DEFAULT_CURRENCY must be a three-letter ISO currency code")
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
		MarketingWindow:        hourEnvOr("APP_MARKETING_WINDOW_HOURS", 7*24*time.Hour),
		RecoveryWindow:         hourEnvOr("APP_RECOVERY_WINDOW_HOURS", 14*24*time.Hour),
		MinimumTrialAccountAge: hourEnvOr("APP_TRIAL_MINIMUM_ACCOUNT_AGE_HOURS", 0),
		CartTTL:                hourEnvOr("APP_CART_TTL_HOURS", 30*24*time.Hour),
		Subscriptions:          loadSubscriptions(),
	}
	if encodedKey := os.Getenv("APP_DATA_ENCRYPTION_KEY"); encodedKey != "" {
		key, err := decodeKey(encodedKey)
		if err != nil {
			return BotConfig{}, errors.New("APP_DATA_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
		cfg.DataEncryptionKey = key
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
	cfg.CustomerPanel = loadCustomerPanel()
	if cfg.CustomerPanel.MagicLinkEnabled && cfg.PublicURL == "" {
		return BotConfig{}, errors.New("APP_PUBLIC_URL is required when the customer magic-link fallback is enabled")
	}
	topUp, err := loadTopUp()
	if err != nil {
		return BotConfig{}, err
	}
	cfg.TopUp = topUp
	if cfg.Operator, err = loadOperator(); err != nil {
		return BotConfig{}, err
	}
	if cfg.Backup, err = loadBackup(); err != nil {
		return BotConfig{}, err
	}
	if cfg.Webhook, err = loadTelegramWebhook(); err != nil {
		return BotConfig{}, err
	}
	return cfg, nil
}

func LoadWorker() (WorkerConfig, error) {
	cfg := WorkerConfig{
		DatabaseURL:      os.Getenv("APP_DATABASE_URL"),
		ValkeyURL:        os.Getenv("APP_VALKEY_URL"),
		RemnawaveURL:     os.Getenv("APP_REMNAWAVE_URL"),
		RemnawaveToken:   os.Getenv("APP_REMNAWAVE_TOKEN"),
		MetricsAddr:      envOr("APP_WORKER_HTTP_ADDR", ":8081"),
		MetricsEnabled:   boolEnvOr("APP_METRICS_ENABLED", true),
		Maintenance:      loadMaintenance(),
		Subscriptions:    loadSubscriptions(),
		DefaultCurrency:  strings.ToUpper(envOr("APP_DEFAULT_CURRENCY", "RUB")),
		TelegramToken:    os.Getenv("APP_TELEGRAM_TOKEN"),
		CryptoBotToken:   os.Getenv("APP_CRYPTOBOT_TOKEN"),
		CryptoBotTestnet: boolEnvOr("APP_CRYPTOBOT_TESTNET", true),
		YooKassaShopID:   os.Getenv("APP_YOOKASSA_SHOP_ID"),
		YooKassaSecret:   os.Getenv("APP_YOOKASSA_SECRET"),
		Retention: RetentionConfig{
			Outbox:    dayEnvOr("APP_RETENTION_OUTBOX_DAYS", 7*24*time.Hour),
			Telemetry: dayEnvOr("APP_RETENTION_TELEMETRY_DAYS", 30*24*time.Hour),
			Drift:     dayEnvOr("APP_RETENTION_DRIFT_DAYS", 90*24*time.Hour),
			Interval:  hourEnvOr("APP_RETENTION_INTERVAL_HOURS", time.Hour),
		},
	}
	if cfg.DatabaseURL == "" || cfg.RemnawaveURL == "" || cfg.RemnawaveToken == "" {
		return WorkerConfig{}, errors.New("APP_DATABASE_URL, APP_REMNAWAVE_URL, and APP_REMNAWAVE_TOKEN are required")
	}
	// Optional: an installation with no digital-goods shop never needs it, and
	// requiring it would break every existing worker deployment.
	if encodedKey := os.Getenv("APP_DATA_ENCRYPTION_KEY"); encodedKey != "" {
		key, err := decodeKey(encodedKey)
		if err != nil {
			return WorkerConfig{}, errors.New("APP_DATA_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
		cfg.DataEncryptionKey = key
	}
	backup, err := loadBackup()
	if err != nil {
		return WorkerConfig{}, err
	}
	cfg.Backup = backup
	return cfg, nil
}

func loadTopUp() (TopUpConfig, error) {
	cfg := TopUpConfig{
		Enabled:          boolEnvOr("APP_WALLET_TOPUP_ENABLED", true),
		MinimumMinor:     int64EnvOr("APP_WALLET_TOPUP_MINIMUM_MINOR", 10000),
		MaximumMinor:     int64EnvOr("APP_WALLET_TOPUP_MAXIMUM_MINOR", 5000000),
		WindowLimitMinor: int64EnvOr("APP_WALLET_TOPUP_WINDOW_LIMIT_MINOR", 10000000),
		Window:           hourEnvOr("APP_WALLET_TOPUP_WINDOW_HOURS", 24*time.Hour),
	}
	presets, err := int64ListEnv("APP_WALLET_TOPUP_PRESETS", []int64{10000, 30000, 50000, 100000})
	if err != nil {
		return TopUpConfig{}, err
	}
	cfg.Presets = presets
	if cfg.MaximumMinor > 0 && cfg.MinimumMinor > cfg.MaximumMinor {
		return TopUpConfig{}, errors.New("APP_WALLET_TOPUP_MINIMUM_MINOR must not exceed APP_WALLET_TOPUP_MAXIMUM_MINOR")
	}
	return cfg, nil
}

func loadSubscriptions() SubscriptionConfig {
	cfg := SubscriptionConfig{
		MultiEnabled:   boolEnvOr("APP_MULTI_SUBSCRIPTION_ENABLED", false),
		MaxPerCustomer: intEnvOr("APP_MAX_SUBSCRIPTIONS_PER_CUSTOMER", 3),
	}
	if cfg.MaxPerCustomer < 1 {
		cfg.MaxPerCustomer = 1
	}
	return cfg
}

func loadMaintenance() MaintenanceConfig {
	return MaintenanceConfig{
		AutoDetect:     boolEnvOr("APP_MAINTENANCE_AUTO_DETECT", true),
		ProbeInterval:  secondEnvOr("APP_MAINTENANCE_PROBE_SECONDS", 30*time.Second),
		FailureStreak:  intEnvOr("APP_MAINTENANCE_FAILURE_STREAK", 3),
		RecoveryStreak: intEnvOr("APP_MAINTENANCE_RECOVERY_STREAK", 3),
	}
}

func loadAdminPanel() AdminPanelConfig {
	cfg := AdminPanelConfig{
		Enabled: boolEnvOr("APP_ADMIN_PANEL_ENABLED", true),
		// Defaults to true so a deployment that forgets to set it still gets a
		// cookie a browser will only send over TLS.
		CookieSecure: boolEnvOr("APP_ADMIN_COOKIE_SECURE", true),
		Issuer:       envOr("APP_ADMIN_TOTP_ISSUER", "Omniflow"),
	}
	for entry := range strings.SplitSeq(os.Getenv("APP_TRUSTED_PROXIES"), ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			cfg.TrustedProxies = append(cfg.TrustedProxies, trimmed)
		}
	}
	return cfg
}

// loadCustomerPanel reads the v0.9 customer web settings.
func loadCustomerPanel() CustomerPanelConfig {
	return CustomerPanelConfig{
		Enabled: boolEnvOr("APP_CUSTOMER_PANEL_ENABLED", true),
		// Defaults to true so a deployment that forgets to set it still gets a
		// cookie a browser will only send over TLS.
		CookieSecure:     boolEnvOr("APP_CUSTOMER_COOKIE_SECURE", true),
		MagicLinkEnabled: boolEnvOr("APP_CUSTOMER_MAGIC_LINK_ENABLED", false),
	}
}

func loadOperator() (OperatorConfig, error) {
	cfg := OperatorConfig{
		ChatID:          int64EnvOr("APP_OPERATOR_CHAT_ID", 0),
		NotificationCap: intEnvOr("APP_OPERATOR_NOTIFICATION_CAP", 30),
		Window:          minuteEnvOr("APP_OPERATOR_NOTIFICATION_WINDOW_MINUTES", 5*time.Minute),
	}
	operators, err := int64ListEnv("APP_OPERATOR_TELEGRAM_IDS", nil)
	if err != nil {
		return OperatorConfig{}, err
	}
	cfg.OperatorIDs = operators
	return cfg, nil
}

func loadBackup() (BackupConfig, error) {
	cfg := BackupConfig{
		Enabled:       boolEnvOr("APP_BACKUP_ENABLED", false),
		Directory:     envOr("APP_BACKUP_DIR", "/var/lib/omniflow/backups"),
		Interval:      hourEnvOr("APP_BACKUP_INTERVAL_HOURS", 24*time.Hour),
		Retention:     dayEnvOr("APP_BACKUP_RETENTION_DAYS", 14*24*time.Hour),
		PgDumpPath:    envOr("APP_PG_DUMP_PATH", "pg_dump"),
		PgRestorePath: envOr("APP_PG_RESTORE_PATH", "pg_restore"),
	}
	if encoded := os.Getenv("APP_BACKUP_ENCRYPTION_KEY"); encoded != "" {
		key, err := decodeKey(encoded)
		if err != nil {
			return BackupConfig{}, errors.New("APP_BACKUP_ENCRYPTION_KEY must be base64-encoded 32 bytes")
		}
		cfg.EncryptionKey = key
	}
	if cfg.Enabled && len(cfg.EncryptionKey) != 32 {
		return BackupConfig{}, errors.New("APP_BACKUP_ENCRYPTION_KEY is required when APP_BACKUP_ENABLED is true")
	}
	return cfg, nil
}

func loadTelegramWebhook() (TelegramWebhookConfig, error) {
	cfg := TelegramWebhookConfig{
		URL:         strings.TrimSpace(os.Getenv("APP_TELEGRAM_WEBHOOK_URL")),
		SecretToken: os.Getenv("APP_TELEGRAM_WEBHOOK_SECRET"),
		ListenAddr:  envOr("APP_BOT_HTTP_ADDR", ":8082"),
		MetricsAddr: os.Getenv("APP_BOT_METRICS_ADDR"),
	}
	if cfg.URL == "" {
		return cfg, nil
	}
	parsed, err := url.ParseRequestURI(cfg.URL)
	if err != nil || parsed.Scheme != "https" {
		return TelegramWebhookConfig{}, errors.New("APP_TELEGRAM_WEBHOOK_URL must be an HTTPS URL")
	}
	// Telegram only accepts A-Z, a-z, 0-9, underscore, and hyphen, 1-256 bytes.
	if matched, _ := regexp.MatchString(`^[A-Za-z0-9_-]{32,256}$`, cfg.SecretToken); !matched {
		return TelegramWebhookConfig{}, errors.New("APP_TELEGRAM_WEBHOOK_SECRET must be 32-256 characters of A-Z, a-z, 0-9, underscore, or hyphen")
	}
	cfg.Enabled = true
	return cfg, nil
}

func decodeKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("key must be base64-encoded 32 bytes")
	}
	return key, nil
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

func int64EnvOr(key string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// int64ListEnv reads a comma-separated list of whole numbers. An entry that is
// not a positive number is an error rather than a silent omission, because a
// mistyped preset would otherwise disappear from the bot without explanation.
func int64ListEnv(key string, fallback []int64) ([]int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]int64, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("%s must be a comma-separated list of positive whole numbers", key)
		}
		values = append(values, parsed)
	}
	return values, nil
}

// hourEnvOr reads a whole number of hours so operators never have to encode
// a Go duration string in the environment.
func hourEnvOr(key string, fallback time.Duration) time.Duration {
	return scaledEnvOr(key, fallback, time.Hour)
}

func dayEnvOr(key string, fallback time.Duration) time.Duration {
	return scaledEnvOr(key, fallback, 24*time.Hour)
}

func minuteEnvOr(key string, fallback time.Duration) time.Duration {
	return scaledEnvOr(key, fallback, time.Minute)
}

func secondEnvOr(key string, fallback time.Duration) time.Duration {
	return scaledEnvOr(key, fallback, time.Second)
}

func scaledEnvOr(key string, fallback time.Duration, unit time.Duration) time.Duration {
	parsed, err := strconv.Atoi(os.Getenv(key))
	if err != nil || parsed < 0 {
		return fallback
	}
	return time.Duration(parsed) * unit
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
