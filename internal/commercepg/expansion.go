package commercepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commerce"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// assertOperational refuses a new purchase while the installation is in
// maintenance. It reads the state rather than upserting it, so the hot purchase
// path never contends on the singleton row.
func (store *Store) assertOperational(ctx context.Context, queries *dbgen.Queries) error {
	state, err := queries.ReadMaintenanceState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	maintenance := commerce.Maintenance{Active: state.Active, Source: state.Source, Reason: state.Reason}
	if maintenance.Blocks(commerce.ActionPurchase) {
		return fmt.Errorf("%w: %s", ErrMaintenance, state.Source)
	}
	return nil
}

// Maintenance reads the installation-wide maintenance record.
func (store *Store) Maintenance(ctx context.Context) (commerce.Maintenance, error) {
	state, err := dbgen.New(store.pool).ReadMaintenanceState(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return commerce.Maintenance{}, nil
	}
	if err != nil {
		return commerce.Maintenance{}, err
	}
	return commerce.Maintenance{
		Active: state.Active, Source: state.Source, Reason: state.Reason,
		NoticeRU: state.NoticeRu, NoticeEN: state.NoticeEn,
		ExpectedReturnAt: state.ExpectedReturnAt.Time, ActivatedAt: state.ActivatedAt.Time,
	}, nil
}

// SetMaintenance activates or clears maintenance mode and records the decision
// so an operator can explain the outage afterwards.
func (store *Store) SetMaintenance(ctx context.Context, desired commerce.Maintenance, actorType, actorID string) (commerce.Maintenance, error) {
	if actorType != "operator" && actorType != "system" {
		return commerce.Maintenance{}, errors.New("maintenance changes require an operator or system actor")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return commerce.Maintenance{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	current, currentErr := queries.ReadMaintenanceState(ctx)
	if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
		return commerce.Maintenance{}, currentErr
	}
	expected := pgtype.Timestamptz{}
	if !desired.ExpectedReturnAt.IsZero() {
		expected = pgtype.Timestamptz{Time: desired.ExpectedReturnAt, Valid: true}
	}
	state, err := queries.SetMaintenanceState(ctx, dbgen.SetMaintenanceStateParams{
		Active: desired.Active, Source: desired.Source, Reason: desired.Reason,
		NoticeRu: desired.NoticeRU, NoticeEn: desired.NoticeEN, ExpectedReturnAt: expected,
	})
	if err != nil {
		return commerce.Maintenance{}, err
	}
	if errors.Is(currentErr, pgx.ErrNoRows) || current.Active != state.Active {
		action := "cleared"
		if state.Active {
			action = "activated"
		}
		if _, err = queries.InsertMaintenanceEvent(ctx, dbgen.InsertMaintenanceEventParams{
			Action: action, Source: state.Source, Reason: state.Reason,
			ActorType: actorType, ActorID: optionalText(actorID),
		}); err != nil {
			return commerce.Maintenance{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return commerce.Maintenance{}, err
	}
	return commerce.Maintenance{
		Active: state.Active, Source: state.Source, Reason: state.Reason,
		NoticeRU: state.NoticeRu, NoticeEN: state.NoticeEn,
		ExpectedReturnAt: state.ExpectedReturnAt.Time, ActivatedAt: state.ActivatedAt.Time,
	}, nil
}

// resolveSubscription decides which subscription an order changes, creating a
// new one only when the caller asked for it and the concurrency policy allows.
func (store *Store) resolveSubscription(ctx context.Context, queries *dbgen.Queries, userID pgtype.UUID, plan dbgen.GetPlanVersionForOrderRow, input CreateOrderInput) (pgtype.UUID, error) {
	if input.SubscriptionID != "" {
		subscriptionID, err := parseUUID(input.SubscriptionID)
		if err != nil {
			return pgtype.UUID{}, ErrSubscriptionUnknown
		}
		subscription, err := queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: subscriptionID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && subscription.Status != "active" {
			return pgtype.UUID{}, ErrSubscriptionUnknown
		}
		if err != nil {
			return pgtype.UUID{}, err
		}
		return subscription.ID, nil
	}
	// The transaction-scoped advisory lock is taken before the count, so two
	// concurrent purchases cannot both observe a stale total and both pass the
	// limit check. It is released when this transaction ends.
	if err := queries.LockCustomerSubscriptions(ctx, uuidString(userID)); err != nil {
		return pgtype.UUID{}, err
	}
	counts, err := queries.CountActiveSubscriptions(ctx, dbgen.CountActiveSubscriptionsParams{UserID: userID, PlanID: plan.PlanID})
	if err != nil {
		return pgtype.UUID{}, err
	}
	wantsNew := input.NewSubscription && commerce.TargetsNewSubscription(input.Operation)
	if counts.TotalCount > 0 && !wantsNew {
		primary, primaryErr := queries.GetPrimarySubscription(ctx, userID)
		if primaryErr != nil {
			return pgtype.UUID{}, primaryErr
		}
		return primary.ID, nil
	}
	if counts.TotalCount > 0 {
		var planMax *int
		if plan.MaxConcurrentPerCustomer.Valid {
			value := int(plan.MaxConcurrentPerCustomer.Int32)
			planMax = &value
		}
		if err = store.options.Subscriptions.AllowAdditional(int(counts.TotalCount), int(counts.PlanCount), planMax); err != nil {
			return pgtype.UUID{}, err
		}
	}
	label := input.SubscriptionLabel
	if label == "" {
		label = commerce.DefaultSubscriptionLabel(int(counts.TotalCount) + 1)
	}
	normalized, err := commerce.NormalizeSubscriptionLabel(label)
	if err != nil {
		return pgtype.UUID{}, err
	}
	subscription, err := queries.CreateSubscription(ctx, dbgen.CreateSubscriptionParams{UserID: userID, Label: normalized})
	if err != nil {
		return pgtype.UUID{}, err
	}
	return subscription.ID, nil
}

// recordOrderRevenue writes the double-entry movement for a settled order:
// the wallet and the provider hand money to the platform.
func (store *Store) recordOrderRevenue(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, idempotencyKey string) error {
	settledMinor := order.WalletMinor + order.ExternalMinor
	if settledMinor <= 0 {
		return nil
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{Type: "payment", ReferenceType: "order", ReferenceID: uuidString(order.ID), IdempotencyKey: idempotencyKey})
	if err != nil {
		return err
	}
	if order.WalletMinor > 0 {
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: order.UserID, Currency: order.Currency, AmountMinor: -order.WalletMinor}); err != nil {
			return err
		}
	}
	if order.ExternalMinor > 0 {
		if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "provider_clearing", Currency: order.Currency, AmountMinor: -order.ExternalMinor}); err != nil {
			return err
		}
	}
	_, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "platform_clearing", Currency: order.Currency, AmountMinor: settledMinor})
	return err
}

// ---------------------------------------------------------------------------
// Wallet top-up
// ---------------------------------------------------------------------------

// TopUpInput is a customer-initiated wallet funding request.
type TopUpInput struct {
	CustomerID     string
	Currency       string
	AmountMinor    int64
	IdempotencyKey string
	ExpiresAt      time.Time
}

// CreateTopUpOrder records a wallet top-up as an ordinary order so the whole
// payment, webhook, reconciliation, and refund pipeline applies to it unchanged.
// The wallet itself is never spent on a top-up.
func (store *Store) CreateTopUpOrder(ctx context.Context, input TopUpInput) (dbgen.Order, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("customer ID: %w", err)
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{UserID: userID, IdempotencyKey: input.IdempotencyKey}); existingErr == nil {
		if existing.Operation != "topup" || existing.Currency != input.Currency || existing.SubtotalMinor != input.AmountMinor {
			return dbgen.Order{}, errors.New("idempotency key was already used with different top-up parameters")
		}
		return existing, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}
	credited, err := queries.SumRecentTopups(ctx, dbgen.SumRecentTopupsParams{UserID: userID, Currency: input.Currency, WindowSeconds: store.options.TopUp.Window.Seconds()})
	if err != nil {
		return dbgen.Order{}, err
	}
	if reason, validateErr := store.options.TopUp.Validate(input.AmountMinor, credited); validateErr != nil {
		return dbgen.Order{}, fmt.Errorf("%w: %s", validateErr, reason)
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = store.clock().Add(time.Hour)
	}
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: string(commerce.OrderPending), Operation: "topup",
		Currency: input.Currency, SubtotalMinor: input.AmountMinor, DiscountMinor: 0,
		WalletMinor: 0, ExternalMinor: input.AmountMinor, IdempotencyKey: input.IdempotencyKey,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
		// The column is NOT NULL and the insert always names it, so the column
		// default never applies: a nil slice would encode as NULL.
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	if _, err = queries.CreateWalletTopup(ctx, dbgen.CreateWalletTopupParams{OrderID: order.ID, UserID: userID, Currency: input.Currency, RequestedMinor: input.AmountMinor}); err != nil {
		return dbgen.Order{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

// creditTopUp turns a settled top-up order into wallet credit. The ledger
// transaction is keyed on the order, so a replayed webhook, a reconciliation
// poll, and a manual approval can never credit the same top-up twice.
func (store *Store) creditTopUp(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, correlationID string) error {
	topup, err := queries.LockWalletTopup(ctx, order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("top-up order has no top-up record")
	}
	if err != nil {
		return err
	}
	if topup.CreditedAt.Valid {
		return nil
	}
	settlement, err := commerce.SettleTopUp(topup.RequestedMinor, order.PaidMinor)
	if err != nil {
		return err
	}
	if settlement.CreditedMinor <= 0 {
		return nil
	}
	entries := []commerce.LedgerEntry{
		{AccountType: "customer_wallet", CustomerID: uuidString(order.UserID), Currency: order.Currency, AmountMinor: settlement.CreditedMinor},
		{AccountType: "provider_clearing", Currency: order.Currency, AmountMinor: -settlement.CreditedMinor},
	}
	if err = commerce.ValidateLedger(entries); err != nil {
		return err
	}
	transaction, err := queries.CreateLedgerTransaction(ctx, dbgen.CreateLedgerTransactionParams{
		Type: "credit", ReferenceType: "wallet_topup", ReferenceID: uuidString(order.ID),
		IdempotencyKey: "topup:" + uuidString(order.ID),
		Reason:         pgtype.Text{String: "wallet top-up " + settlement.Classification, Valid: true},
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "customer_wallet", UserID: order.UserID, Currency: order.Currency, AmountMinor: settlement.CreditedMinor}); err != nil {
		return err
	}
	if _, err = queries.InsertLedgerEntry(ctx, dbgen.InsertLedgerEntryParams{TransactionID: transaction.ID, AccountType: "provider_clearing", Currency: order.Currency, AmountMinor: -settlement.CreditedMinor}); err != nil {
		return err
	}
	if _, err = queries.CreditWalletTopup(ctx, dbgen.CreditWalletTopupParams{OrderID: order.ID, CreditedMinor: settlement.CreditedMinor, LedgerTransactionID: transaction.ID}); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"orderId": uuidString(order.ID), "customerId": uuidString(order.UserID),
		"currency": order.Currency, "creditedMinor": settlement.CreditedMinor,
		"classification": settlement.Classification, "correlationId": correlationID,
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{Topic: "wallet.credited", Payload: payload}); err != nil {
		return err
	}
	store.notifyOperator(ctx, queries, "topup", "topup:"+uuidString(order.ID), map[string]any{
		"orderId": uuidString(order.ID), "currency": order.Currency,
		"creditedMinor": settlement.CreditedMinor, "classification": settlement.Classification,
	})
	return nil
}

// TopUpHistory lists a customer's top-ups with the payment state attached, so a
// pending, failed, expired, or duplicate attempt is all visible in one screen.
func (store *Store) TopUpHistory(ctx context.Context, customerID string, limit int) ([]dbgen.ListWalletTopupsRow, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	return dbgen.New(store.pool).ListWalletTopups(ctx, dbgen.ListWalletTopupsParams{UserID: userID, RowLimit: int32(limit)})
}

// TopUpAllowance reports how much a customer may still credit inside the
// configured rolling window.
func (store *Store) TopUpAllowance(ctx context.Context, customerID, currency string) (int64, error) {
	userID, err := parseUUID(customerID)
	if err != nil {
		return 0, err
	}
	return dbgen.New(store.pool).SumRecentTopups(ctx, dbgen.SumRecentTopupsParams{UserID: userID, Currency: currency, WindowSeconds: store.options.TopUp.Window.Seconds()})
}

// ---------------------------------------------------------------------------
// Add-ons
// ---------------------------------------------------------------------------

// AddonSelection is one add-on line a customer wants.
type AddonSelection struct {
	AddonVersionID string
	Quantity       int
}

// AddonOrderInput is a mid-period add-on purchase against one subscription.
type AddonOrderInput struct {
	CustomerID     string
	SubscriptionID string
	Currency       string
	Addons         []AddonSelection
	IdempotencyKey string
	ExpiresAt      time.Time
	SkipWallet     bool
}

// CreateAddonOrder prices and records a mid-period add-on purchase. Prices come
// from the add-on version that is current right now and are snapshotted onto the
// order, so a later catalog change never rewrites this order.
func (store *Store) CreateAddonOrder(ctx context.Context, input AddonOrderInput) (dbgen.Order, error) {
	userID, err := parseUUID(input.CustomerID)
	if err != nil {
		return dbgen.Order{}, fmt.Errorf("customer ID: %w", err)
	}
	if len(input.Addons) == 0 {
		return dbgen.Order{}, ErrAddonUnavailable
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	if existing, existingErr := queries.GetOrderByIdempotency(ctx, dbgen.GetOrderByIdempotencyParams{UserID: userID, IdempotencyKey: input.IdempotencyKey}); existingErr == nil {
		if existing.Operation != "addon" || existing.Currency != input.Currency {
			return dbgen.Order{}, errors.New("idempotency key was already used with different add-on parameters")
		}
		return existing, tx.Commit(ctx)
	} else if !errors.Is(existingErr, pgx.ErrNoRows) {
		return dbgen.Order{}, existingErr
	}
	if err = store.assertOperational(ctx, queries); err != nil {
		return dbgen.Order{}, err
	}
	subscriptionID := pgtype.UUID{}
	if input.SubscriptionID != "" {
		if subscriptionID, err = parseUUID(input.SubscriptionID); err != nil {
			return dbgen.Order{}, ErrSubscriptionUnknown
		}
		if _, err = queries.GetCustomerSubscription(ctx, dbgen.GetCustomerSubscriptionParams{SubscriptionID: subscriptionID, UserID: userID}); err != nil {
			return dbgen.Order{}, ErrSubscriptionUnknown
		}
	}
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: userID, SubscriptionID: subscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Order{}, ErrNoActiveSubscription
	}
	if err != nil {
		return dbgen.Order{}, err
	}
	if !subscriptionID.Valid {
		subscriptionID = entitlement.SubscriptionID
	}
	charges, subtotal, err := store.priceAddons(ctx, queries, entitlement, input)
	if err != nil {
		return dbgen.Order{}, err
	}
	walletBalance := int64(0)
	if !input.SkipWallet {
		if walletBalance, err = queries.GetAvailableWalletBalance(ctx, dbgen.GetAvailableWalletBalanceParams{TargetUserID: userID, TargetCurrency: input.Currency}); err != nil {
			return dbgen.Order{}, err
		}
	}
	domainOrder, err := commerce.NewOrder("", input.CustomerID, commerce.Money{Amount: subtotal, Currency: input.Currency}, 0, walletBalance)
	if err != nil {
		return dbgen.Order{}, err
	}
	state := string(commerce.OrderPending)
	if domainOrder.ExternalMinor == 0 {
		state = string(commerce.OrderPaid)
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = store.clock().Add(time.Hour)
	}
	order, err := queries.CreateOrder(ctx, dbgen.CreateOrderParams{
		UserID: userID, State: state, Operation: "addon", Currency: input.Currency,
		SubtotalMinor: subtotal, DiscountMinor: 0, WalletMinor: domainOrder.WalletMinor,
		ExternalMinor: domainOrder.ExternalMinor, IdempotencyKey: input.IdempotencyKey,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, SubscriptionID: subscriptionID,
		// An add-on order changes capacity, never the squad set, but the column
		// is NOT NULL and a nil slice would encode as NULL.
		SelectedSquadIds: noSquads(),
	})
	if err != nil {
		return dbgen.Order{}, err
	}
	if err = store.insertAddonLines(ctx, queries, order.ID, charges, entitlement.EndsAt.Time); err != nil {
		return dbgen.Order{}, err
	}
	if state == string(commerce.OrderPaid) {
		if err = store.settlePaidOrder(ctx, tx, queries, order, "wallet:"+input.IdempotencyKey); err != nil {
			return dbgen.Order{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.Order{}, err
	}
	return order, nil
}

type pricedAddon struct {
	row    dbgen.GetAddonVersionForOrderRow
	charge commerce.AddonCharge
}

// priceInitialAddons prices the add-ons bought together with a plan. There is no
// entitlement to prorate against yet — the add-on covers the whole first period
// — so every line is charged at its catalog price.
func (store *Store) priceInitialAddons(ctx context.Context, queries *dbgen.Queries, plan dbgen.GetPlanVersionForOrderRow, input CreateOrderInput) ([]pricedAddon, int64, error) {
	if len(input.Addons) == 0 {
		return nil, 0, nil
	}
	priced := make([]pricedAddon, 0, len(input.Addons))
	total := int64(0)
	seen := make(map[string]struct{}, len(input.Addons))
	for _, selection := range input.Addons {
		row, err := store.addonVersionForPlan(ctx, queries, plan.ID, plan.Currency, selection.AddonVersionID, seen)
		if err != nil {
			return nil, 0, err
		}
		charge, err := commerce.PriceAddon(row.AmountMinor, selection.Quantity, int(row.MaxQuantity), commerce.ProrationFullPrice, store.clock(), time.Time{}, time.Time{})
		if err != nil {
			return nil, 0, err
		}
		total += charge.ChargedMinor
		priced = append(priced, pricedAddon{row: row, charge: charge})
	}
	return priced, total, nil
}

// addonVersionForPlan resolves one add-on version and refuses anything the plan
// does not offer, is archived, is retired, or is unpriced in this currency.
func (store *Store) addonVersionForPlan(ctx context.Context, queries *dbgen.Queries, planVersionID pgtype.UUID, currency, addonVersionID string, seen map[string]struct{}) (dbgen.GetAddonVersionForOrderRow, error) {
	versionID, err := parseUUID(addonVersionID)
	if err != nil {
		return dbgen.GetAddonVersionForOrderRow{}, ErrAddonUnavailable
	}
	if _, duplicate := seen[addonVersionID]; duplicate {
		return dbgen.GetAddonVersionForOrderRow{}, errors.New("the same add-on was requested twice in one order")
	}
	seen[addonVersionID] = struct{}{}
	offered, err := queries.IsAddonOfferedForPlan(ctx, dbgen.IsAddonOfferedForPlanParams{PlanVersionID: planVersionID, AddonVersionID: versionID})
	if err != nil {
		return dbgen.GetAddonVersionForOrderRow{}, err
	}
	if !offered {
		return dbgen.GetAddonVersionForOrderRow{}, ErrAddonUnavailable
	}
	row, err := queries.GetAddonVersionForOrder(ctx, dbgen.GetAddonVersionForOrderParams{AddonVersionID: versionID, Currency: currency})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetAddonVersionForOrderRow{}, ErrAddonUnavailable
	}
	if err != nil {
		return dbgen.GetAddonVersionForOrderRow{}, err
	}
	if row.ArchivedAt.Valid || row.RetiredAt.Valid {
		return dbgen.GetAddonVersionForOrderRow{}, ErrAddonUnavailable
	}
	return row, nil
}

// insertAddonLines snapshots every priced add-on onto the order, so a later
// catalog change never rewrites what this order charged.
func (store *Store) insertAddonLines(ctx context.Context, queries *dbgen.Queries, orderID pgtype.UUID, priced []pricedAddon, periodEndsAt time.Time) error {
	for _, charge := range priced {
		snapshot, err := json.Marshal(map[string]any{
			"addonCode": charge.row.Code, "addonKind": charge.row.Kind, "version": charge.row.Version,
			"trafficBytes": nullableInt8(charge.row.TrafficBytes), "deviceSlots": nullableInt4(charge.row.DeviceSlots),
			"squadIds": databaseutil.UUIDStrings(charge.row.RemnawaveSquadIds), "proration": string(charge.charge.Proration),
			"periodEndsAt": periodEndsAt,
		})
		if err != nil {
			return err
		}
		if _, err = queries.InsertOrderAddonLine(ctx, dbgen.InsertOrderAddonLineParams{
			OrderID: orderID, AddonID: charge.row.AddonID, AddonVersionID: charge.row.ID,
			Quantity: int32(charge.charge.Quantity), UnitAmountMinor: charge.charge.UnitAmountMinor,
			ChargedMinor: charge.charge.ChargedMinor, Proration: string(charge.charge.Proration), Snapshot: snapshot,
		}); err != nil {
			return err
		}
	}
	return nil
}

// priceAddons validates every requested add-on against the plan it is being
// added to and prices it with that add-on version's own proration rule.
func (store *Store) priceAddons(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, input AddonOrderInput) ([]pricedAddon, int64, error) {
	now := store.clock().UTC()
	priced := make([]pricedAddon, 0, len(input.Addons))
	subtotal := int64(0)
	seen := make(map[string]struct{}, len(input.Addons))
	for _, selection := range input.Addons {
		row, err := store.addonVersionForPlan(ctx, queries, entitlement.PlanVersionID, input.Currency, selection.AddonVersionID, seen)
		if err != nil {
			return nil, 0, err
		}
		// A mid-period add-on is priced with its own version's proration rule
		// against the time the current period has left.
		charge, err := commerce.PriceAddon(row.AmountMinor, selection.Quantity, int(row.MaxQuantity), commerce.Proration(row.Proration), now, entitlement.StartsAt.Time, entitlement.EndsAt.Time)
		if err != nil {
			return nil, 0, err
		}
		subtotal += charge.ChargedMinor
		priced = append(priced, pricedAddon{row: row, charge: charge})
	}
	return priced, subtotal, nil
}

// applyAddonLines folds an order's add-on capacity into one entitlement and
// returns the updated row. It is idempotent: `entitlement_addons` is unique on
// (order, add-on version), so a replayed settlement finds the row already
// present, skips it, and leaves the totals untouched.
func (store *Store) applyAddonLines(ctx context.Context, queries *dbgen.Queries, order dbgen.Order, entitlement dbgen.Entitlement) (dbgen.Entitlement, error) {
	lines, err := queries.ListOrderAddonLines(ctx, order.ID)
	if err != nil || len(lines) == 0 {
		return entitlement, err
	}
	extraTraffic := int64(0)
	extraDevices := int32(0)
	extraSquads := make([]pgtype.UUID, 0, 4)
	applied := false
	for _, line := range lines {
		row, versionErr := queries.GetAddonVersionForOrder(ctx, dbgen.GetAddonVersionForOrderParams{AddonVersionID: line.AddonVersionID, Currency: order.Currency})
		if versionErr != nil {
			return entitlement, versionErr
		}
		capacity := commerce.Capacity(row.TrafficBytes.Int64, int(row.DeviceSlots.Int32), int(line.Quantity), databaseutil.UUIDStrings(row.RemnawaveSquadIds))
		_, insertErr := queries.InsertEntitlementAddon(ctx, dbgen.InsertEntitlementAddonParams{
			EntitlementID: entitlement.ID, OrderID: order.ID, AddonVersionID: line.AddonVersionID,
			Quantity:          line.Quantity,
			TrafficBytes:      pgtype.Int8{Int64: capacity.TrafficBytes, Valid: row.TrafficBytes.Valid},
			DeviceSlots:       pgtype.Int4{Int32: int32(capacity.DeviceSlots), Valid: row.DeviceSlots.Valid},
			RemnawaveSquadIds: row.RemnawaveSquadIds,
		})
		if errors.Is(insertErr, pgx.ErrNoRows) {
			// Already applied by an earlier settlement attempt.
			continue
		}
		if insertErr != nil {
			return entitlement, insertErr
		}
		applied = true
		extraTraffic += capacity.TrafficBytes
		extraDevices += int32(capacity.DeviceSlots)
		extraSquads = append(extraSquads, row.RemnawaveSquadIds...)
	}
	if !applied {
		return entitlement, nil
	}
	return queries.ApplyEntitlementAddonTotals(ctx, dbgen.ApplyEntitlementAddonTotalsParams{
		EntitlementID: entitlement.ID, ExtraTrafficBytes: extraTraffic,
		ExtraDeviceSlots: extraDevices, ExtraSquadIds: extraSquads,
	})
}

// applyOrderAddons settles a mid-period add-on order against the subscription's
// live entitlement and enqueues one fulfillment operation for the combined
// result.
func (store *Store) applyOrderAddons(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, order dbgen.Order, correlationID string) error {
	entitlement, err := queries.GetLatestEntitlementForChange(ctx, dbgen.GetLatestEntitlementForChangeParams{UserID: order.UserID, SubscriptionID: order.SubscriptionID})
	if errors.Is(err, pgx.ErrNoRows) {
		// The subscription disappeared between purchase and settlement. The
		// payment stays recorded and an operator resolves it; nothing here may
		// roll back money that has already been taken.
		return store.recordAddonOrphan(ctx, queries, order)
	}
	if err != nil {
		return err
	}
	updated, err := store.applyAddonLines(ctx, queries, order, entitlement)
	if err != nil {
		return err
	}
	if updated.ID == entitlement.ID && updated.UpdatedAt == entitlement.UpdatedAt {
		// Every line was already applied by an earlier settlement attempt.
		return nil
	}
	desired, err := json.Marshal(map[string]any{
		"effectiveAt": store.clock().UTC(), "endsAt": updated.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(updated.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(updated.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(updated.RemnawaveSquadIds),
	})
	if err != nil {
		return err
	}
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: updated.ID, Operation: "set_limits",
		IdempotencyKey: "order:" + uuidString(order.ID) + ":addon", CorrelationID: correlationID,
		DesiredState: desired,
	})
	if err != nil {
		return err
	}
	if _, err = queries.InsertOutboxEvent(ctx, dbgen.InsertOutboxEventParams{Topic: "addon.purchased", Payload: []byte(fmt.Sprintf(`{"orderId":%q,"entitlementId":%q}`, uuidString(order.ID), uuidString(updated.ID)))}); err != nil {
		return err
	}
	if _, err = queries.SetOrderState(ctx, dbgen.SetOrderStateParams{ID: order.ID, State: string(commerce.OrderFulfilled)}); err != nil {
		return err
	}
	if store.enqueue != nil {
		return store.enqueue(ctx, tx, uuidString(operation.ID))
	}
	return nil
}

// recordAddonOrphan records that a paid add-on had nothing to attach to. The
// order keeps its paid state so the money is never silently lost, and the audit
// event tells an operator exactly what to refund or re-apply.
func (store *Store) recordAddonOrphan(ctx context.Context, queries *dbgen.Queries, order dbgen.Order) error {
	metadata, err := json.Marshal(map[string]any{"orderId": uuidString(order.ID), "currency": order.Currency, "paidMinor": order.PaidMinor})
	if err != nil {
		return err
	}
	if _, err = queries.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ActorType: "system", Action: "addon.no_active_entitlement", TargetType: "order",
		TargetID: uuidString(order.ID),
		Reason:   pgtype.Text{String: "add-on settled without a live entitlement", Valid: true},
		Metadata: metadata,
	}); err != nil {
		return err
	}
	_, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
		Kind: "fulfillment_failure", DedupeKey: "addon-orphan:" + uuidString(order.ID), Payload: metadata,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

// notifyOperator queues an operational notice inside the caller's transaction,
// so a notice exists exactly when the event it describes is durable. The dedupe
// key collapses a retried webhook or a second reconciliation pass into one
// message. A queue failure never fails the transaction that caused it.
func (store *Store) notifyOperator(ctx context.Context, queries *dbgen.Queries, kind, dedupeKey string, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		store.options.Logger.Warn("operator notice could not be encoded", "kind", kind, "error", err)
		return
	}
	if _, err = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{Kind: kind, DedupeKey: dedupeKey, Payload: encoded}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		store.options.Logger.Warn("operator notice could not be queued", "kind", kind, "error", err)
	}
}

// operatorKindForOrder maps an order operation onto the topic that should carry
// it, so renewals do not drown the purchase stream.
func operatorKindForOrder(operation string) string {
	switch operation {
	case "extension":
		return "renewal"
	case "topup":
		return "topup"
	default:
		return "purchase"
	}
}

func optionalText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

// noSquads is an empty, non-nil squad set. pgx encodes a nil slice as NULL and
// an empty one as '{}', and every array column here is NOT NULL.
func noSquads() []pgtype.UUID { return []pgtype.UUID{} }
