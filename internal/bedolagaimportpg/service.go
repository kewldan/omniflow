// Package bedolagaimportpg carries out a Bedolaga import plan against the
// Omniflow database.
//
// Every write goes through the tables' own constraints and, for money,
// through commercepg's AdjustWallet — the same audited, idempotent path an
// operator adjustment takes. Each record is its own transaction, so a run that
// dies part-way leaves whole customers and whole subscriptions behind, and the
// item table remembers which ones, so the next run continues from there.
//
// The Remnawave panel is read and never written. The only panel interface
// this package accepts is remnawave.Importer, which can list users and nothing
// else, and no fulfillment operation is created for an imported entitlement,
// so no job exists that could push one.
package bedolagaimportpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/bedolagaimport"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// WalletAdjuster is the audited wallet movement commercepg.Store provides.
type WalletAdjuster interface {
	AdjustWallet(ctx context.Context, customerID, currency string, amount int64, kind, reference, idempotencyKey, reason, actorID string) error
}

// ContactSealer encrypts a contact the way customerpg stores every contact.
type ContactSealer interface {
	SealContact(kind, value string) (ciphertext, fingerprint []byte, err error)
}

// Options configure a Service.
type Options struct {
	// Actor names whoever runs the import in audit events and run records.
	Actor  string
	Logger *slog.Logger
	// RemotePageSize is how many Remnawave users one list call reads.
	RemotePageSize int
	Clock          func() time.Time
}

// Service plans, applies, and verifies a Bedolaga import.
type Service struct {
	pool     *pgxpool.Pool
	wallet   WalletAdjuster
	contacts ContactSealer
	remote   remnawave.Importer
	options  Options
}

// New builds the service. Every dependency is required.
func New(pool *pgxpool.Pool, wallet WalletAdjuster, contacts ContactSealer, remote remnawave.Importer, options Options) (*Service, error) {
	if pool == nil || wallet == nil || contacts == nil || remote == nil {
		return nil, errors.New("bedolagaimportpg: pool, wallet, contacts, and Remnawave reader are required")
	}
	if options.Actor == "" {
		options.Actor = "migrate-bedolaga"
	}
	if len(options.Actor) > 120 {
		return nil, errors.New("bedolagaimportpg: actor must be at most 120 characters")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.RemotePageSize <= 0 || options.RemotePageSize > 500 {
		options.RemotePageSize = 500
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &Service{pool: pool, wallet: wallet, contacts: contacts, remote: remote, options: options}, nil
}

// Plan builds the plan without writing anything.
func (service *Service) Plan(ctx context.Context, source bedolagaimport.Source, mapping bedolagaimport.Mapping) (bedolagaimport.Plan, error) {
	plan, _, err := service.plan(ctx, source, mapping)
	return plan, err
}

func (service *Service) plan(ctx context.Context, source bedolagaimport.Source, mapping bedolagaimport.Mapping) (bedolagaimport.Plan, map[string]dbgen.ListPlanVersionsForImportRow, error) {
	remote, err := service.loadRemote(ctx)
	if err != nil {
		return bedolagaimport.Plan{}, nil, err
	}
	target, versions, err := service.loadTarget(ctx)
	if err != nil {
		return bedolagaimport.Plan{}, nil, err
	}
	return bedolagaimport.BuildPlan(source, remote, target, mapping, service.options.Clock()), versions, nil
}

// loadRemote pages through every Remnawave user. It is the only panel call
// the importer makes.
func (service *Service) loadRemote(ctx context.Context) (map[int64]bedolagaimport.RemoteUser, error) {
	users := map[int64]bedolagaimport.RemoteUser{}
	for start := 0; ; {
		page, total, err := service.remote.ListUsers(ctx, start, service.options.RemotePageSize)
		if err != nil {
			return nil, fmt.Errorf("read Remnawave users: %w", err)
		}
		for _, user := range page {
			squads := make([]string, 0, len(user.ActiveInternalSquads))
			for _, squad := range user.ActiveInternalSquads {
				squads = append(squads, squad.UUID)
			}
			users[user.ID] = bedolagaimport.RemoteUser{
				ID: user.ID, Username: user.Username, Status: user.Status, ExpireAt: user.ExpireAt.UTC(),
				TrafficLimitBytes: user.TrafficLimitBytes, DeviceLimit: user.HWIDDeviceLimit,
				SquadIDs: squads, TelegramID: user.TelegramID,
			}
		}
		start += len(page)
		if len(page) == 0 || start >= total {
			return users, nil
		}
	}
}

func (service *Service) loadTarget(ctx context.Context) (bedolagaimport.Target, map[string]dbgen.ListPlanVersionsForImportRow, error) {
	queries := dbgen.New(service.pool)
	target := bedolagaimport.Target{
		Items: map[string]bedolagaimport.Item{}, TelegramOwners: map[int64]string{},
		RemnawaveOwners: map[int64]string{}, PlanVersions: map[string]bedolagaimport.PlanVersion{},
		Referrers: map[string]string{}, Wallets: map[int64]bedolagaimport.WalletMovements{},
		TrialHolders: map[string]bool{}, LegacyPeriods: map[int64]bedolagaimport.LegacyPeriod{},
		LegacyPayments: map[string]bool{}, LegacyDiscounts: map[string]time.Time{},
		ReferralAliases: map[string]string{},
	}
	items, err := queries.ListBedolagaImportItems(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read import items: %w", err)
	}
	for _, row := range items {
		item := bedolagaimport.Item{
			Kind: row.Kind, SourceID: row.SourceID, Status: row.Status, Codes: row.Codes,
			CustomerID: uuidString(row.UserID), SubscriptionID: uuidString(row.SubscriptionID),
			EntitlementID: uuidString(row.EntitlementID),
		}
		copy(item.Fingerprint[:], row.Fingerprint)
		target.Items[bedolagaimport.ItemKey(row.Kind, row.SourceID)] = item
	}
	owners, err := queries.ListTelegramOwners(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read Telegram identities: %w", err)
	}
	for _, owner := range owners {
		if telegramID, parseErr := strconv.ParseInt(owner.TelegramSubject, 10, 64); parseErr == nil {
			target.TelegramOwners[telegramID] = uuidString(owner.UserID)
		}
	}
	linked, err := queries.ListLinkedRemnawaveUsers(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read Remnawave links: %w", err)
	}
	for _, link := range linked {
		target.RemnawaveOwners[link.RemnawaveID] = uuidString(link.UserID)
	}
	versionRows, err := queries.ListPlanVersionsForImport(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read plan versions: %w", err)
	}
	versions := map[string]dbgen.ListPlanVersionsForImportRow{}
	for _, row := range versionRows {
		id := uuidString(row.ID)
		versions[id] = row
		target.PlanVersions[id] = bedolagaimport.PlanVersion{
			ID: id, PlanID: uuidString(row.PlanID), PlanCode: row.PlanCode, PlanKind: row.PlanKind,
			GraceSeconds: row.GracePeriodSeconds, Retired: row.RetiredAt.Valid,
		}
	}
	pairs, err := queries.ListReferralAttributionPairs(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read referral attributions: %w", err)
	}
	for _, pair := range pairs {
		target.Referrers[uuidString(pair.ReferredUserID)] = uuidString(pair.ReferrerUserID)
	}
	movements, err := queries.SumBedolagaWalletMovements(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried-over balances: %w", err)
	}
	for _, movement := range movements {
		existing, seen := target.Wallets[movement.SourceUserID]
		wallet := bedolagaimport.WalletMovements{
			CustomerID: uuidString(movement.UserID), Currency: movement.Currency,
			TotalMinor: movement.TotalMinor, Count: int(movement.Movements),
		}
		if seen {
			// Movements in two currencies, or onto two customers, cannot be
			// reconciled against one Bedolaga balance; the decision refuses it.
			wallet.Currency = "mixed"
			wallet.TotalMinor += existing.TotalMinor
			wallet.Count += existing.Count
		}
		target.Wallets[movement.SourceUserID] = wallet
	}
	holders, err := queries.ListTrialClaimHolders(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read trial claims: %w", err)
	}
	for _, holder := range holders {
		target.TrialHolders[uuidString(holder)] = true
	}
	periods, err := queries.ListBedolagaLegacyPeriods(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried-over subscription periods: %w", err)
	}
	for _, period := range periods {
		target.LegacyPeriods[period.SourceID] = bedolagaimport.LegacyPeriod{
			IsTrial: period.IsTrial, StartsAt: period.StartsAt.Time.UTC(), EndsAt: period.EndsAt.Time.UTC(),
		}
	}
	payments, err := queries.ListBedolagaLegacyPayments(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried-over payments: %w", err)
	}
	for _, payment := range payments {
		target.LegacyPayments[payment.Source+":"+strconv.FormatInt(payment.SourceID, 10)] = true
	}
	aliases, err := queries.ListBedolagaReferralAliases(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried referral codes: %w", err)
	}
	for _, alias := range aliases {
		target.ReferralAliases[alias.Code] = uuidString(alias.UserID)
	}
	discounts, err := queries.ListLegacyDiscounts(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried-over discounts: %w", err)
	}
	for _, discount := range discounts {
		target.LegacyDiscounts[uuidString(discount.UserID)] = discount.LastDiscountAt.Time.UTC()
	}
	target.AdCampaigns, target.CampaignAttributions, target.LegacyGoods = map[string]bool{}, map[string]string{}, map[int64]bool{}
	codes, err := queries.ListAdCampaignCodes(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read advertising campaigns: %w", err)
	}
	for _, code := range codes {
		target.AdCampaigns[code] = true
	}
	attributions, err := queries.ListCampaignAttributionCodes(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read campaign attributions: %w", err)
	}
	for _, attribution := range attributions {
		target.CampaignAttributions[uuidString(attribution.UserID)] = attribution.Code
	}
	goods, err := queries.ListBedolagaLegacyGoods(ctx)
	if err != nil {
		return target, nil, fmt.Errorf("read carried-over stars orders: %w", err)
	}
	for _, sourceID := range goods {
		target.LegacyGoods[sourceID] = true
	}
	return target, versions, nil
}

// Verify reconciles a fresh plan against the Omniflow tables.
func (service *Service) Verify(ctx context.Context, source bedolagaimport.Source, mapping bedolagaimport.Mapping) (bedolagaimport.VerifyReport, error) {
	plan, err := service.Plan(ctx, source, mapping)
	if err != nil {
		return bedolagaimport.VerifyReport{}, err
	}
	queries := dbgen.New(service.pool)
	input := bedolagaimport.VerifyInput{
		Customers:     map[int64]bedolagaimport.VerifiedCustomer{},
		Subscriptions: map[int64]bedolagaimport.VerifiedSubscription{},
		Referrals:     map[int64]bedolagaimport.VerifiedReferral{},
		Wallets:       map[int64]bedolagaimport.WalletMovements{},
	}
	customers, err := queries.ListImportedCustomersForVerify(ctx)
	if err != nil {
		return bedolagaimport.VerifyReport{}, err
	}
	for _, row := range customers {
		input.Customers[row.SourceID] = bedolagaimport.VerifiedCustomer{CustomerID: uuidString(row.UserID), Status: row.Status, TelegramSubject: row.TelegramSubject}
	}
	subscriptions, err := queries.ListImportedSubscriptionsForVerify(ctx)
	if err != nil {
		return bedolagaimport.VerifyReport{}, err
	}
	for _, row := range subscriptions {
		input.Subscriptions[row.SourceID] = bedolagaimport.VerifiedSubscription{
			CustomerID: uuidString(row.UserID), RemnawaveID: row.RemnawaveUserID.Int64,
			EntitlementStatus: row.EntitlementStatus.String, EndsAt: row.EndsAt.Time.UTC(), HasEntitlement: row.EntitlementID.Valid,
		}
	}
	referrals, err := queries.ListImportedReferralsForVerify(ctx)
	if err != nil {
		return bedolagaimport.VerifyReport{}, err
	}
	for _, row := range referrals {
		input.Referrals[row.SourceID] = bedolagaimport.VerifiedReferral{CustomerID: uuidString(row.UserID), ReferrerCustomerID: uuidString(row.ReferrerUserID)}
	}
	movements, err := queries.SumBedolagaWalletMovements(ctx)
	if err != nil {
		return bedolagaimport.VerifyReport{}, err
	}
	for _, row := range movements {
		wallet := input.Wallets[row.SourceUserID]
		wallet.CustomerID, wallet.Currency = uuidString(row.UserID), row.Currency
		wallet.TotalMinor += row.TotalMinor
		wallet.Count += int(row.Movements)
		input.Wallets[row.SourceUserID] = wallet
	}
	return bedolagaimport.Verify(plan, input, service.options.Clock()), nil
}

// RecentRuns lists the latest apply runs, newest first.
func (service *Service) RecentRuns(ctx context.Context, limit int32) ([]dbgen.BedolagaImportRun, error) {
	return dbgen.New(service.pool).ListBedolagaImportRuns(ctx, limit)
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	strings := databaseutil.UUIDStrings([]pgtype.UUID{value})
	return strings[0]
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID %q", value)
	}
	return id, nil
}

func optionalUUID(value string) pgtype.UUID {
	id, err := parseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: !value.IsZero()}
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`{}`)
	}
	return encoded
}

// isNoRows reports a statement whose guard matched nothing.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
