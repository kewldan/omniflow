package config

import (
	"errors"
	"os"
	"strings"
	"time"
)

// MailConfig configures outgoing e-mail for every process that sends or
// queues it. It is read by the api (sign-in links, the panel's test message),
// the worker (the delivery pass), and the bot (nothing yet, but the same
// variables), so one .env configures all three identically.
type MailConfig struct {
	// Transport is APP_EMAIL_TRANSPORT: auto (default), resend, or smtp.
	Transport    string
	From         string
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	// SMTPSecurity is starttls (default), tls, or none.
	SMTPSecurity string
	// ReceiptsEnabled e-mails a payment confirmation to customers with a
	// verified address (APP_EMAIL_RECEIPTS_ENABLED, default true).
	ReceiptsEnabled bool
	// BlockedDomains are the operator's own disposable domains, added to the
	// built-in list (APP_EMAIL_BLOCKED_DOMAINS).
	BlockedDomains string
	// The marketing policy shared with Telegram broadcasts.
	MarketingFrequencyCap int
	MarketingWindow       time.Duration
	MarketingLongCap      int
	MarketingLongWindow   time.Duration
}

// Configured reports whether any transport is set up.
func (mail MailConfig) Configured() bool {
	return mail.ResendAPIKey != "" || mail.SMTPHost != ""
}

// LoadMail reads the e-mail settings.
func LoadMail() (MailConfig, error) {
	mail := MailConfig{
		Transport:             strings.ToLower(envOr("APP_EMAIL_TRANSPORT", "auto")),
		From:                  strings.TrimSpace(os.Getenv("APP_EMAIL_FROM")),
		ResendAPIKey:          strings.TrimSpace(os.Getenv("APP_RESEND_API_KEY")),
		SMTPHost:              strings.TrimSpace(os.Getenv("APP_SMTP_HOST")),
		SMTPPort:              intEnvOr("APP_SMTP_PORT", 0),
		SMTPUsername:          os.Getenv("APP_SMTP_USERNAME"),
		SMTPPassword:          os.Getenv("APP_SMTP_PASSWORD"),
		SMTPSecurity:          strings.ToLower(envOr("APP_SMTP_SECURITY", "starttls")),
		ReceiptsEnabled:       boolEnvOr("APP_EMAIL_RECEIPTS_ENABLED", true),
		BlockedDomains:        os.Getenv("APP_EMAIL_BLOCKED_DOMAINS"),
		MarketingFrequencyCap: intEnvOr("APP_MARKETING_FREQUENCY_CAP", 3),
		MarketingWindow:       hourEnvOr("APP_MARKETING_WINDOW_HOURS", 7*24*time.Hour),
		MarketingLongCap:      intEnvOr("APP_MARKETING_LONG_FREQUENCY_CAP", 0),
		MarketingLongWindow:   hourEnvOr("APP_MARKETING_LONG_WINDOW_HOURS", 30*24*time.Hour),
	}
	switch mail.Transport {
	case "auto", "resend", "smtp":
	default:
		return MailConfig{}, errors.New("APP_EMAIL_TRANSPORT must be auto, resend, or smtp")
	}
	if mail.Configured() && mail.From == "" {
		return MailConfig{}, errors.New("APP_EMAIL_FROM is required when an e-mail transport is configured")
	}
	return mail, nil
}
