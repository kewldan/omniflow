package panelpg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// BanTemplates are the ready-made messages a ban can send, by reason. Each is
// in Russian and English together, because the panel does not choose a
// customer's language for an operator's notice. An operator may write their
// own text instead.
var BanTemplates = map[string]string{
	"torrent": "🚫 Доступ приостановлен: в нашей сети запрещены торренты.\n\n🚫 Access suspended: torrent traffic is not allowed on our network.",
	"sharing": "🚫 Доступ приостановлен: подписка использовалась на большем числе устройств или людьми, чем разрешено.\n\n🚫 Access suspended: the subscription was shared beyond its limit.",
	"spam":    "🚫 Доступ приостановлен: с вашего подключения шла рассылка спама.\n\n🚫 Access suspended: spam was sent through your connection.",
	"fraud":   "🚫 Доступ приостановлен: оплата оспорена или признана мошеннической.\n\n🚫 Access suspended: a payment was disputed or found fraudulent.",
	"rules":   "🚫 Доступ приостановлен за нарушение правил сервиса. Напишите в поддержку, если считаете это ошибкой.\n\n🚫 Access suspended for breaking the service rules. Write to support if you think this is a mistake.",
}

// BanInput is a ban of many customers at once.
type BanInput struct {
	// IDs are customer identifiers or Telegram identifiers, mixed.
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
	// Template names one of BanTemplates; Message replaces it; with neither
	// the customer is not told.
	Template string `json:"template"`
	Message  string `json:"message"`
}

// BanResult is what happened to one identifier.
type BanResult struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId,omitempty"`
	Outcome    string `json:"outcome"`
}

// maxBanList bounds one request.
const maxBanList = 1000

// BanCustomers suspends every customer in the list, with the reason on each
// lifecycle event and audit entry, and optionally sends each the template or
// message. An identifier that names nobody is reported, not fatal.
func (service *Service) BanCustomers(ctx context.Context, input BanInput, actor Actor) ([]BanResult, error) {
	reason := strings.TrimSpace(input.Reason)
	message := strings.TrimSpace(input.Message)
	if message == "" && input.Template != "" {
		var known bool
		if message, known = BanTemplates[input.Template]; !known {
			return nil, ErrValidaton
		}
	}
	if reason == "" || len(input.IDs) == 0 || len(input.IDs) > maxBanList || len([]rune(message)) > 3500 {
		return nil, ErrValidaton
	}
	actor.Reason = reason
	queries := service.queries()
	results := make([]BanResult, 0, len(input.IDs))
	for _, raw := range input.IDs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		result := BanResult{ID: raw, Outcome: "not_found"}
		customerID := ""
		if telegramID, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if found, lookupErr := queries.ResolveCustomerByTelegramID(ctx, telegramID); lookupErr == nil {
				customerID = uuidString(found)
			}
		} else if _, err = parseUUID(raw); err == nil {
			customerID = raw
		}
		if customerID != "" {
			if _, err := service.SetCustomerStatus(ctx, customerID, "suspended", actor); err != nil {
				result.Outcome = "failed"
			} else {
				result.CustomerID, result.Outcome = customerID, "suspended"
				if message != "" {
					if _, err = service.MessageCustomer(ctx, customerID, message, actor); err != nil {
						result.Outcome = "suspended_not_notified"
					}
				}
			}
		}
		results = append(results, result)
	}
	return results, nil
}

// BlockerStatus is the blocker scan card.
type BlockerStatus struct {
	Blocked  int64        `json:"blocked"`
	LastScan *BlockerScan `json:"lastScan,omitempty"`
}

// BlockerScan is one scan's progress.
type BlockerScan struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Checked     int32      `json:"checked"`
	Blocked     int32      `json:"blocked"`
	RequestedAt time.Time  `json:"requestedAt"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// BlockerState reads how many active customers blocked the bot and the last scan.
func (service *Service) BlockerState(ctx context.Context) (BlockerStatus, error) {
	queries := service.queries()
	blocked, err := queries.CountBlockedCustomers(ctx)
	if err != nil {
		return BlockerStatus{}, err
	}
	status := BlockerStatus{Blocked: blocked}
	if scan, scanErr := queries.LatestBlockerScan(ctx); scanErr == nil {
		status.LastScan = &BlockerScan{
			ID: uuidString(scan.ID), Status: scan.Status, Checked: scan.Checked, Blocked: scan.Blocked,
			RequestedAt: scan.RequestedAt.Time, FinishedAt: timePointer(scan.FinishedAt),
		}
	} else if !errors.Is(scanErr, pgx.ErrNoRows) {
		return BlockerStatus{}, scanErr
	}
	return status, nil
}

// RequestBlockerScan asks the bot to check every customer's chat. Only one
// scan waits or runs at a time; asking again while one does is a conflict.
func (service *Service) RequestBlockerScan(ctx context.Context, actor Actor) error {
	requestedBy := pgtype.UUID{}
	if actor.AdminID != "" {
		requestedBy, _ = parseUUID(actor.AdminID)
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, err := queries.RequestBlockerScan(ctx, requestedBy); err != nil {
			return fmt.Errorf("%w: a scan is already waiting or running", ErrConflict)
		}
		return appendAudit(ctx, queries, actor.audit("moderation.blocker_scan.requested", "customer", "blocker_scan", "*", nil))
	})
}

// CleanBlockers suspends the customers who blocked the bot and hold no live
// access, so they leave every audience and list. It returns how many.
func (service *Service) CleanBlockers(ctx context.Context, actor Actor) (int, error) {
	ids, err := service.queries().ListCleanableBlockers(ctx, 5000)
	if err != nil {
		return 0, err
	}
	actor.Reason = "blocked the bot and holds no live access"
	cleaned := 0
	for _, id := range ids {
		if _, err = service.SetCustomerStatus(ctx, uuidString(id), "suspended", actor); err == nil {
			cleaned++
		}
	}
	return cleaned, nil
}
