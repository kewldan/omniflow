package importservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/customer"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

type Service struct {
	pool   *pgxpool.Pool
	source remnawave.Importer
}

func New(pool *pgxpool.Pool, source remnawave.Importer) *Service {
	return &Service{pool: pool, source: source}
}

func (service *Service) Preview(ctx context.Context, importID string, pageSize int) (dbgen.CustomerImport, error) {
	queries := dbgen.New(service.pool)
	var run dbgen.CustomerImport
	var err error
	if importID == "" {
		run, err = queries.CreateCustomerImport(ctx)
	} else {
		id, parseErr := parseUUID(importID)
		if parseErr != nil {
			return dbgen.CustomerImport{}, parseErr
		}
		run, err = queries.GetCustomerImport(ctx, id)
	}
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	if run.Status == "completed" || run.Status == "cancelled" {
		return run, nil
	}
	start := 0
	if run.Cursor.Valid {
		start, _ = strconv.Atoi(run.Cursor.String)
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 100
	}
	remoteUsers, total, err := service.source.ListUsers(ctx, start, pageSize)
	if err != nil {
		return queries.UpdateCustomerImportProgress(ctx, dbgen.UpdateCustomerImportProgressParams{ImportID: run.ID, Status: "failed", Cursor: run.Cursor, TotalCount: run.TotalCount, ValidCount: run.ValidCount, ConflictCount: run.ConflictCount, InvalidCount: run.InvalidCount, ErrorSummary: []byte(`{"code":"remnawave_unavailable"}`)})
	}
	mappings, err := queries.ListRemnawaveMappings(ctx)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	existingSources := make(map[string]struct{}, len(mappings))
	existingTelegram := make(map[int64]struct{}, len(mappings))
	for _, mapping := range mappings {
		existingSources[strconv.FormatInt(mapping.RemnawaveID, 10)] = struct{}{}
		if mapping.TelegramID.Valid {
			existingTelegram[mapping.TelegramID.Int64] = struct{}{}
		}
	}
	identitySubjects, err := queries.ListTelegramIdentitySubjects(ctx)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	for _, subject := range identitySubjects {
		if telegramID, parseErr := strconv.ParseInt(subject, 10, 64); parseErr == nil {
			existingTelegram[telegramID] = struct{}{}
		}
	}
	stagedTelegramIDs, err := queries.ListCustomerImportTelegramIDs(ctx, run.ID)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	currentSourceIDs := make(map[string]struct{}, len(remoteUsers))
	for _, user := range remoteUsers {
		currentSourceIDs[strconv.FormatInt(user.ID, 10)] = struct{}{}
	}
	for _, staged := range stagedTelegramIDs {
		if _, isCurrentPage := currentSourceIDs[staged.SourceID]; !isCurrentPage {
			existingTelegram[staged.TelegramID] = struct{}{}
		}
	}
	candidates := make([]customer.ImportCandidate, 0, len(remoteUsers))
	for _, user := range remoteUsers {
		payload := map[string]any{"remnawaveId": user.ID, "username": user.Username, "status": user.Status, "telegramId": user.TelegramID}
		candidates = append(candidates, customer.ImportCandidate{SourceID: strconv.FormatInt(user.ID, 10), TelegramID: user.TelegramID, Username: user.Username, Payload: payload})
	}
	items := customer.PreviewImport(candidates, existingSources, existingTelegram)
	for _, item := range items {
		errorsJSON, _ := json.Marshal(item.ValidationCodes)
		if _, err = queries.UpsertCustomerImportItem(ctx, dbgen.UpsertCustomerImportItemParams{ImportID: run.ID, SourceID: item.SourceID, Status: item.Status, Fingerprint: item.Fingerprint[:], StagedData: item.StagedData, ValidationErrors: errorsJSON}); err != nil {
			return dbgen.CustomerImport{}, err
		}
	}
	counts, err := queries.GetCustomerImportItemCounts(ctx, run.ID)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	next := start + len(remoteUsers)
	status := "previewing"
	if next >= total {
		status = "ready"
	}
	return queries.UpdateCustomerImportProgress(ctx, dbgen.UpdateCustomerImportProgressParams{ImportID: run.ID, Status: status, Cursor: pgtype.Text{String: strconv.Itoa(next), Valid: true}, TotalCount: int32(total), ValidCount: counts.ValidCount, ConflictCount: counts.ConflictCount, InvalidCount: counts.InvalidCount, ErrorSummary: []byte(`{}`)})
}

func (service *Service) Apply(ctx context.Context, importID string, limit int) (dbgen.CustomerImport, error) {
	id, err := parseUUID(importID)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	queries := dbgen.New(service.pool)
	run, err := queries.GetCustomerImport(ctx, id)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	if run.Status != "ready" && run.Status != "applying" {
		return dbgen.CustomerImport{}, errors.New("import is not ready")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items, err := queries.ListCustomerImportItems(ctx, dbgen.ListCustomerImportItemsParams{ImportID: id, Status: pgtype.Text{String: "valid", Valid: true}, PageSize: int32(limit)})
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	for _, item := range items {
		var staged struct {
			RemnawaveID int64  `json:"remnawaveId"`
			TelegramID  *int64 `json:"telegramId"`
			Status      string `json:"status"`
		}
		if err := json.Unmarshal(item.StagedData, &staged); err != nil {
			return dbgen.CustomerImport{}, err
		}
		tx, beginErr := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if beginErr != nil {
			return dbgen.CustomerImport{}, beginErr
		}
		txQueries := dbgen.New(tx)
		observed, _ := json.Marshal(map[string]any{"status": staged.Status})
		_, applyErr := txQueries.ApplyCustomerImportItem(ctx, dbgen.ApplyCustomerImportItemParams{ImportID: id, SourceID: item.SourceID, Locale: "ru", RemnawaveID: staged.RemnawaveID, TelegramID: optionalInt8(staged.TelegramID), ObservedState: observed})
		if applyErr != nil {
			_ = tx.Rollback(ctx)
			return dbgen.CustomerImport{}, fmt.Errorf("apply import item %s: %w", item.SourceID, applyErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return dbgen.CustomerImport{}, commitErr
		}
	}
	remaining, err := queries.ListCustomerImportItems(ctx, dbgen.ListCustomerImportItemsParams{ImportID: id, Status: pgtype.Text{String: "valid", Valid: true}, PageSize: 1})
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	status := "applying"
	if len(remaining) == 0 {
		status = "completed"
	}
	return queries.UpdateCustomerImportProgress(ctx, dbgen.UpdateCustomerImportProgressParams{ImportID: id, Status: status, Cursor: run.Cursor, TotalCount: run.TotalCount, ValidCount: run.ValidCount, ConflictCount: run.ConflictCount, InvalidCount: run.InvalidCount, ErrorSummary: run.ErrorSummary})
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, errors.New("invalid import ID")
	}
	return id, nil
}

func optionalInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}
