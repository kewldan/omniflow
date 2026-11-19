package mailer

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is the smallest server net/smtp will talk to: it accepts one
// message per connection and records it, or refuses RCPT with rcptCode.
type fakeSMTP struct {
	listener net.Listener
	mu       sync.Mutex
	messages []string
	rcptCode string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSMTP{listener: listener, rcptCode: "250 ok"}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	return server
}

func (server *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	write := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	write("220 fake ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			write("250 fake")
		case strings.HasPrefix(command, "MAIL FROM"):
			write("250 ok")
		case strings.HasPrefix(command, "RCPT TO"):
			server.mu.Lock()
			code := server.rcptCode
			server.mu.Unlock()
			write(code)
		case command == "DATA":
			write("354 go ahead")
			var body strings.Builder
			for {
				dataLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				body.WriteString(dataLine)
			}
			server.mu.Lock()
			server.messages = append(server.messages, body.String())
			server.mu.Unlock()
			write("250 queued")
		case command == "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func TestNewPicksTheConfiguredTransport(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("nothing configured: %v", err)
	}
	if sender, err := New(Config{ResendAPIKey: "key", From: "a@example.com"}); err != nil {
		t.Fatal(err)
	} else if _, ok := sender.(*Resend); !ok {
		t.Fatalf("auto with a key = %T, want Resend", sender)
	}
	if sender, err := New(Config{SMTPHost: "smtp.example.com", From: "Shop <a@example.com>"}); err != nil {
		t.Fatal(err)
	} else if smtpSender, ok := sender.(*SMTP); !ok || smtpSender.port != 587 {
		t.Fatalf("auto with a host = %#v", sender)
	}
	if _, err := New(Config{Transport: "smtp", SMTPHost: "smtp.example.com", From: "a@example.com",
		SMTPUsername: "u", SMTPPassword: "p", SMTPSecurity: "none"}); err == nil {
		t.Fatal("credentials over an unencrypted remote link were accepted")
	}
	if _, err := New(Config{Transport: "pigeon"}); err == nil {
		t.Fatal("an unknown transport was accepted")
	}
}

func TestSMTPDeliversMultipartWithHeaders(t *testing.T) {
	t.Parallel()
	server := newFakeSMTP(t)
	address := server.listener.Addr().(*net.TCPAddr)
	sender, err := NewSMTP(Config{
		SMTPHost: "127.0.0.1", SMTPPort: address.Port, SMTPSecurity: SecurityNone, From: "Shop <shop@example.com>",
	})
	if err != nil {
		t.Fatal(err)
	}
	message := Broadcast("client@example.com", "ru", "Новости", "Привет", "https://example.com/u?t=x")
	if err := sender.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.messages) != 1 {
		t.Fatalf("messages = %d", len(server.messages))
	}
	raw := server.messages[0]
	for _, want := range []string{
		"List-Unsubscribe: <https://example.com/u?t=x>", "List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"multipart/alternative", "To: client@example.com", "=?utf-8?q?",
		base64.StdEncoding.EncodeToString([]byte("Привет"))[:8],
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("message lacks %q:\n%s", want, raw)
		}
	}
}

func TestSMTPReportsPermanentRefusalWithoutTheAddress(t *testing.T) {
	t.Parallel()
	server := newFakeSMTP(t)
	server.rcptCode = "550 5.1.1 <client@example.com> unknown user"
	address := server.listener.Addr().(*net.TCPAddr)
	sender, _ := NewSMTP(Config{SMTPHost: "127.0.0.1", SMTPPort: address.Port, SMTPSecurity: SecurityNone, From: "shop@example.com"})
	err := sender.Send(context.Background(), Test("client@example.com", "en"))
	if !errors.Is(err, ErrPermanent) || strings.Contains(err.Error(), "client@example.com") {
		t.Fatalf("error = %v, want a permanent failure that does not name the recipient", err)
	}
}

func TestHeadersCannotBeInjected(t *testing.T) {
	t.Parallel()
	message := Message{Headers: map[string]string{
		"Bcc": "victim@example.com", "List-Unsubscribe": "<x>\r\nBcc: victim@example.com",
	}}
	headers := message.safeHeaders()
	if _, ok := headers["Bcc"]; ok || strings.ContainsAny(headers["List-Unsubscribe"], "\r\n") {
		t.Fatalf("headers = %q", headers)
	}
}

func TestDisposableDomains(t *testing.T) {
	t.Parallel()
	checker := NewDisposableChecker("@corp-temp.example, other.test")
	for address, want := range map[string]bool{
		"a@mailinator.com": true, "a@Sub.Mail.TM": true, "a@corp-temp.example": true,
		"a@x.other.test": true, "a@gmail.com": false, "a@yandex.ru": false, "not-an-address": false,
		"a@tm": false,
	} {
		if got := checker.IsDisposable(address); got != want {
			t.Errorf("IsDisposable(%q) = %v, want %v", address, got, want)
		}
	}
	if IsDisposable("a@corp-temp.example") {
		t.Error("the operator's list leaked into the package default")
	}
}

func TestUnsubscribeTokens(t *testing.T) {
	t.Parallel()
	key := []byte("0123456789abcdef0123456789abcdef")
	signer, err := NewUnsubscribeSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	token := signer.Token("5b0c7d0e-0000-4000-8000-000000000001")
	customer, err := signer.Verify(token)
	if err != nil || customer != "5b0c7d0e-0000-4000-8000-000000000001" {
		t.Fatalf("verify = %q %v", customer, err)
	}
	other, _ := NewUnsubscribeSigner([]byte("fedcba9876543210fedcba9876543210"))
	for _, forged := range []string{
		other.Token("5b0c7d0e-0000-4000-8000-000000000001"), token + "x", "", "abc", strings.Replace(token, ".", "", 1),
	} {
		if _, err := signer.Verify(forged); !errors.Is(err, ErrBadUnsubscribeToken) {
			t.Errorf("Verify(%q) = %v", forged, err)
		}
	}
}

func TestTransactionalMailHasNoUnsubscribe(t *testing.T) {
	t.Parallel()
	receipt := ReceiptMessage("a@example.com", "ru", Receipt{OrderNumber: "AB12", Amount: "100 ₽", Paid: "today", Description: "VPN"})
	if receipt.Headers["List-Unsubscribe"] != "" || strings.Contains(receipt.Text, "Отписаться") {
		t.Fatal("a receipt offered an unsubscribe link")
	}
	if !strings.Contains(receipt.Subject, "AB12") || !strings.Contains(receipt.HTML, "100 ₽") {
		t.Fatalf("receipt = %+v", receipt)
	}
	if english := Text("de", "test_subject"); english != "Test message" {
		t.Fatalf("fallback = %q", english)
	}
}
