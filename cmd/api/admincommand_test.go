package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/omniflow/omniflow/internal/adminauthpg"
)

type stubIssuer struct {
	token string
	err   error
}

func (stub stubIssuer) IssuePasswordReset(
	context.Context, string, adminauthpg.RequestContext,
) (string, adminauthpg.Account, error) {
	return stub.token, adminauthpg.Account{Email: "owner@example.com"}, stub.err
}

// The token goes to the terminal that asked for it — stdout — and nowhere
// else. In particular it never reaches stderr, which a container runtime
// collects into the same log stream the old warn-level line leaked through.
func TestResetTokenIsPrintedToStdoutOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := issueResetToken(context.Background(), stubIssuer{token: "tok-123"},
		"owner@example.com", "https://panel.example.com/", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tok-123") ||
		!strings.Contains(stdout.String(), "https://panel.example.com/v1/panel/auth/password-reset/complete") {
		t.Fatalf("stdout does not carry the token and the redeem URL: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr is not empty: %q", stderr.String())
	}
}

func TestUnknownOperatorFailsWithoutAToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := issueResetToken(context.Background(), stubIssuer{err: adminauthpg.ErrNotFound},
		"nobody@example.com", "", &stdout, &stderr)
	if code == 0 || stdout.Len() != 0 {
		t.Fatalf("code = %d, stdout = %q", code, stdout.String())
	}
}

func TestUnknownCommandPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCommand(context.Background(), []string{"bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "reset-password") {
		t.Fatalf("usage not printed: %q", stderr.String())
	}
}
