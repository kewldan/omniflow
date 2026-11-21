// Command migrate-bedolaga moves a Bedolaga bot installation's customers into
// Omniflow: plan reads both sides and reports, apply writes, verify reconciles.
//
// It reads Bedolaga through APP_BEDOLAGA_DATABASE_URL inside a read-only
// snapshot, reads the Remnawave panel's user list through the same adapter
// the API uses, and writes Omniflow through APP_DATABASE_URL. It never writes
// to Bedolaga or to Remnawave. See docs/operations/migrate-from-bedolaga.mdx.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/app"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	"github.com/omniflow/omniflow/internal/bedolagaimportpg"
	"github.com/omniflow/omniflow/internal/bedolagasource"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/config"
	"github.com/omniflow/omniflow/internal/customerpg"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/jobs"
	"github.com/omniflow/omniflow/internal/remnawave"
)

const usage = `usage:
  migrate-bedolaga plan   -mapping FILE [-json FILE]
      read Bedolaga, Remnawave and Omniflow, write nothing, report what apply would do
  migrate-bedolaga apply  -mapping FILE -confirm [-json FILE] [-limit N] [-actor NAME]
      write customers, balances, subscriptions and referrals; safe to re-run
  migrate-bedolaga verify -mapping FILE [-json FILE]
      reconcile users, balance sum, active subscriptions and referral pairs
  migrate-bedolaga runs
      list the latest apply runs

environment:
  APP_DATABASE_URL            Omniflow database (written by apply)
  APP_DATA_ENCRYPTION_KEY     the key the API seals contacts with
  APP_REMNAWAVE_URL, APP_REMNAWAVE_TOKEN
                              the panel; only its user list is read
  APP_BEDOLAGA_DATABASE_URL   the Bedolaga database; only read

exit status: 0 done, 1 error, 2 usage, 3 apply left failures or stopped at -limit,
4 verify found a mismatch
`

// Exit statuses. The deploy scripts branch on them.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitPartial  = 3
	exitMismatch = 4
)

// command is one parsed invocation.
type command struct {
	name     string
	mapping  string
	jsonPath string
	limit    int
	actor    string
	confirm  bool
}

func parse(args []string, stderr io.Writer) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New("a command is required")
	}
	parsed := command{name: args[0], actor: "migrate-bedolaga"}
	flags := flag.NewFlagSet(parsed.name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	switch parsed.name {
	case "plan", "verify":
		flags.StringVar(&parsed.mapping, "mapping", "", "operator mapping file (YAML or JSON)")
		flags.StringVar(&parsed.jsonPath, "json", "", "also write the report as JSON to this file")
	case "apply":
		flags.StringVar(&parsed.mapping, "mapping", "", "operator mapping file (YAML or JSON)")
		flags.StringVar(&parsed.jsonPath, "json", "", "also write the result as JSON to this file")
		flags.IntVar(&parsed.limit, "limit", 0, "stop after this many writes (0: no limit)")
		flags.StringVar(&parsed.actor, "actor", parsed.actor, "who runs the import, recorded in audit events")
		flags.BoolVar(&parsed.confirm, "confirm", false, "required: apply writes to the Omniflow database")
	case "runs":
	default:
		return command{}, fmt.Errorf("unknown command %q", parsed.name)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return command{}, err
	}
	if flags.NArg() > 0 {
		return command{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if parsed.name != "runs" && parsed.mapping == "" {
		return command{}, errors.New("-mapping is required")
	}
	if parsed.name == "apply" && !parsed.confirm {
		return command{}, errors.New("apply writes to the Omniflow database; run plan first, then pass -confirm")
	}
	if parsed.limit < 0 {
		return command{}, errors.New("-limit must not be negative")
	}
	return parsed, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	parsed, err := parse(args, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n\n%s", err, usage)
		return exitUsage
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cfg, err := config.LoadMigration()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "invalid configuration: %v\n", err)
		return exitError
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Omniflow database: %v\n", err)
		return exitError
	}
	defer pool.Close()
	service, err := build(pool, cfg, parsed.actor, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	if parsed.name == "runs" {
		return listRuns(ctx, service, stdout, stderr)
	}
	mapping, err := readMapping(parsed.mapping)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	connection, err := bedolagasource.Connect(ctx, cfg.BedolagaDatabaseURL)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Bedolaga database: %v\n", err)
		return exitError
	}
	defer func() { _ = connection.Close(context.Background()) }()
	source, err := bedolagasource.Read(ctx, connection)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v\n", err)
		return exitError
	}
	switch parsed.name {
	case "plan":
		plan, err := service.Plan(ctx, source, mapping)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "plan: %v\n", err)
			return exitError
		}
		plan.Report.WriteText(stdout)
		return writeJSON(parsed.jsonPath, plan.Report, stderr, exitOK)
	case "apply":
		result, err := service.Apply(ctx, source, mapping, bedolagaimportpg.ApplyOptions{Limit: parsed.limit})
		writeApply(stdout, result)
		status := exitOK
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "apply stopped: %v\nRe-run apply to continue; finished records are not written twice.\n", err)
			status = exitError
		} else if len(result.Failures) > 0 || result.Stopped || result.Remaining > 0 {
			status = exitPartial
		}
		return writeJSON(parsed.jsonPath, result, stderr, status)
	default:
		report, err := service.Verify(ctx, source, mapping)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "verify: %v\n", err)
			return exitError
		}
		report.WriteText(stdout)
		status := exitOK
		if !report.OK {
			status = exitMismatch
		}
		return writeJSON(parsed.jsonPath, report, stderr, status)
	}
}

// build wires the importer exactly as the processes wire their own parts:
// the wallet goes through the production commerce store, contacts through
// the customer service, and the panel through the versioned adapter.
func build(pool *pgxpool.Pool, cfg config.MigrationConfig, actor string, logger *slog.Logger) (*bedolagaimportpg.Service, error) {
	jobsClient, err := jobs.NewInsertOnlyClient(pool)
	if err != nil {
		return nil, fmt.Errorf("initialize River: %w", err)
	}
	enqueue, err := app.FulfillmentEnqueuer(jobsClient)
	if err != nil {
		return nil, err
	}
	store, err := commercepg.New(pool, enqueue, commercepg.Options{Logger: logger})
	if err != nil {
		return nil, err
	}
	customers, err := customerpg.New(pool, cfg.DataEncryptionKey, 0)
	if err != nil {
		return nil, err
	}
	panel, err := remnawave.NewClient(cfg.RemnawaveURL, cfg.RemnawaveToken, remnawave.OptionsFromEnv(os.Getenv))
	if err != nil {
		return nil, fmt.Errorf("Remnawave: %w", err)
	}
	return bedolagaimportpg.New(pool, store, customers, panel, bedolagaimportpg.Options{Actor: actor, Logger: logger})
}

func readMapping(path string) (bedolagaimport.Mapping, error) {
	file, err := os.Open(path)
	if err != nil {
		return bedolagaimport.Mapping{}, fmt.Errorf("mapping file: %w", err)
	}
	defer func() { _ = file.Close() }()
	return bedolagaimport.ParseMapping(file)
}

func writeApply(stdout io.Writer, result bedolagaimportpg.ApplyResult) {
	result.Plan.WriteText(stdout)
	_, _ = fmt.Fprintf(stdout, "\nApply run %s\n", result.RunID)
	for _, kind := range []string{bedolagaimport.KindUser, "balance", bedolagaimport.KindSubscription, bedolagaimport.KindReferral, "trial", "period", "payment", "discount", "referral_code"} {
		_, _ = fmt.Fprintf(stdout, "  written %-13s %d\n", kind+":", result.Written[kind])
	}
	if len(result.Failures) > 0 {
		_, _ = fmt.Fprintf(stdout, "  failures: %d\n", len(result.Failures))
		for index, failure := range result.Failures {
			if index == 20 {
				_, _ = fmt.Fprintln(stdout, "    … the rest are in the JSON result")
				break
			}
			_, _ = fmt.Fprintf(stdout, "    %s %d: %s\n", failure.Kind, failure.SourceID, failure.Error)
		}
	}
	if result.Stopped {
		_, _ = fmt.Fprintln(stdout, "  stopped at -limit; run apply again to continue")
	}
	_, _ = fmt.Fprintf(stdout, "  still to write after this run: %d\n", result.Remaining)
}

func listRuns(ctx context.Context, service *bedolagaimportpg.Service, stdout, stderr io.Writer) int {
	runs, err := service.RecentRuns(ctx, 20)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "runs: %v\n", err)
		return exitError
	}
	if len(runs) == 0 {
		_, _ = fmt.Fprintln(stdout, "no apply runs yet")
	}
	for _, run := range runs {
		finished := "-"
		if run.FinishedAt.Valid {
			finished = run.FinishedAt.Time.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(stdout, "%s  %-9s  %s  started %s  finished %s\n",
			databaseutil.UUIDStrings([]pgtype.UUID{run.ID})[0], run.Status, run.Actor, run.StartedAt.Time.UTC().Format(time.RFC3339), finished)
	}
	return exitOK
}

// writeJSON writes the machine-readable report beside the text. The file is
// private to the operator: it carries Bedolaga row IDs, which are not
// personal data, but a report is still nobody else's business.
func writeJSON(path string, value any, stderr io.Writer, status int) int {
	if path == "" {
		return status
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "encode JSON report: %v\n", err)
		return exitError
	}
	if err = os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "write JSON report: %v\n", err)
		return exitError
	}
	return status
}
