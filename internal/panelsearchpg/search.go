// Package panelsearchpg runs the command palette's record search against
// PostgreSQL.
//
// It receives a parsed query and the list of groups the caller may see, and
// never decides either: what a string means is internal/panelsearch, and what
// a role may read is internal/rbac. A group outside the allowed list is not
// queried at all, so a role's search costs only what it could have read.
package panelsearchpg

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/panelsearch"
)

// Service is the search adapter.
type Service struct {
	pool *pgxpool.Pool
	// fingerprintKey is APP_DATA_ENCRYPTION_KEY, the key contact fingerprints
	// are computed with. An address is found by recomputing its fingerprint,
	// never by reading or decrypting stored values.
	fingerprintKey []byte
}

// New builds the adapter.
func New(pool *pgxpool.Pool, encryptionKey []byte) (*Service, error) {
	if pool == nil {
		return nil, errors.New("a database pool is required")
	}
	if len(encryptionKey) != 32 {
		return nil, errors.New("search fingerprint key must be 32 bytes")
	}
	return &Service{pool: pool, fingerprintKey: append([]byte(nil), encryptionKey...)}, nil
}

// Customer is a customer search hit.
type Customer struct {
	ID         string
	Status     string
	CreatedAt  time.Time
	TelegramID int64
	MatchedBy  string
}

// Order is an order search hit.
type Order struct {
	ID         string
	CustomerID string
	State      string
	Operation  string
	Currency   string
	TotalMinor int64
	CreatedAt  time.Time
	Provider   string
	MatchedBy  string
}

// Ticket is a support ticket search hit.
type Ticket struct {
	ID            string
	CustomerID    string
	Subject       string
	Status        string
	LastMessageAt time.Time
	MatchedBy     string
}

// Gift is a gift found by its code.
type Gift struct {
	ID        string
	Status    string
	Kind      string
	CodeHint  string
	ExpiresAt time.Time
}

// AccessCode is a wholesale access code found by its code.
type AccessCode struct {
	ID             string
	Status         string
	CodeHint       string
	BatchID        string
	BatchReference string
}

// Results are the grouped hits. A group the caller may not see is nil, which
// transport leaves out of the response entirely.
type Results struct {
	Customers   []Customer
	Orders      []Order
	Tickets     []Ticket
	Gifts       []Gift
	AccessCodes []AccessCode
	// HandleNotStored is set when the input was a Telegram @handle, which
	// Omniflow does not record, so the palette can say why nothing matched
	// instead of implying there is no such customer.
	HandleNotStored bool
}

// Search runs every lookup the query implies for the allowed groups.
func (service *Service) Search(
	ctx context.Context, query panelsearch.Query, allowed []panelsearch.Group,
) (Results, error) {
	queries := dbgen.New(service.pool)
	limit := int32(panelsearch.GroupLimit)
	results := Results{HandleNotStored: query.Handle != ""}
	var err error

	if slices.Contains(allowed, panelsearch.GroupCustomers) {
		if results.Customers, err = service.customers(ctx, queries, query, limit); err != nil {
			return Results{}, err
		}
	}
	if slices.Contains(allowed, panelsearch.GroupOrders) {
		if results.Orders, err = orders(ctx, queries, query, limit); err != nil {
			return Results{}, err
		}
	}
	if slices.Contains(allowed, panelsearch.GroupTickets) {
		if results.Tickets, err = tickets(ctx, queries, query, limit); err != nil {
			return Results{}, err
		}
	}
	if slices.Contains(allowed, panelsearch.GroupGifts) {
		results.Gifts = []Gift{}
		if query.CodeHash != nil {
			rows, giftErr := queries.PanelSearchGiftByCodeHash(ctx, query.CodeHash)
			if giftErr != nil {
				return Results{}, giftErr
			}
			for _, row := range rows {
				results.Gifts = append(results.Gifts, Gift{
					ID: uuidString(row.ID), Status: row.Status, Kind: row.Kind,
					CodeHint: row.CodeHint, ExpiresAt: row.ExpiresAt.Time,
				})
			}
		}
	}
	if slices.Contains(allowed, panelsearch.GroupAccessCodes) {
		results.AccessCodes = []AccessCode{}
		if query.CodeHash != nil {
			rows, codeErr := queries.PanelSearchAccessCodeByCodeHash(ctx, query.CodeHash)
			if codeErr != nil {
				return Results{}, codeErr
			}
			for _, row := range rows {
				results.AccessCodes = append(results.AccessCodes, AccessCode{
					ID: uuidString(row.ID), Status: row.Status, CodeHint: row.CodeHint,
					BatchID: uuidString(row.BatchID), BatchReference: row.BatchReference,
				})
			}
		}
	}
	return results, nil
}

type customerRow struct {
	ID         pgtype.UUID
	Status     string
	CreatedAt  pgtype.Timestamptz
	TelegramID pgtype.Int8
}

func (service *Service) customers(
	ctx context.Context, queries *dbgen.Queries, query panelsearch.Query, limit int32,
) ([]Customer, error) {
	found := make([]Customer, 0, limit)
	add := func(rows []customerRow, matchedBy string) {
		for _, row := range rows {
			id := uuidString(row.ID)
			if len(found) >= int(limit) || slices.ContainsFunc(found, func(hit Customer) bool { return hit.ID == id }) {
				continue
			}
			found = append(found, Customer{
				ID: id, Status: row.Status, CreatedAt: row.CreatedAt.Time,
				TelegramID: row.TelegramID.Int64, MatchedBy: matchedBy,
			})
		}
	}

	// The most specific interpretations run first, so when one string means
	// two things the exact hit is the one listed first.
	if query.Email != "" {
		mac := hmac.New(sha256.New, service.fingerprintKey)
		_, _ = mac.Write([]byte(query.Email))
		rows, err := queries.PanelSearchCustomersByEmailFingerprint(ctx, dbgen.PanelSearchCustomersByEmailFingerprintParams{
			Fingerprint: mac.Sum(nil), MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		add(convert(rows, func(row dbgen.PanelSearchCustomersByEmailFingerprintRow) customerRow {
			return customerRow(row)
		}), "email")
	}
	if query.TelegramID != 0 {
		rows, err := queries.PanelSearchCustomersByTelegramID(ctx, dbgen.PanelSearchCustomersByTelegramIDParams{
			TelegramID: query.TelegramID, TelegramSubject: strconv.FormatInt(query.TelegramID, 10), MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		add(convert(rows, func(row dbgen.PanelSearchCustomersByTelegramIDRow) customerRow {
			return customerRow(row)
		}), "telegramId")
	}
	if query.ID != nil {
		low, high, err := uuidBounds(*query.ID)
		if err != nil {
			return nil, err
		}
		rows, err := queries.PanelSearchCustomersByIDRange(ctx, dbgen.PanelSearchCustomersByIDRangeParams{
			Low: low, High: high, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		add(convert(rows, func(row dbgen.PanelSearchCustomersByIDRangeRow) customerRow {
			return customerRow(row)
		}), "id")
	}
	if query.Username != "" {
		candidates := []string{query.Username}
		if lower := strings.ToLower(query.Username); lower != query.Username {
			candidates = append(candidates, lower)
		}
		rows, err := queries.PanelSearchCustomersByRemnawaveUsername(ctx, dbgen.PanelSearchCustomersByRemnawaveUsernameParams{
			Usernames: candidates, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		add(convert(rows, func(row dbgen.PanelSearchCustomersByRemnawaveUsernameRow) customerRow {
			return customerRow(row)
		}), "remnawaveUsername")
	}
	return found, nil
}

func orders(ctx context.Context, queries *dbgen.Queries, query panelsearch.Query, limit int32) ([]Order, error) {
	found := make([]Order, 0, limit)
	seen := func(id string) bool {
		return slices.ContainsFunc(found, func(hit Order) bool { return hit.ID == id })
	}
	if query.Reference != "" {
		rows, err := queries.PanelSearchOrdersByProviderReference(ctx, dbgen.PanelSearchOrdersByProviderReferenceParams{
			Reference: query.Reference, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := uuidString(row.ID); !seen(id) && len(found) < int(limit) {
				found = append(found, Order{
					ID: id, CustomerID: uuidString(row.UserID), State: row.State, Operation: row.Operation,
					Currency: row.Currency, TotalMinor: row.TotalMinor, CreatedAt: row.CreatedAt.Time,
					Provider: row.Provider, MatchedBy: "providerReference",
				})
			}
		}
	}
	if query.ID != nil {
		low, high, err := uuidBounds(*query.ID)
		if err != nil {
			return nil, err
		}
		rows, err := queries.PanelSearchOrdersByIDRange(ctx, dbgen.PanelSearchOrdersByIDRangeParams{
			Low: low, High: high, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := uuidString(row.ID); !seen(id) && len(found) < int(limit) {
				found = append(found, Order{
					ID: id, CustomerID: uuidString(row.UserID), State: row.State, Operation: row.Operation,
					Currency: row.Currency, TotalMinor: row.TotalMinor, CreatedAt: row.CreatedAt.Time,
					MatchedBy: "id",
				})
			}
		}
	}
	return found, nil
}

func tickets(ctx context.Context, queries *dbgen.Queries, query panelsearch.Query, limit int32) ([]Ticket, error) {
	found := make([]Ticket, 0, limit)
	seen := func(id string) bool {
		return slices.ContainsFunc(found, func(hit Ticket) bool { return hit.ID == id })
	}
	if query.ID != nil {
		low, high, err := uuidBounds(*query.ID)
		if err != nil {
			return nil, err
		}
		rows, err := queries.PanelSearchTicketsByIDRange(ctx, dbgen.PanelSearchTicketsByIDRangeParams{
			Low: low, High: high, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := uuidString(row.ID); !seen(id) && len(found) < int(limit) {
				found = append(found, Ticket{
					ID: id, CustomerID: uuidString(row.UserID), Subject: row.Subject, Status: row.Status,
					LastMessageAt: row.LastMessageAt.Time, MatchedBy: "id",
				})
			}
		}
	}
	// An identifier, an address, or a handle is not a phrase anybody puts in
	// a subject line; searching subjects for one only adds noise.
	if query.SubjectPattern != "" && query.ID == nil && query.Email == "" && query.Handle == "" {
		rows, err := queries.PanelSearchTicketsBySubject(ctx, dbgen.PanelSearchTicketsBySubjectParams{
			Pattern: query.SubjectPattern, MaxRows: limit,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if id := uuidString(row.ID); !seen(id) && len(found) < int(limit) {
				found = append(found, Ticket{
					ID: id, CustomerID: uuidString(row.UserID), Subject: row.Subject, Status: row.Status,
					LastMessageAt: row.LastMessageAt.Time, MatchedBy: "subject",
				})
			}
		}
	}
	return found, nil
}

func convert[Row any](rows []Row, into func(Row) customerRow) []customerRow {
	converted := make([]customerRow, 0, len(rows))
	for _, row := range rows {
		converted = append(converted, into(row))
	}
	return converted
}

func uuidBounds(id panelsearch.UUIDRange) (pgtype.UUID, pgtype.UUID, error) {
	var low, high pgtype.UUID
	if err := low.Scan(id.Low); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	if err := high.Scan(id.High); err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return low, high, nil
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
