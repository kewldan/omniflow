package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Transport names.
const (
	TransportAuto   = "auto"
	TransportResend = "resend"
	TransportSMTP   = "smtp"
)

// SMTP security modes. STARTTLS upgrades a plain connection (port 587), TLS
// opens one already encrypted (port 465), and none is for a relay on the same
// host or a local development catcher only.
const (
	SecuritySTARTTLS = "starttls"
	SecurityTLS      = "tls"
	SecurityNone     = "none"
)

// Config selects and configures the transport.
type Config struct {
	// Transport is auto, resend, or smtp. Auto picks Resend when an API key is
	// set and SMTP when a host is.
	Transport    string
	From         string
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPSecurity string
}

// New builds the configured transport. With nothing configured it returns
// ErrNotConfigured, which callers treat as "e-mail is off", not as a failure.
func New(config Config) (Sender, error) {
	transport := strings.ToLower(strings.TrimSpace(config.Transport))
	if transport == "" {
		transport = TransportAuto
	}
	if transport == TransportAuto {
		switch {
		case strings.TrimSpace(config.ResendAPIKey) != "":
			transport = TransportResend
		case strings.TrimSpace(config.SMTPHost) != "":
			transport = TransportSMTP
		default:
			return nil, ErrNotConfigured
		}
	}
	switch transport {
	case TransportResend:
		return NewResend(config.ResendAPIKey, config.From)
	case TransportSMTP:
		return NewSMTP(config)
	default:
		return nil, fmt.Errorf("unknown e-mail transport %q (want auto, resend, or smtp)", config.Transport)
	}
}

// SMTP sends through a plain SMTP server.
type SMTP struct {
	host     string
	port     int
	username string
	password string
	security string
	from     string
	fromAddr string
	timeout  time.Duration
	// dial is replaced in tests.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// NewSMTP validates the SMTP settings.
func NewSMTP(config Config) (*SMTP, error) {
	host := strings.TrimSpace(config.SMTPHost)
	from := strings.TrimSpace(config.From)
	if host == "" {
		return nil, ErrNotConfigured
	}
	parsed, err := mail.ParseAddress(from)
	if err != nil {
		return nil, errors.New("APP_EMAIL_FROM must be an e-mail address, optionally with a display name")
	}
	security := strings.ToLower(strings.TrimSpace(config.SMTPSecurity))
	if security == "" {
		security = SecuritySTARTTLS
	}
	if security != SecuritySTARTTLS && security != SecurityTLS && security != SecurityNone {
		return nil, errors.New("APP_SMTP_SECURITY must be starttls, tls, or none")
	}
	port := config.SMTPPort
	if port == 0 {
		port = 587
		if security == SecurityTLS {
			port = 465
		}
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("APP_SMTP_PORT is out of range")
	}
	if config.SMTPPassword != "" && security == SecurityNone && !isLoopback(host) {
		// PLAIN authentication over an unencrypted link hands the password to
		// anyone on the path; net/smtp refuses it too, but with a less useful
		// message, and only at the first send.
		return nil, errors.New("SMTP credentials require APP_SMTP_SECURITY=starttls or tls")
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	return &SMTP{
		host: host, port: port, username: config.SMTPUsername, password: config.SMTPPassword,
		security: security, from: parsed.String(), fromAddr: parsed.Address, timeout: 30 * time.Second,
		dial: dialer.DialContext,
	}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Send delivers one message. Errors never include the recipient.
func (sender *SMTP) Send(ctx context.Context, message Message) error {
	recipient, err := mail.ParseAddress(message.To)
	if err != nil {
		return errors.New("recipient is not an e-mail address")
	}
	body, err := sender.compose(message, recipient.Address)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(sender.host, strconv.Itoa(sender.port))
	ctx, cancel := context.WithTimeout(ctx, sender.timeout)
	defer cancel()
	conn, err := sender.dial(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	tlsConfig := &tls.Config{ServerName: sender.host, MinVersion: tls.VersionTLS12}
	if sender.security == SecurityTLS {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, sender.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("SMTP greeting: %w", err)
	}
	defer client.Close()
	if sender.security == SecuritySTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not offer STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if sender.username != "" {
		if err = client.Auth(smtp.PlainAuth("", sender.username, sender.password, sender.host)); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", classifySMTP(err))
		}
	}
	if err = client.Mail(sender.fromAddr); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", classifySMTP(err))
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("SMTP RCPT TO refused: %w", classifySMTP(err))
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", classifySMTP(err))
	}
	if _, err = writer.Write(body); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("SMTP message refused: %w", classifySMTP(err))
	}
	return client.Quit()
}

// ErrPermanent marks a refusal the server will repeat: retrying it only adds
// to the sender's bounce rate.
var ErrPermanent = errors.New("permanent delivery failure")

// classifySMTP strips the server's text, which can echo the recipient, and
// keeps only whether the code was permanent (5xx).
func classifySMTP(err error) error {
	text := err.Error()
	if len(text) >= 3 && text[0] == '5' {
		return fmt.Errorf("%w (SMTP %s)", ErrPermanent, text[:3])
	}
	if len(text) >= 3 && text[0] == '4' {
		return fmt.Errorf("temporary failure (SMTP %s)", text[:3])
	}
	return errors.New("SMTP error")
}

// compose renders a MIME message: multipart/alternative when there is HTML,
// plain text otherwise, every text part base64-encoded so no line-length or
// charset rule of an old relay can mangle Cyrillic.
func (sender *SMTP) compose(message Message, to string) ([]byte, error) {
	var buffer bytes.Buffer
	header := func(name, value string) {
		buffer.WriteString(name + ": " + strings.NewReplacer("\r", "", "\n", "").Replace(value) + "\r\n")
	}
	header("From", sender.from)
	header("To", to)
	header("Subject", mime.QEncoding.Encode("utf-8", message.Subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	header("Message-ID", "<"+id+"@"+domainOf(sender.fromAddr)+">")
	header("MIME-Version", "1.0")
	extra := message.safeHeaders()
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header(name, extra[name])
	}
	if message.HTML == "" {
		header("Content-Type", "text/plain; charset=utf-8")
		header("Content-Transfer-Encoding", "base64")
		buffer.WriteString("\r\n")
		writeBase64(&buffer, message.Text)
		return buffer.Bytes(), nil
	}
	boundary, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	buffer.WriteString("\r\n")
	for _, part := range []struct{ kind, body string }{
		{"text/plain", message.Text}, {"text/html", message.HTML},
	} {
		buffer.WriteString("--" + boundary + "\r\n")
		buffer.WriteString("Content-Type: " + part.kind + "; charset=utf-8\r\n")
		buffer.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		writeBase64(&buffer, part.body)
	}
	buffer.WriteString("--" + boundary + "--\r\n")
	return buffer.Bytes(), nil
}

func writeBase64(buffer *bytes.Buffer, text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	for len(encoded) > 76 {
		buffer.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	buffer.WriteString(encoded + "\r\n")
}

func randomHex(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func domainOf(address string) string {
	if at := strings.LastIndexByte(address, '@'); at >= 0 {
		return address[at+1:]
	}
	return "localhost"
}
