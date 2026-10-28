package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/config"
)

// adminCommandUsage is printed for any command line the API does not know.
const adminCommandUsage = `usage:
  api                                   serve the HTTP API
  api admin reset-password <email>      issue a one-time password reset token
                                        for an operator and print it here
`

// runCommand handles a command line given to the API binary. It is how a
// deployer does the few things that must not travel through the network or the
// log: a password reset token is printed to the terminal that asked for it and
// nowhere else. Anyone who can run this already holds APP_DATABASE_URL and
// APP_DATA_ENCRYPTION_KEY, so the token gives away nothing they could not take.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 3 && args[0] == "admin" && args[1] == "reset-password" {
		return resetOperatorPassword(ctx, args[2], stdout, stderr)
	}
	_, _ = fmt.Fprint(stderr, adminCommandUsage)
	return 2
}

func resetOperatorPassword(ctx context.Context, email string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	if cfg.DatabaseURL == "" || len(cfg.DataEncryptionKey) != 32 {
		_, _ = fmt.Fprintln(stderr, "APP_DATABASE_URL and APP_DATA_ENCRYPTION_KEY are required")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "database: %v\n", err)
		return 1
	}
	defer pool.Close()
	service, err := adminauthpg.New(pool, cfg.DataEncryptionKey, adminauthpg.Options{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "admin service: %v\n", err)
		return 1
	}
	return issueResetToken(ctx, service, email, cfg.PublicURL, stdout, stderr)
}

// resetIssuer is the one method the command needs, so the output can be
// tested without a database.
type resetIssuer interface {
	IssuePasswordReset(
		ctx context.Context, email string, request adminauthpg.RequestContext,
	) (string, adminauthpg.Account, error)
}

func issueResetToken(
	ctx context.Context, service resetIssuer, email, publicURL string, stdout, stderr io.Writer,
) int {
	token, account, err := service.IssuePasswordReset(
		ctx, email, adminauthpg.RequestContext{RequestID: "cli-reset-password"},
	)
	switch {
	case errors.Is(err, adminauthpg.ErrNotFound):
		_, _ = fmt.Fprintf(stderr, "no operator account uses %s\n", email)
		return 1
	case errors.Is(err, adminauthpg.ErrAccountDisabled):
		_, _ = fmt.Fprintf(stderr, "the operator account for %s is not active\n", email)
		return 1
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "could not issue a reset token: %v\n", err)
		return 1
	}
	endpoint := strings.TrimRight(publicURL, "/")
	if endpoint == "" {
		endpoint = "https://<your-host>"
	}
	_, _ = fmt.Fprintf(stdout,
		"Password reset token for %s (single use, valid for %s):\n\n  %s\n\n"+
			"Redeem it with:\n\n  curl -X POST %s/v1/panel/auth/password-reset/complete \\\n"+
			"    -H 'Content-Type: application/json' \\\n"+
			"    -d '{\"token\":\"<token>\",\"newPassword\":\"<new password>\"}'\n\n"+
			"Completing the reset ends every session the account holds.\n",
		account.Email, adminauthpg.PasswordResetTTL, token, endpoint,
	)
	return 0
}
