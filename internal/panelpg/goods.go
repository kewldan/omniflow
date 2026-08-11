package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/goods"
)

// GoodsProvider is one digital-goods provider as the panel shows it.
//
// The credential is absent; `CredentialsSet` reports only whether one is
// stored. The balance and the threshold are present because a shop that keeps
// selling after its funding source runs dry produces paid orders that can only
// be refunded, and that is the number an operator has to be able to see.
type GoodsProvider struct {
	Slug                string     `json:"slug"`
	Enabled             bool       `json:"enabled"`
	CredentialsSet      bool       `json:"credentialsSet"`
	BalanceMinor        *int64     `json:"balanceMinor,omitempty"`
	BalanceCurrency     string     `json:"balanceCurrency,omitempty"`
	LowBalanceThreshold *int64     `json:"lowBalanceThresholdMinor,omitempty"`
	SpendLimitMinor     int64      `json:"spendLimitMinor"`
	SpendWindowSeconds  int64      `json:"spendWindowSeconds"`
	Status              string     `json:"status"`
	LastErrorCode       string     `json:"lastErrorCode,omitempty"`
	LastCheckedAt       *time.Time `json:"lastCheckedAt,omitempty"`
	// LowBalance is derived rather than stored, so the flag can never disagree
	// with the two numbers it is computed from.
	LowBalance bool `json:"lowBalance"`
}

// GoodsProviderInput is a panel save.
type GoodsProviderInput struct {
	Slug                string
	Enabled             bool
	Credentials         *string
	LowBalanceThreshold *int64
	SpendLimitMinor     int64
	SpendWindowSeconds  int64
}

// ListGoodsProviders reads every configured digital-goods provider.
func (service *Service) ListGoodsProviders(ctx context.Context) ([]GoodsProvider, error) {
	rows, err := service.queries().ListGoodsProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]GoodsProvider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, goodsProviderFrom(row))
	}
	return providers, nil
}

// SaveGoodsProvider stores a provider configuration.
func (service *Service) SaveGoodsProvider(
	ctx context.Context, input GoodsProviderInput, actor Actor,
) (GoodsProvider, error) {
	if input.SpendWindowSeconds <= 0 || input.SpendLimitMinor < 0 {
		return GoodsProvider{}, ErrValidaton
	}

	var credentials []byte
	if input.Credentials != nil {
		sealed, err := service.sealSecret(*input.Credentials, SecretGoodsProvider)
		if err != nil {
			return GoodsProvider{}, err
		}
		credentials = sealed
	}

	var saved GoodsProvider
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertGoodsProvider(ctx, dbgen.UpsertGoodsProviderParams{
			Slug:                     input.Slug,
			Enabled:                  input.Enabled,
			CredentialsCiphertext:    credentials,
			LowBalanceThresholdMinor: optionalInt8(input.LowBalanceThreshold),
			SpendLimitMinor:          input.SpendLimitMinor,
			SpendWindowSeconds:       input.SpendWindowSeconds,
		})
		if txErr != nil {
			return txErr
		}
		saved = goodsProviderFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.provider_updated", "configuration", "goods_provider", input.Slug,
			map[string]any{
				"enabled": input.Enabled, "spendLimitMinor": input.SpendLimitMinor,
				"credentialsRotated": input.Credentials != nil,
			},
		))
	})
	return saved, err
}

// GoodsProviderCredential returns the decrypted token for an adapter to use.
func (service *Service) GoodsProviderCredential(ctx context.Context, slug string) (string, error) {
	row, err := service.queries().GetGoodsProvider(ctx, slug)
	if err != nil {
		return "", notFound(err)
	}
	return service.OpenSecret(row.CredentialsCiphertext, SecretGoodsProvider)
}

// RecordGoodsProviderHealth stores the result of a health probe.
func (service *Service) RecordGoodsProviderHealth(
	ctx context.Context, slug, status, errorCode string, balanceMinor *int64, currency string,
) error {
	_, err := service.queries().RecordGoodsProviderHealth(ctx, dbgen.RecordGoodsProviderHealthParams{
		Slug: slug, Status: status, LastErrorCode: optionalText(errorCode),
		BalanceMinor: optionalInt8(balanceMinor), BalanceCurrency: optionalText(currency),
	})
	return notFound(err)
}

// RemainingSpend reports how much of a provider's rolling ceiling is left.
//
// A zero limit means no ceiling, which is reported as an unlimited remainder
// rather than as zero — the two are opposite answers and confusing them would
// stop every delivery on an installation that never configured a limit.
func (service *Service) RemainingSpend(ctx context.Context, slug string) (int64, bool, error) {
	provider, err := service.queries().GetGoodsProvider(ctx, slug)
	if err != nil {
		return 0, false, notFound(err)
	}
	if provider.SpendLimitMinor <= 0 {
		return 0, false, nil
	}
	spent, err := service.queries().SumRecentGoodsSpend(ctx, dbgen.SumRecentGoodsSpendParams{
		ProviderSlug: slug,
		Lookback:     interval(time.Duration(provider.SpendWindowSeconds) * time.Second),
	})
	if err != nil {
		return 0, true, err
	}
	return max(provider.SpendLimitMinor-spent, 0), true, nil
}

func goodsProviderFrom(row dbgen.GoodsProvider) GoodsProvider {
	provider := GoodsProvider{
		Slug:                row.Slug,
		Enabled:             row.Enabled,
		CredentialsSet:      len(row.CredentialsCiphertext) > 0,
		BalanceMinor:        int8Pointer(row.BalanceMinor),
		BalanceCurrency:     textValue(row.BalanceCurrency),
		LowBalanceThreshold: int8Pointer(row.LowBalanceThresholdMinor),
		SpendLimitMinor:     row.SpendLimitMinor,
		SpendWindowSeconds:  row.SpendWindowSeconds,
		Status:              row.Status,
		LastErrorCode:       textValue(row.LastErrorCode),
		LastCheckedAt:       timePointer(row.LastCheckedAt),
	}
	if provider.BalanceMinor != nil && provider.LowBalanceThreshold != nil {
		provider.LowBalance = *provider.BalanceMinor <= *provider.LowBalanceThreshold
	}
	return provider
}

// ---------------------------------------------------------------------------
// Catalogue
// ---------------------------------------------------------------------------

// GoodsProduct is one sellable digital good.
type GoodsProduct struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	ProviderSlug   string     `json:"providerSlug"`
	Kind           string     `json:"kind"`
	DurationMonths *int32     `json:"durationMonths,omitempty"`
	StarQuantity   *int32     `json:"starQuantity,omitempty"`
	Visible        bool       `json:"visible"`
	SortOrder      int32      `json:"sortOrder"`
	CreatedAt      time.Time  `json:"createdAt"`
	ArchivedAt     *time.Time `json:"archivedAt,omitempty"`

	Localizations map[string]Localization `json:"localizations,omitempty"`
	Pricing       *GoodsPricing           `json:"pricing,omitempty"`
}

// GoodsPricing is the rule that turns a provider cost into a customer price.
type GoodsPricing struct {
	Currency         string    `json:"currency"`
	MarkupBPS        int32     `json:"markupBps"`
	Rounding         string    `json:"rounding"`
	FixedAmountMinor *int64    `json:"fixedAmountMinor,omitempty"`
	QuoteTTLSeconds  int32     `json:"quoteTtlSeconds"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ListGoodsProducts reads the shop catalogue as an operator sees it.
func (service *Service) ListGoodsProducts(
	ctx context.Context, includeArchived bool,
) ([]GoodsProduct, error) {
	queries := service.queries()
	rows, err := queries.ListGoodsProducts(ctx, optionalBool(&includeArchived))
	if err != nil {
		return nil, err
	}

	products := make([]GoodsProduct, 0, len(rows))
	for _, row := range rows {
		product := goodsProductFrom(row)

		localizations, localErr := queries.ListGoodsProductLocalizations(ctx, row.ID)
		if localErr != nil {
			return nil, localErr
		}
		product.Localizations = make(map[string]Localization, len(localizations))
		for _, localization := range localizations {
			product.Localizations[localization.Locale] = Localization{
				Name: localization.Name, Description: localization.Description,
			}
		}

		pricing, priceErr := queries.GetGoodsPricing(ctx, row.ID)
		switch {
		case priceErr == nil:
			product.Pricing = &GoodsPricing{
				Currency:         pricing.Currency,
				MarkupBPS:        pricing.MarkupBps,
				Rounding:         pricing.Rounding,
				FixedAmountMinor: int8Pointer(pricing.FixedAmountMinor),
				QuoteTTLSeconds:  pricing.QuoteTtlSeconds,
				UpdatedAt:        timeValue(pricing.UpdatedAt),
			}
		case notFound(priceErr) == ErrNotFound:
			// A product with no pricing rule is not sellable, and the panel
			// shows it as such rather than failing the whole listing.
		default:
			return nil, priceErr
		}

		products = append(products, product)
	}
	return products, nil
}

// GoodsProductInput creates a product.
type GoodsProductInput struct {
	Code           string
	ProviderSlug   string
	Kind           string
	DurationMonths *int32
	StarQuantity   *int32
	Visible        bool
	SortOrder      int32
}

// CreateGoodsProduct adds a product to the shop catalogue.
func (service *Service) CreateGoodsProduct(
	ctx context.Context, input GoodsProductInput, actor Actor,
) (GoodsProduct, error) {
	switch input.Kind {
	case goods.KindTelegramPremium:
		if input.DurationMonths == nil || input.StarQuantity != nil {
			return GoodsProduct{}, ErrValidaton
		}
	case goods.KindTelegramStars:
		if input.StarQuantity == nil || input.DurationMonths != nil {
			return GoodsProduct{}, ErrValidaton
		}
	default:
		return GoodsProduct{}, ErrValidaton
	}

	var created GoodsProduct
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreateGoodsProduct(ctx, dbgen.CreateGoodsProductParams{
			Code:           input.Code,
			ProviderSlug:   input.ProviderSlug,
			Kind:           input.Kind,
			DurationMonths: optionalInt2(input.DurationMonths),
			StarQuantity:   optionalInt4(input.StarQuantity),
			Visible:        input.Visible,
			SortOrder:      input.SortOrder,
		})
		if txErr != nil {
			return conflicted(txErr)
		}
		created = goodsProductFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.product_created", "configuration", "goods_product", input.Code,
			map[string]any{"kind": input.Kind, "provider": input.ProviderSlug},
		))
	})
	return created, err
}

// UpdateGoodsProduct changes visibility, ordering, and archive state.
//
// The kind, duration, and quantity are immutable once created, for the same
// reason a plan version is: an order references the product it bought, and
// changing what that product is would rewrite history.
func (service *Service) UpdateGoodsProduct(
	ctx context.Context, productID string, visible bool, sortOrder int32, archived bool, actor Actor,
) error {
	id, err := parseUUID(productID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpdateGoodsProduct(ctx, dbgen.UpdateGoodsProductParams{
			ProductID: id, Visible: visible && !archived, SortOrder: sortOrder, Archived: archived,
		})
		if txErr != nil {
			return notFound(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.product_updated", "configuration", "goods_product", row.Code,
			map[string]any{"visible": row.Visible, "archived": archived},
		))
	})
}

// SaveGoodsLocalization stores a product's name and description for one locale.
func (service *Service) SaveGoodsLocalization(
	ctx context.Context, productID, locale, name, description string, actor Actor,
) error {
	if locale != "ru" && locale != "en" {
		return ErrValidaton
	}
	id, err := parseUUID(productID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.UpsertGoodsProductLocalization(
			ctx, dbgen.UpsertGoodsProductLocalizationParams{
				ProductID: id, Locale: locale, Name: name, Description: description,
			},
		); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.product_localized", "configuration", "goods_product", productID,
			map[string]any{"locale": locale},
		))
	})
}

// SaveGoodsPricing stores the markup and rounding rule for a product.
func (service *Service) SaveGoodsPricing(
	ctx context.Context, productID string, pricing GoodsPricing, actor Actor,
) error {
	if pricing.MarkupBPS < 0 || pricing.MarkupBPS > 100_000 {
		return ErrValidaton
	}
	if pricing.QuoteTTLSeconds < 30 || pricing.QuoteTTLSeconds > 3600 {
		return ErrValidaton
	}
	switch pricing.Rounding {
	case goods.RoundNone, goods.RoundMinor, goods.RoundUnit,
		goods.RoundTenUnits, goods.RoundHundredUnits:
	default:
		return ErrValidaton
	}
	id, err := parseUUID(productID)
	if err != nil {
		return err
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.UpsertGoodsPricing(ctx, dbgen.UpsertGoodsPricingParams{
			ProductID:        id,
			Currency:         pricing.Currency,
			MarkupBps:        pricing.MarkupBPS,
			Rounding:         pricing.Rounding,
			FixedAmountMinor: optionalInt8(pricing.FixedAmountMinor),
			QuoteTtlSeconds:  pricing.QuoteTTLSeconds,
			UpdatedBy:        optionalUUID(actor.AdminID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.pricing_updated", "configuration", "goods_product", productID,
			map[string]any{
				"currency": pricing.Currency, "markupBps": pricing.MarkupBPS,
				"rounding": pricing.Rounding, "fixed": pricing.FixedAmountMinor != nil,
			},
		))
	})
}

func goodsProductFrom(row dbgen.GoodsProduct) GoodsProduct {
	product := GoodsProduct{
		ID:           uuidString(row.ID),
		Code:         row.Code,
		ProviderSlug: row.ProviderSlug,
		Kind:         row.Kind,
		StarQuantity: int4Pointer(row.StarQuantity),
		Visible:      row.Visible,
		SortOrder:    row.SortOrder,
		CreatedAt:    timeValue(row.CreatedAt),
		ArchivedAt:   timePointer(row.ArchivedAt),
	}
	if row.DurationMonths.Valid {
		months := int32(row.DurationMonths.Int16)
		product.DurationMonths = &months
	}
	return product
}

// ---------------------------------------------------------------------------
// Shop orders
// ---------------------------------------------------------------------------

// GoodsOrder is one shop purchase with its delivery state.
//
// The recipient username is present because support cannot answer "where did it
// go" without it. Nothing else about the recipient is retained anywhere.
type GoodsOrder struct {
	OrderID          string `json:"orderId"`
	CustomerID       string `json:"customerId"`
	ProductID        string `json:"productId"`
	Quantity         int32  `json:"quantity"`
	Recipient        string `json:"recipient"`
	RecipientIsSelf  bool   `json:"recipientIsSelf"`
	QuotedCostMinor  int64  `json:"quotedCostMinor"`
	QuotedPriceMinor int64  `json:"quotedPriceMinor"`
	// MarginMinor is the difference the operator earned. It is derived here
	// rather than recomputed from the markup, which may have changed since.
	MarginMinor      int64      `json:"marginMinor"`
	Currency         string     `json:"currency"`
	Status           string     `json:"status"`
	DeliveryStatus   string     `json:"deliveryStatus,omitempty"`
	DeliveryAttempts *int32     `json:"deliveryAttempts,omitempty"`
	FailureClass     string     `json:"failureClass,omitempty"`
	ErrorCode        string     `json:"errorCode,omitempty"`
	Refunded         bool       `json:"refunded"`
	DeliveredAt      *time.Time `json:"deliveredAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
}

// GoodsOrderFilter is what the shop order list accepts.
type GoodsOrderFilter struct {
	Status     string
	CustomerID string
	Cursor     string
	PageSize   int32
}

// GoodsOrderPage is one page of shop orders.
type GoodsOrderPage struct {
	Items      []GoodsOrder `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// SearchGoodsOrders reads the shop order register.
func (service *Service) SearchGoodsOrders(
	ctx context.Context, filter GoodsOrderFilter,
) (GoodsOrderPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchGoodsOrders(ctx, dbgen.SearchGoodsOrdersParams{
		CursorCreatedAt: cursor.timestamp(),
		CursorID:        cursor.uuid(),
		Status:          optionalText(filter.Status),
		UserID:          optionalUUID(filter.CustomerID),
		PageSize:        size + 1,
	})
	if err != nil {
		return GoodsOrderPage{}, err
	}

	page := GoodsOrderPage{Items: make([]GoodsOrder, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(
				timeValue(last.GoodsOrder.CreatedAt), uuidString(last.GoodsOrder.OrderID),
			)
			break
		}
		page.Items = append(page.Items, GoodsOrder{
			OrderID:          uuidString(row.GoodsOrder.OrderID),
			CustomerID:       uuidString(row.GoodsOrder.UserID),
			ProductID:        uuidString(row.GoodsOrder.ProductID),
			Quantity:         row.GoodsOrder.Quantity,
			Recipient:        row.GoodsOrder.RecipientUsername,
			RecipientIsSelf:  row.GoodsOrder.RecipientIsSelf,
			QuotedCostMinor:  row.GoodsOrder.QuotedCostMinor,
			QuotedPriceMinor: row.GoodsOrder.QuotedPriceMinor,
			MarginMinor:      row.GoodsOrder.QuotedPriceMinor - row.GoodsOrder.QuotedCostMinor,
			Currency:         row.GoodsOrder.Currency,
			Status:           row.GoodsOrder.Status,
			DeliveryStatus:   textValue(row.DeliveryStatus),
			DeliveryAttempts: int4Pointer(row.DeliveryAttempts),
			FailureClass:     textValue(row.DeliveryFailureClass),
			ErrorCode:        textValue(row.DeliveryErrorCode),
			Refunded:         row.RefundLedgerTransactionID.Valid,
			DeliveredAt:      timePointer(row.DeliveredAt),
			CreatedAt:        timeValue(row.GoodsOrder.CreatedAt),
		})
	}
	return page, nil
}

// GoodsDeliveryAttempt is one recorded exchange with a goods provider.
type GoodsDeliveryAttempt struct {
	Attempt       int32     `json:"attempt"`
	Outcome       string    `json:"outcome"`
	FailureClass  string    `json:"failureClass,omitempty"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	CorrelationID string    `json:"correlationId"`
	OccurredAt    time.Time `json:"occurredAt"`
}

// GoodsDeliveryHistory reads the attempt history for one shop order.
func (service *Service) GoodsDeliveryHistory(
	ctx context.Context, orderID string,
) ([]GoodsDeliveryAttempt, error) {
	id, err := parseUUID(orderID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListGoodsDeliveryAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	attempts := make([]GoodsDeliveryAttempt, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, GoodsDeliveryAttempt{
			Attempt:       row.Attempt,
			Outcome:       row.Outcome,
			FailureClass:  textValue(row.FailureClass),
			ErrorCode:     textValue(row.ErrorCode),
			CorrelationID: row.CorrelationID,
			OccurredAt:    timeValue(row.OccurredAt),
		})
	}
	return attempts, nil
}

// CancelGoodsDelivery abandons a delivery that has not completed.
//
// The refund that follows is the ordinary wallet credit the permanent-failure
// path already performs, so cancelling here and failing permanently reach the
// customer in the same way. A delivered order cannot be cancelled: the
// recipient already has what was bought.
func (service *Service) CancelGoodsDelivery(
	ctx context.Context, orderID string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(orderID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.CancelGoodsDelivery(ctx, id); txErr != nil {
			return rejected(txErr)
		}
		if _, txErr := queries.SetGoodsOrderStatus(ctx, dbgen.SetGoodsOrderStatusParams{
			OrderID: id, Status: "failed",
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.goods.delivery_cancelled", "financial", "order", orderID, nil,
		))
	})
}
