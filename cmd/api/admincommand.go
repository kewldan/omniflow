package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/platform"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// adminCommandUsage is printed for `help` and for any command line the API does
// not know.
const adminCommandUsage = `usage: api [command]

Run inside the api container, e.g.  docker exec omniflow-api /app <command>

  (no command)                              serve the HTTP API
  health                                    check the database, Valkey, Remnawave,
                                            the schema and the work queues
  version                                   print the build version

  admin list                                operators, their sign-in state and passkeys
  admin password-login <email> [--for 2h]   let an operator sign in with a password
                                            again: for the given time, or for good
                                            without --for (a lost passkey)
  admin unlock <email>                      clear failed attempts and a lockout
  admin revoke-sessions <email>             end every session of an operator
  admin reset-password <email>              issue a one-time password reset token
                                            for an operator and print it here
`

// runCommand handles a command line given to the API binary. It is how a
// deployer does the few things that must not travel through the network or the
// log: a password reset token is printed to the terminal that asked for it and
// nowhere else, and a way back in for an operator whose passkey is gone needs
// the server rather than the panel. Anyone who can run this already holds
// APP_DATABASE_URL and APP_DATA_ENCRYPTION_KEY, so nothing here gives away
// what they could not take.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h"):
		_, _ = fmt.Fprint(stdout, adminCommandUsage)
		return 0
	case len(args) == 1 && args[0] == "version":
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	case len(args) == 1 && args[0] == "health":
		return runHealth(ctx, stdout, stderr)
	case len(args) == 3 && args[0] == "admin" && args[1] == "reset-password":
		return resetOperatorPassword(ctx, args[2], stdout, stderr)
	case len(args) == 2 && args[0] == "admin" && args[1] == "list":
		return withAdminService(ctx, stderr, func(ctx context.Context, service *adminauthpg.Service, _ config.Config) int {
			return listOperators(ctx, service, stdout, stderr)
		})
	case len(args) >= 3 && args[0] == "admin" && args[1] == "password-login":
		duration, ok := parseFor(args[3:], stderr)
		if !ok {
			return 2
		}
		return withAdminService(ctx, stderr, func(ctx context.Context, service *adminauthpg.Service, _ config.Config) int {
			account, err := service.ReopenPasswordLogin(ctx, args[2], duration)
			if code := reportAccountErr(err, args[2], stderr); code != 0 {
				return code
			}
			if duration > 0 {
				_, _ = fmt.Fprintf(stdout, "%s may sign in with a password until %s.\n"+
					"Register a new passkey in the panel; password sign-in closes again on its own.\n",
					account.Email, time.Now().Add(duration).UTC().Format(time.RFC3339))
			} else {
				_, _ = fmt.Fprintf(stdout, "Password sign-in is on again for %s.\n", account.Email)
			}
			return 0
		})
	case len(args) == 3 && args[0] == "admin" && args[1] == "unlock":
		return withAdminService(ctx, stderr, func(ctx context.Context, service *adminauthpg.Service, _ config.Config) int {
			account, err := service.UnlockAccount(ctx, args[2])
			if code := reportAccountErr(err, args[2], stderr); code != 0 {
				return code
			}
			_, _ = fmt.Fprintf(stdout, "%s is unlocked.\n", account.Email)
			return 0
		})
	case len(args) == 3 && args[0] == "admin" && args[1] == "revoke-sessions":
		return withAdminService(ctx, stderr, func(ctx context.Context, service *adminauthpg.Service, _ config.Config) int {
			revoked, err := service.RevokeAllSessions(ctx, args[2])
			if code := reportAccountErr(err, args[2], stderr); code != 0 {
				return code
			}
			_, _ = fmt.Fprintf(stdout, "Ended %d session(s) of %s.\n", revoked, args[2])
			return 0
		})
	}
	_, _ = fmt.Fprint(stderr, adminCommandUsage)
	return 2
}

// parseFor reads an optional `--for <duration>`.
func parseFor(rest []string, stderr io.Writer) (time.Duration, bool) {
	if len(rest) == 0 {
		return 0, true
	}
	if len(rest) != 2 || rest[0] != "--for" {
		_, _ = fmt.Fprint(stderr, adminCommandUsage)
		return 0, false
	}
	duration, err := time.ParseDuration(rest[1])
	if err != nil || duration <= 0 || duration > 7*24*time.Hour {
		_, _ = fmt.Fprintln(stderr, "--for takes a duration up to 168h, e.g. 30m or 2h")
		return 0, false
	}
	return duration, true
}

func reportAccountErr(err error, email string, stderr io.Writer) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, adminauthpg.ErrNotFound):
		_, _ = fmt.Fprintf(stderr, "no operator account uses %s\n", email)
	default:
		_, _ = fmt.Fprintf(stderr, "failed: %v\n", err)
	}
	return 1
}

// withAdminService opens the database and the operator service for one
// command, with a deadline so a hung database cannot hold the terminal.
func withAdminService(
	ctx context.Context, stderr io.Writer,
	run func(context.Context, *adminauthpg.Service, config.Config) int,
) int {
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
	return run(ctx, service, cfg)
}

// listOperators prints every operator with what decides how they get in.
func listOperators(ctx context.Context, service *adminauthpg.Service, stdout, stderr io.Writer) int {
	table := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "EMAIL\tSTATUS\tROLES\tPASSWORD\t2FA\tLOCKED\tLAST SIGN-IN")
	cursor := ""
	for {
		page, err := service.ListAccounts(ctx, "", cursor, 100)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "could not list operators: %v\n", err)
			return 1
		}
		for _, account := range page.Accounts {
			password := "on"
			switch {
			case !account.PasswordSet:
				password = "none"
			case account.PasswordLoginDisabled && account.PasswordLoginAllowedUntil != nil &&
				account.PasswordLoginAllowedUntil.After(time.Now()):
				password = "reopened until " + account.PasswordLoginAllowedUntil.UTC().Format("01-02 15:04")
			case account.PasswordLoginDisabled:
				password = "off (passkeys only)"
			}
			twoFactor := "-"
			if account.TOTPEnabled {
				twoFactor = "totp"
			}
			locked := "-"
			if account.LockedUntil != nil && account.LockedUntil.After(time.Now()) {
				locked = "until " + account.LockedUntil.UTC().Format("15:04")
			} else if account.FailedLogins > 0 {
				locked = fmt.Sprintf("%d failed", account.FailedLogins)
			}
			last := "never"
			if account.LastLoginAt != nil {
				last = account.LastLoginAt.UTC().Format("2006-01-02 15:04")
			}
			roles := make([]string, 0, len(account.Roles))
			for _, role := range account.Roles {
				roles = append(roles, string(role))
			}
			_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", account.Email, account.Status,
				strings.Join(roles, ","), password, twoFactor, locked, last)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	_ = table.Flush()
	return 0
}

// runHealth checks what the API depends on, the way /readyz does, and adds
// what an operator on the server wants to see at a glance: the schema
// revision and work that is stuck. It exits non-zero when a dependency is down.
func runHealth(ctx context.Context, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	failed := false
	line := func(name, state, detail string) {
		_, _ = fmt.Fprintf(stdout, "%-12s %-5s %s\n", name, state, detail)
	}
	probe := func(name string, check func(context.Context) error) {
		started := time.Now()
		probeCtx, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if checkErr := check(probeCtx); checkErr != nil {
			failed = true
			line(name, "FAIL", checkErr.Error())
			return
		}
		line(name, "ok", fmt.Sprintf("%d ms", time.Since(started).Milliseconds()))
	}

	_, _ = fmt.Fprintf(stdout, "omniflow %s\n\n", version)
	pool, poolErr := pgxpool.New(ctx, cfg.DatabaseURL)
	if poolErr != nil {
		failed = true
		line("postgres", "FAIL", poolErr.Error())
	} else {
		defer pool.Close()
		probe("postgres", pool.Ping)
	}
	if valkey, valkeyErr := platform.NewValkeyClient(cfg.ValkeyURL); valkeyErr != nil {
		failed = true
		line("valkey", "FAIL", valkeyErr.Error())
	} else {
		defer valkey.Close()
		probe("valkey", func(probeCtx context.Context) error {
			return valkey.Do(probeCtx, valkey.B().Ping().Build()).Error()
		})
	}
	if client, clientErr := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken, remnawave.OptionsFromEnv(os.Getenv)); clientErr != nil {
		failed = true
		line("remnawave", "FAIL", clientErr.Error())
	} else {
		probe("remnawave", client.Ping)
	}

	if pool != nil && poolErr == nil {
		_, _ = fmt.Fprintln(stdout)
		facts := []struct{ name, query string }{
			{"schema", `SELECT version FROM atlas_schema_revisions.atlas_schema_revisions ORDER BY version DESC LIMIT 1`},
			{"fulfilment", `SELECT count(*) FILTER (WHERE status = 'retrying') || ' retrying, ' ||
				count(*) FILTER (WHERE status = 'failed' AND updated_at > now() - interval '1 day') || ' failed today'
				FROM fulfillment_operations`},
			{"jobs", `SELECT count(*) FILTER (WHERE state = 'retryable') || ' retryable, ' ||
				count(*) FILTER (WHERE state = 'discarded' AND finalized_at > now() - interval '1 day') || ' discarded today'
				FROM river_job`},
			{"payments", `SELECT count(*) || ' awaiting action for over an hour' FROM payment_intents
				WHERE status IN ('pending', 'requires_action') AND created_at < now() - interval '1 hour'
				AND created_at > now() - interval '7 days'`},
			{"support", `SELECT count(*) || ' waiting for a reply' FROM support_tickets WHERE status = 'open'`},
			{"operators", `SELECT count(*) FILTER (WHERE status = 'pending') || ' notices waiting, ' ||
				count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '1 day') || ' failed today'
				FROM operator_notifications`},
		}
		for _, fact := range facts {
			var value string
			if scanErr := pool.QueryRow(ctx, fact.query).Scan(&value); scanErr != nil {
				line(fact.name, "?", scanErr.Error())
				continue
			}
			line(fact.name, "", value)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func resetOperatorPassword(ctx context.Context, email string, stdout, stderr io.Writer) int {
	return withAdminService(ctx, stderr, func(ctx context.Context, service *adminauthpg.Service, cfg config.Config) int {
		return issueResetToken(ctx, service, email, cfg.PublicURL, stdout, stderr)
	})
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
