package accountcheckout

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/gifts"
)

// ErrGiftInvalid refuses a gift the store would not sell: no plan, a plan in
// another currency, or a message that is too long.
var ErrGiftInvalid = errors.New("gift request is not valid")

// GiftInput is a subscription gift bought on the web.
type GiftInput struct {
	PlanVersionID string
	Currency      string
	Message       string
	// SkipWallet pays the whole gift through a provider.
	SkipWallet bool
	// Recipient is a Telegram @username or an e-mail address the gift is
	// delivered to once it is paid; empty leaves the code to the sender.
	Recipient string
	// Locale is the language a mailed recipient is written to in.
	Locale string
}

// ErrGiftEmailUnavailable refuses an e-mail recipient on an installation that
// cannot seal one.
var ErrGiftEmailUnavailable = errors.New("gift_email_unavailable")

// BoughtGift is a gift as its sender sees it. Code is set only on the
// response that created it: only the hash is stored.
type BoughtGift struct {
	ID          string     `json:"id"`
	OrderID     string     `json:"orderId"`
	Code        string     `json:"code,omitempty"`
	CodeHint    string     `json:"codeHint"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Currency    string     `json:"currency"`
	CreditMinor *int64     `json:"creditMinor,omitempty"`
	OrderState  string     `json:"orderState,omitempty"`
	ExternalDue int64      `json:"externalMinor"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	ClaimedAt   *time.Time `json:"claimedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	// Recipient is "@username" or "email" for an addressed gift; the
	// address itself is never shown back.
	Recipient string `json:"recipient,omitempty"`
	// Delivery is how far an addressed gift got: waiting, delivered, closed.
	Delivery string `json:"delivery,omitempty"`
}

// BuyGift opens a subscription gift order for the sender, exactly as the bot
// does: the wallet pays what it can and whatever remains is paid through the
// order's payment route. The code is returned once, now.
func (service *Service) BuyGift(ctx context.Context, customerID, idempotencyKey string, input GiftInput) (BoughtGift, error) {
	if strings.TrimSpace(input.PlanVersionID) == "" || len([]rune(input.Message)) > 500 {
		return BoughtGift{}, ErrGiftInvalid
	}
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = service.settings.Currency
	}
	recipient, err := service.giftRecipient(input)
	if err != nil {
		return BoughtGift{}, err
	}
	purchase, err := service.orders.CreateGiftOrder(ctx, commercepg.GiftOrderInput{
		SenderID: customerID, Kind: gifts.KindSubscription, PlanVersionID: input.PlanVersionID,
		Currency: currency, SenderMessage: input.Message, IdempotencyKey: idempotencyKey,
		SkipWallet: input.SkipWallet, Recipient: recipient,
	})
	if err != nil {
		return BoughtGift{}, err
	}
	gift := boughtGift(purchase.Gift)
	gift.Code = purchase.Code
	if recipient.Username != "" {
		gift.Recipient, gift.Delivery = "@"+recipient.Username, "waiting"
	} else if len(recipient.EmailCiphertext) > 0 {
		gift.Recipient, gift.Delivery = "email", "waiting"
	}
	gift.OrderState = purchase.Order.State
	gift.ExternalDue = purchase.Order.ExternalMinor
	return gift, nil
}

// GiftsSent lists the gifts the customer bought, newest first.
func (service *Service) GiftsSent(ctx context.Context, customerID string) ([]BoughtGift, error) {
	return service.listGifts(ctx, customerID, false)
}

// GiftsReceived lists the gifts the customer claimed.
func (service *Service) GiftsReceived(ctx context.Context, customerID string) ([]BoughtGift, error) {
	return service.listGifts(ctx, customerID, true)
}

func (service *Service) listGifts(ctx context.Context, customerID string, received bool) ([]BoughtGift, error) {
	var id pgtype.UUID
	if err := id.Scan(customerID); err != nil {
		return nil, err
	}
	queries := dbgen.New(service.store.pool)
	var rows []dbgen.Gift
	var err error
	if received {
		rows, err = queries.ListGiftsReceived(ctx, dbgen.ListGiftsReceivedParams{RecipientUserID: id, PageSize: 50})
	} else {
		rows, err = queries.ListGiftsForSender(ctx, dbgen.ListGiftsForSenderParams{SenderUserID: id, PageSize: 50})
	}
	if err != nil {
		return nil, err
	}
	result := make([]BoughtGift, 0, len(rows))
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		result = append(result, boughtGift(row))
		ids = append(ids, row.ID)
	}
	if received || len(ids) == 0 {
		return result, nil
	}
	addressed, err := service.store.pool.Query(ctx, `SELECT gift_id::text,
			CASE WHEN username IS NOT NULL THEN '@' || username ELSE 'email' END, status
		FROM gift_recipients WHERE gift_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer addressed.Close()
	recipients := map[string][2]string{}
	for addressed.Next() {
		var id, recipient, status string
		if err = addressed.Scan(&id, &recipient, &status); err != nil {
			return nil, err
		}
		recipients[id] = [2]string{recipient, status}
	}
	if err = addressed.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if found, ok := recipients[result[index].ID]; ok {
			result[index].Recipient, result[index].Delivery = found[0], found[1]
		}
	}
	return result, nil
}

func boughtGift(row dbgen.Gift) BoughtGift {
	gift := BoughtGift{
		ID: uuidOf(row.ID), OrderID: uuidOf(row.OrderID), CodeHint: row.CodeHint, Kind: row.Kind,
		Status: row.Status, Currency: row.Currency, ExpiresAt: row.ExpiresAt.Time, CreatedAt: row.CreatedAt.Time,
	}
	if row.CreditMinor.Valid {
		value := row.CreditMinor.Int64
		gift.CreditMinor = &value
	}
	if row.ClaimedAt.Valid {
		claimed := row.ClaimedAt.Time
		gift.ClaimedAt = &claimed
	}
	return gift
}

func uuidOf(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	encoded, _ := value.Value()
	text, _ := encoded.(string)
	return text
}

// giftRecipient reads the recipient a sender typed: a username as it is, an
// e-mail address sealed as a contact is.
func (service *Service) giftRecipient(input GiftInput) (commercepg.GiftRecipient, error) {
	username, email, err := commercepg.ParseGiftRecipient(input.Recipient)
	if err != nil {
		return commercepg.GiftRecipient{}, err
	}
	recipient := commercepg.GiftRecipient{Username: username, Locale: input.Locale}
	if email == "" {
		if username == "" {
			return commercepg.GiftRecipient{}, nil
		}
		return recipient, nil
	}
	if service.contacts == nil {
		return commercepg.GiftRecipient{}, ErrGiftEmailUnavailable
	}
	if recipient.EmailCiphertext, recipient.EmailFingerprint, err = service.contacts.SealContact("email", email); err != nil {
		return commercepg.GiftRecipient{}, err
	}
	return recipient, nil
}
