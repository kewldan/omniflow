package bedolagaimportpg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	"github.com/omniflow/omniflow/internal/customerauth"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ApplyOptions bound one apply run.
type ApplyOptions struct {
	// Limit stops the run after this many writes. Zero means no limit. A
	// staged first pass — a hundred customers, then look — uses it.
	Limit int
}

// Failure is one record a run tried to write and could not. The error text
// is for the operator; it never carries a contact or a Telegram ID, because
// the importer never puts one in an error.
type Failure struct {
	Kind     string `json:"kind"`
	SourceID int64  `json:"sourceId"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

// ApplyResult is what one run did.
type ApplyResult struct {
	RunID    string                `json:"runId"`
	Plan     bedolagaimport.Report `json:"plan"`
	Written  map[string]int        `json:"written"`
	Failures []Failure             `json:"failures"`
	// Stopped is true when the run ended at its limit with work remaining.
	Stopped bool `json:"stopped"`
	// Remaining is how many writes the plan still held when the run ended.
	Remaining int `json:"remaining"`
}

var errLimitReached = errors.New("write limit reached")

// Apply carries out the plan. It is safe to run any number of times: each
// run re-plans against what Omniflow holds now, so whatever an earlier run
// wrote is "unchanged" and only the remainder is written.
//
// The error is non-nil only when the run itself could not continue — the
// database went away, the context was cancelled. A record that conflicts at
// write time is a Failure in the result, and the run goes on.
func (service *Service) Apply(ctx context.Context, source bedolagaimport.Source, mapping bedolagaimport.Mapping, options ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{Written: map[string]int{}, Failures: []Failure{}}
	run, err := dbgen.New(service.pool).CreateBedolagaImportRun(ctx, service.options.Actor)
	if err != nil {
		return result, fmt.Errorf("record the run: %w", err)
	}
	result.RunID = uuidString(run.ID)
	plan, versions, err := service.plan(ctx, source, mapping)
	if err != nil {
		service.finish(run.ID, "failed", result)
		return result, err
	}
	result.Plan = plan.Report
	runner := &applier{service: service, runID: run.ID, mapping: mapping, versions: versions, result: &result, limit: options.Limit, customers: map[int64]string{}}
	for _, decision := range plan.Users {
		if decision.CustomerID != "" {
			runner.customers[decision.Source.ID] = decision.CustomerID
		}
	}
	err = runner.run(ctx, plan)
	switch {
	case errors.Is(err, errLimitReached):
		result.Stopped = true
		err = nil
	case err != nil:
		service.finish(run.ID, "failed", result)
		return result, err
	}
	if after, planErr := service.Plan(ctx, source, mapping); planErr == nil {
		result.Remaining = after.Pending()
	}
	service.finish(run.ID, "completed", result)
	return result, nil
}

// finish records the outcome. It uses its own short-lived context so a run
// cancelled by the operator is still recorded as failed.
func (service *Service) finish(runID pgtype.UUID, status string, result ApplyResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := dbgen.New(service.pool).FinishBedolagaImportRun(ctx, dbgen.FinishBedolagaImportRunParams{
		RunID: runID, Status: status, Report: mustJSON(result),
	}); err != nil {
		service.options.Logger.Error("record Bedolaga import run outcome", "error", err)
	}
}

type applier struct {
	service   *Service
	runID     pgtype.UUID
	mapping   bedolagaimport.Mapping
	versions  map[string]dbgen.ListPlanVersionsForImportRow
	result    *ApplyResult
	limit     int
	writes    int
	customers map[int64]string
}

func (runner *applier) run(ctx context.Context, plan bedolagaimport.Plan) error {
	for _, decision := range plan.Users {
		if err := runner.user(ctx, decision); err != nil {
			return err
		}
	}
	for _, decision := range plan.Balances {
		if err := runner.balance(ctx, decision); err != nil {
			return err
		}
	}
	for _, decision := range plan.Subscriptions {
		if err := runner.subscription(ctx, decision); err != nil {
			return err
		}
	}
	for _, decision := range plan.Referrals {
		if err := runner.referral(ctx, decision); err != nil {
			return err
		}
	}
	return nil
}

// step counts a write against the limit before it is attempted.
func (runner *applier) step() error {
	if runner.limit > 0 && runner.writes >= runner.limit {
		return errLimitReached
	}
	runner.writes++
	return nil
}

// fail records a write-time failure. The cause is fatal to the run only when
// the context ended; anything else is this record's problem alone.
func (runner *applier) fail(ctx context.Context, kind string, sourceID int64, fingerprint [32]byte, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	code := bedolagaimport.CodeWriteConflict
	var coded codedError
	if errors.As(err, &coded) {
		code = coded.code
	}
	runner.result.Failures = append(runner.result.Failures, Failure{Kind: kind, SourceID: sourceID, Code: code, Error: err.Error()})
	if kind == "balance" {
		return nil
	}
	return runner.record(ctx, dbgen.New(runner.service.pool), kind, sourceID, bedolagaimport.ItemConflict, []string{code}, fingerprint, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{})
}

type codedError struct {
	code string
	err  error
}

func (err codedError) Error() string { return err.code + ": " + err.err.Error() }
func (err codedError) Unwrap() error { return err.err }

func (runner *applier) record(ctx context.Context, queries *dbgen.Queries, kind string, sourceID int64, status string, codes []string, fingerprint [32]byte, userID, subscriptionID, entitlementID pgtype.UUID) error {
	if codes == nil {
		codes = []string{}
	}
	_, err := queries.UpsertBedolagaImportItem(ctx, dbgen.UpsertBedolagaImportItemParams{
		Kind: kind, SourceID: sourceID, Status: status, Codes: codes, UserID: userID,
		SubscriptionID: subscriptionID, EntitlementID: entitlementID, Fingerprint: fingerprint[:], RunID: runner.runID,
	})
	if isNoRows(err) {
		// The row is already applied; a conflict cannot overwrite it.
		return nil
	}
	return err
}

// note records a conflict or skip the plan decided, so the item table shows
// every Bedolaga record the importer has looked at, not only the imported.
func (runner *applier) note(ctx context.Context, kind string, sourceID int64, action bedolagaimport.Action, codes []string, fingerprint [32]byte) error {
	status := bedolagaimport.ItemConflict
	if action == bedolagaimport.ActionSkip {
		status = bedolagaimport.ItemSkipped
	}
	return runner.record(ctx, dbgen.New(runner.service.pool), kind, sourceID, status, codes, fingerprint, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{})
}

func (runner *applier) inTx(ctx context.Context, work func(pgx.Tx, *dbgen.Queries) error) error {
	tx, err := runner.service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// History carried over from Bedolaga is not news: the operator stream
	// triggers skip a transaction marked as an import, so migrating fifty
	// thousand trials does not queue fifty thousand operator notices.
	if _, err := tx.Exec(ctx, "SELECT set_config('omniflow.import', 'on', true)"); err != nil {
		return err
	}
	if err := work(tx, dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func (runner *applier) user(ctx context.Context, decision bedolagaimport.UserDecision) error {
	switch decision.Action {
	case bedolagaimport.ActionConflict, bedolagaimport.ActionSkip:
		return runner.note(ctx, bedolagaimport.KindUser, decision.Source.ID, decision.Action, decision.Codes, decision.Fingerprint)
	case bedolagaimport.ActionCreate:
	default:
		return nil
	}
	if err := runner.step(); err != nil {
		return err
	}
	var customerID string
	err := runner.inTx(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		var err error
		customerID, err = runner.createCustomer(ctx, queries, decision)
		return err
	})
	if err != nil {
		return runner.fail(ctx, bedolagaimport.KindUser, decision.Source.ID, decision.Fingerprint, err)
	}
	runner.customers[decision.Source.ID] = customerID
	runner.result.Written[bedolagaimport.KindUser]++
	return nil
}

func (runner *applier) createCustomer(ctx context.Context, queries *dbgen.Queries, decision bedolagaimport.UserDecision) (string, error) {
	source := decision.Source
	subject := ""
	if source.TelegramID != nil {
		subject = strconv.FormatInt(*source.TelegramID, 10)
		// The same lock the bot and the web sign-in take, so a /start racing
		// the import from the same account converges on one customer.
		if err := queries.LockTelegramSubject(ctx, subject); err != nil {
			return "", err
		}
	}
	existing, err := queries.LockBedolagaImportItem(ctx, dbgen.LockBedolagaImportItemParams{Kind: bedolagaimport.KindUser, SourceID: source.ID})
	switch {
	case err == nil && existing.Status == bedolagaimport.ItemApplied:
		return uuidString(existing.UserID), nil
	case err != nil && !isNoRows(err):
		return "", err
	}
	customer, err := queries.InsertImportedCustomer(ctx, dbgen.InsertImportedCustomerParams{
		Status: decision.Status, Locale: decision.Locale, CreatedAt: timestamp(source.CreatedAt),
	})
	if err != nil {
		return "", err
	}
	if subject != "" {
		metadata := map[string]any{"source": "bedolaga"}
		for key, value := range map[string]string{"username": source.Username, "firstName": source.FirstName, "lastName": source.LastName} {
			if value != "" {
				metadata[key] = value
			}
		}
		if _, err = queries.LinkCustomerIdentity(ctx, dbgen.LinkCustomerIdentityParams{
			UserID: customer.ID, Provider: "telegram", ProviderSubject: subject,
			VerifiedAt: timestamp(source.CreatedAt), Metadata: mustJSON(metadata),
		}); err != nil {
			if isNoRows(err) {
				return "", codedError{bedolagaimport.CodeTelegramAlreadyLinked, errors.New("another customer holds this Telegram identity")}
			}
			return "", err
		}
	}
	if decision.PrimaryRemnawaveID > 0 {
		telegram := pgtype.Int8{}
		if source.TelegramID != nil {
			telegram = pgtype.Int8{Int64: *source.TelegramID, Valid: true}
		}
		if err = queries.InsertImportedRemnawaveMapping(ctx, dbgen.InsertImportedRemnawaveMappingParams{
			UserID: customer.ID, RemnawaveID: decision.PrimaryRemnawaveID, TelegramID: telegram,
			ObservedState: mustJSON(map[string]any{"source": "bedolaga"}),
		}); err != nil {
			if isUniqueViolation(err) {
				return "", codedError{bedolagaimport.CodeRemnawaveAlreadyLinked, errors.New("the Remnawave user or Telegram ID is already mapped to another customer")}
			}
			return "", err
		}
	}
	if source.EmailVerified && source.Email != "" {
		ciphertext, fingerprint, sealErr := runner.service.contacts.SealContact("email", source.Email)
		if sealErr != nil {
			return "", sealErr
		}
		verifiedAt := source.CreatedAt
		if source.EmailVerifiedAt != nil {
			verifiedAt = *source.EmailVerifiedAt
		}
		// An address another customer already holds is left with them; the
		// customer is still imported, reachable through Telegram.
		if _, err = queries.CreateContactChannel(ctx, dbgen.CreateContactChannelParams{
			UserID: customer.ID, Kind: "email", ValueCiphertext: ciphertext, ValueFingerprint: fingerprint,
			VerifiedAt: timestamp(verifiedAt), TransactionalEnabled: true, MarketingEnabled: false,
		}); err != nil && !isNoRows(err) {
			return "", err
		}
	}
	// Cabinet sign-in links (Google, Yandex, Discord, VK ID), bound to the
	// issuer of the matching Omniflow preset. A subject another customer
	// already holds stays with them.
	for slug, subject := range source.OAuth {
		issuer, known := bedolagaimport.OAuthIssuers[slug]
		if !known {
			continue
		}
		if _, err = queries.LinkCustomerOIDCIdentity(ctx, dbgen.LinkCustomerOIDCIdentityParams{
			UserID: customer.ID, Provider: customerauth.OIDCProvider(slug), ProviderSubject: subject,
			VerifiedAt: timestamp(source.CreatedAt), OidcIssuer: pgtype.Text{String: issuer, Valid: true},
		}); err != nil && !isNoRows(err) {
			return "", err
		}
	}
	if decision.Status == "suspended" {
		if _, err = queries.InsertCustomerLifecycleEvent(ctx, dbgen.InsertCustomerLifecycleEventParams{
			UserID: customer.ID, Action: "suspended", Reason: "Blocked in Bedolaga before the migration",
			ActorType: "system", ActorID: pgtype.Text{String: runner.service.options.Actor, Valid: true},
		}); err != nil {
			return "", err
		}
	}
	if err = queries.EnsureBotPreferences(ctx, dbgen.EnsureBotPreferencesParams{UserID: customer.ID, Locale: decision.Locale}); err != nil {
		return "", err
	}
	if err = runner.record(ctx, queries, bedolagaimport.KindUser, source.ID, bedolagaimport.ItemApplied, nil, decision.Fingerprint, customer.ID, pgtype.UUID{}, pgtype.UUID{}); err != nil {
		return "", err
	}
	return uuidString(customer.ID), nil
}

// ---------------------------------------------------------------------------
// Balances
// ---------------------------------------------------------------------------

func (runner *applier) balance(ctx context.Context, decision bedolagaimport.BalanceDecision) error {
	if decision.Action != bedolagaimport.ActionCreate && decision.Action != bedolagaimport.ActionRefresh {
		return nil
	}
	customerID := runner.customers[decision.SourceUserID]
	if customerID == "" {
		// The user itself failed earlier in this run; it is already reported.
		return nil
	}
	if err := runner.step(); err != nil {
		return err
	}
	status, err := dbgen.New(runner.service.pool).GetCustomer(ctx, optionalUUID(customerID))
	if err != nil {
		return runner.fail(ctx, "balance", decision.SourceUserID, [32]byte{}, err)
	}
	if status.Status == "merged" || status.Status == "deleted" {
		return runner.fail(ctx, "balance", decision.SourceUserID, [32]byte{}, codedError{"customer_" + status.Status, errors.New("the customer this balance belongs to is no longer an active account")})
	}
	reason := runner.mapping.WalletReason
	if decision.WalletKind() == "correction" {
		reason += " (updated)"
	}
	if err = runner.service.wallet.AdjustWallet(ctx, customerID, decision.Currency, decision.DeltaMinor,
		decision.WalletKind(), decision.Reference(), decision.IdempotencyKey(), reason, runner.service.options.Actor); err != nil {
		return runner.fail(ctx, "balance", decision.SourceUserID, [32]byte{}, err)
	}
	runner.result.Written["balance"]++
	return nil
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

func (runner *applier) subscription(ctx context.Context, decision bedolagaimport.SubscriptionDecision) error {
	switch decision.Action {
	case bedolagaimport.ActionConflict, bedolagaimport.ActionSkip:
		return runner.note(ctx, bedolagaimport.KindSubscription, decision.Source.ID, decision.Action, decision.Codes, decision.Fingerprint)
	case bedolagaimport.ActionCreate, bedolagaimport.ActionRefresh:
	default:
		return nil
	}
	customerID := runner.customers[decision.Source.UserID]
	if customerID == "" {
		return nil
	}
	if err := runner.step(); err != nil {
		return err
	}
	err := runner.inTx(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		return runner.writeSubscription(ctx, queries, decision, optionalUUID(customerID))
	})
	if err != nil {
		var coded codedError
		if decision.Action == bedolagaimport.ActionRefresh && errors.As(err, &coded) {
			// The subscription stays imported; only the refresh is refused,
			// and the applied item is left exactly as it was.
			runner.result.Failures = append(runner.result.Failures, Failure{Kind: bedolagaimport.KindSubscription, SourceID: decision.Source.ID, Code: coded.code, Error: err.Error()})
			return ctx.Err()
		}
		return runner.fail(ctx, bedolagaimport.KindSubscription, decision.Source.ID, decision.Fingerprint, err)
	}
	runner.result.Written[bedolagaimport.KindSubscription]++
	return nil
}

func (runner *applier) writeSubscription(ctx context.Context, queries *dbgen.Queries, decision bedolagaimport.SubscriptionDecision, customerID pgtype.UUID) error {
	source, remote := decision.Source, decision.Remote
	item, err := queries.LockBedolagaImportItem(ctx, dbgen.LockBedolagaImportItemParams{Kind: bedolagaimport.KindSubscription, SourceID: source.ID})
	if err != nil && !isNoRows(err) {
		return err
	}
	applied := err == nil && item.Status == bedolagaimport.ItemApplied
	if applied && decision.Action == bedolagaimport.ActionCreate {
		// Another run got here first.
		return nil
	}
	observed := mustJSON(map[string]any{"status": remote.Status, "expireAt": remote.ExpireAt, "username": remote.Username})
	subscriptionID, entitlementID := item.SubscriptionID, item.EntitlementID
	if !applied || !subscriptionID.Valid {
		subscription, err := queries.InsertImportedSubscription(ctx, dbgen.InsertImportedSubscriptionParams{
			UserID: customerID, Label: decision.Label, RemnawaveUserID: pgtype.Int8{Int64: remote.ID, Valid: true},
			RemnawaveUsername: pgtype.Text{String: remote.Username, Valid: remote.Username != "" && len(remote.Username) <= 64},
			ObservedState:     observed, CreatedAt: timestamp(source.CreatedAt),
		})
		if err != nil {
			if isUniqueViolation(err) {
				return codedError{bedolagaimport.CodeRemnawaveAlreadyLinked, errors.New("another subscription already owns this Remnawave user")}
			}
			return err
		}
		subscriptionID = subscription.ID
	}
	if decision.Active {
		squads, err := databaseutil.ParseUUIDs(decision.SquadIDs)
		if err != nil {
			return fmt.Errorf("Remnawave squad identifiers: %w", err)
		}
		traffic, devices := pgtype.Int8{}, pgtype.Int4{}
		if decision.TrafficBytes != nil {
			traffic = pgtype.Int8{Int64: *decision.TrafficBytes, Valid: true}
		}
		if decision.DeviceLimit != nil {
			devices = pgtype.Int4{Int32: int32(*decision.DeviceLimit), Valid: true}
		}
		if entitlementID.Valid {
			if _, err = queries.RefreshImportedEntitlement(ctx, dbgen.RefreshImportedEntitlementParams{
				EntitlementID: entitlementID, Status: decision.EntitlementStatus, EndsAt: timestamp(decision.EndsAt),
				TrafficAllowanceBytes: traffic, DeviceLimit: devices, RemnawaveSquadIds: squads, ObservedState: observed,
			}); err != nil {
				if isNoRows(err) {
					return codedError{bedolagaimport.CodeOmniflowOwnsEntitlement, errors.New("Omniflow has already acted on this entitlement; it is no longer refreshed from Bedolaga")}
				}
				return err
			}
		} else {
			if entitlementID, err = runner.createEntitlement(ctx, queries, decision, customerID, subscriptionID, squads, traffic, devices, observed); err != nil {
				return err
			}
		}
	}
	return runner.record(ctx, queries, bedolagaimport.KindSubscription, source.ID, bedolagaimport.ItemApplied, nil, decision.Fingerprint, customerID, subscriptionID, entitlementID)
}

func (runner *applier) createEntitlement(
	ctx context.Context, queries *dbgen.Queries, decision bedolagaimport.SubscriptionDecision,
	customerID, subscriptionID pgtype.UUID, squads []pgtype.UUID, traffic pgtype.Int8, devices pgtype.Int4, observed []byte,
) (pgtype.UUID, error) {
	version, ok := runner.versions[decision.PlanVersion.ID]
	if !ok {
		return pgtype.UUID{}, codedError{bedolagaimport.CodeUnknownPlanVersion, errors.New("the mapped plan version no longer exists")}
	}
	order, err := queries.InsertImportedOrder(ctx, dbgen.InsertImportedOrderParams{
		UserID: customerID, Currency: runner.mapping.Currency,
		IdempotencyKey: fmt.Sprintf("bedolaga:subscription:%d", decision.Source.ID),
		SubscriptionID: subscriptionID, CreatedAt: timestamp(decision.StartsAt),
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	snapshot := mustJSON(map[string]any{
		"planCode": version.PlanCode, "planKind": version.PlanKind, "version": version.Version,
		"billingPeriod": version.BillingPeriod, "durationSeconds": version.DurationSeconds,
		"trafficAllowanceBytes": nullable(traffic.Valid, traffic.Int64), "deviceLimit": nullable(devices.Valid, devices.Int32),
		"squadIds": decision.SquadIDs, "planSquadIds": databaseutil.UUIDStrings(version.RemnawaveSquadIds),
		"upgradePolicy": version.UpgradePolicy, "downgradePolicy": version.DowngradePolicy,
		"cancellationPolicy": version.CancellationPolicy,
		"source":             "bedolaga", "bedolagaSubscriptionId": decision.Source.ID, "bedolagaTariffId": decision.Source.TariffID,
	})
	if _, err = queries.InsertOrderLine(ctx, dbgen.InsertOrderLineParams{
		OrderID: order.ID, PlanID: version.PlanID, PlanVersionID: version.ID, UnitAmountMinor: 0, Snapshot: snapshot,
	}); err != nil {
		return pgtype.UUID{}, err
	}
	entitlement, err := queries.InsertImportedEntitlement(ctx, dbgen.InsertImportedEntitlementParams{
		UserID: customerID, OrderID: order.ID, PlanVersionID: version.ID, Status: decision.EntitlementStatus,
		StartsAt: timestamp(decision.StartsAt), EndsAt: timestamp(decision.EndsAt),
		TrafficAllowanceBytes: traffic, DeviceLimit: devices, RemnawaveSquadIds: squads,
		RemnawaveUserID: pgtype.Int8{Int64: decision.Remote.ID, Valid: true}, SubscriptionID: subscriptionID,
		ObservedState: observed,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	if version.PlanKind == "trial" {
		// The trial Bedolaga already gave counts as this customer's one trial.
		if _, err = queries.ClaimTrial(ctx, dbgen.ClaimTrialParams{UserID: customerID, PlanID: version.PlanID, OrderID: order.ID}); err != nil {
			return pgtype.UUID{}, err
		}
	}
	return entitlement.ID, nil
}

// ---------------------------------------------------------------------------
// Referrals
// ---------------------------------------------------------------------------

func (runner *applier) referral(ctx context.Context, decision bedolagaimport.ReferralDecision) error {
	switch decision.Action {
	case bedolagaimport.ActionConflict, bedolagaimport.ActionSkip:
		return runner.note(ctx, bedolagaimport.KindReferral, decision.RefereeID, decision.Action, decision.Codes, decision.Fingerprint)
	case bedolagaimport.ActionCreate:
	default:
		return nil
	}
	referee, referrer := runner.customers[decision.RefereeID], runner.customers[decision.ReferrerID]
	if referee == "" || referrer == "" {
		return nil
	}
	if err := runner.step(); err != nil {
		return err
	}
	refereeID, referrerID := optionalUUID(referee), optionalUUID(referrer)
	err := runner.inTx(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		qualifiedAt := pgtype.Timestamptz{}
		if decision.Qualified {
			qualifiedAt = timestamp(runner.service.options.Clock())
		}
		_, err := queries.InsertImportedReferral(ctx, dbgen.InsertImportedReferralParams{
			ReferredUserID: refereeID, ReferrerUserID: referrerID, Code: decision.Code,
			CreatedAt: timestamp(decision.CreatedAt), QualifiedAt: qualifiedAt,
		})
		if isNoRows(err) {
			var holder pgtype.UUID
			if err = tx.QueryRow(ctx, `SELECT referrer_user_id FROM referral_attributions WHERE referred_user_id = $1`, refereeID).Scan(&holder); err != nil {
				return err
			}
			if holder != referrerID {
				return codedError{bedolagaimport.CodeAlreadyAttributed, errors.New("the customer is already attributed to another referrer")}
			}
		} else if err != nil {
			return err
		}
		return runner.record(ctx, queries, bedolagaimport.KindReferral, decision.RefereeID, bedolagaimport.ItemApplied, nil, decision.Fingerprint, refereeID, pgtype.UUID{}, pgtype.UUID{})
	})
	if err != nil {
		return runner.fail(ctx, bedolagaimport.KindReferral, decision.RefereeID, decision.Fingerprint, err)
	}
	runner.result.Written[bedolagaimport.KindReferral]++
	return nil
}

func nullable[T any](valid bool, value T) any {
	if !valid {
		return nil
	}
	return value
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
