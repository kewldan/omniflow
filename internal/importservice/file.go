package importservice

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/customer"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// MaxFileRows bounds one uploaded file. An installation larger than this is
// imported from the live panel, which pages.
const MaxFileRows = 50000

// ErrFileFormat refuses a file the importer cannot read.
var ErrFileFormat = errors.New("the file is not a customer import CSV")

// FileRow is one customer read from an uploaded CSV.
type FileRow struct {
	RemnawaveID int64
	TelegramID  *int64
	Username    string
	Status      string
}

// ParseFile reads a CSV with a header row naming at least `remnawave_id`, and
// optionally `telegram_id`, `username`, and `status`. Column order is free, so
// an export from another tool needs its header renamed, not its columns
// reordered. A row whose identifiers do not parse is kept and staged as
// invalid, so the preview counts it rather than the upload failing on it.
func ParseFile(reader io.Reader) ([]FileRow, []int, error) {
	records := csv.NewReader(reader)
	records.FieldsPerRecord = -1
	records.TrimLeadingSpace = true
	header, err := records.Read()
	if err != nil {
		return nil, nil, ErrFileFormat
	}
	columns := map[string]int{}
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))] = index
	}
	remnawaveColumn, ok := columns["remnawave_id"]
	if !ok {
		return nil, nil, fmt.Errorf("%w: a remnawave_id column is required", ErrFileFormat)
	}
	field := func(record []string, name string) string {
		if index, present := columns[name]; present && index < len(record) {
			return strings.TrimSpace(record[index])
		}
		return ""
	}
	var rows []FileRow
	var invalid []int
	for line := 2; ; line++ {
		record, readErr := records.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, nil, ErrFileFormat
		}
		if len(rows)+len(invalid) >= MaxFileRows {
			return nil, nil, fmt.Errorf("%w: more than %d rows", ErrFileFormat, MaxFileRows)
		}
		if remnawaveColumn >= len(record) {
			invalid = append(invalid, line)
			continue
		}
		remnawaveID, parseErr := strconv.ParseInt(strings.TrimSpace(record[remnawaveColumn]), 10, 64)
		if parseErr != nil || remnawaveID <= 0 {
			invalid = append(invalid, line)
			continue
		}
		row := FileRow{RemnawaveID: remnawaveID, Username: field(record, "username"), Status: strings.ToUpper(field(record, "status"))}
		if raw := field(record, "telegram_id"); raw != "" {
			telegramID, telegramErr := strconv.ParseInt(raw, 10, 64)
			if telegramErr != nil {
				invalid = append(invalid, line)
				continue
			}
			row.TelegramID = &telegramID
		}
		rows = append(rows, row)
	}
	return rows, invalid, nil
}

// PreviewFile stages an uploaded file as an import, with the same duplicate
// detection the Remnawave preview applies, and leaves it ready to apply.
func (service *Service) PreviewFile(ctx context.Context, rows []FileRow, invalidLines []int) (dbgen.CustomerImport, error) {
	queries := dbgen.New(service.pool)
	run, err := queries.CreateFileCustomerImport(ctx)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	existingSources, existingTelegram, err := service.existing(ctx, queries)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	candidates := make([]customer.ImportCandidate, 0, len(rows))
	for _, row := range rows {
		payload := map[string]any{"remnawaveId": row.RemnawaveID, "username": row.Username, "status": row.Status, "telegramId": row.TelegramID}
		candidates = append(candidates, customer.ImportCandidate{
			SourceID: strconv.FormatInt(row.RemnawaveID, 10), TelegramID: row.TelegramID, Username: row.Username, Payload: payload,
		})
	}
	for _, item := range customer.PreviewImport(candidates, existingSources, existingTelegram) {
		errorsJSON, _ := json.Marshal(item.ValidationCodes)
		if _, err = queries.UpsertCustomerImportItem(ctx, dbgen.UpsertCustomerImportItemParams{
			ImportID: run.ID, SourceID: item.SourceID, Status: item.Status, Fingerprint: item.Fingerprint[:],
			StagedData: item.StagedData, ValidationErrors: errorsJSON,
		}); err != nil {
			return dbgen.CustomerImport{}, err
		}
	}
	counts, err := queries.GetCustomerImportItemCounts(ctx, run.ID)
	if err != nil {
		return dbgen.CustomerImport{}, err
	}
	summary := []byte(`{}`)
	if len(invalidLines) > 0 {
		summary, _ = json.Marshal(map[string]int{"unreadable_row": len(invalidLines)})
	}
	return queries.UpdateCustomerImportProgress(ctx, dbgen.UpdateCustomerImportProgressParams{
		ImportID: run.ID, Status: "ready", Cursor: pgtype.Text{},
		TotalCount: int32(len(rows) + len(invalidLines)), ValidCount: counts.ValidCount,
		ConflictCount: counts.ConflictCount, InvalidCount: counts.InvalidCount + int32(len(invalidLines)),
		ErrorSummary: summary,
	})
}

// existing reads what is already linked, so a staged row that would collide
// is a conflict rather than a second customer.
func (service *Service) existing(ctx context.Context, queries *dbgen.Queries) (map[string]struct{}, map[int64]struct{}, error) {
	mappings, err := queries.ListRemnawaveMappings(ctx)
	if err != nil {
		return nil, nil, err
	}
	sources := make(map[string]struct{}, len(mappings))
	telegram := make(map[int64]struct{}, len(mappings))
	for _, mapping := range mappings {
		sources[strconv.FormatInt(mapping.RemnawaveID, 10)] = struct{}{}
		if mapping.TelegramID.Valid {
			telegram[mapping.TelegramID.Int64] = struct{}{}
		}
	}
	subjects, err := queries.ListTelegramIdentitySubjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, subject := range subjects {
		if telegramID, parseErr := strconv.ParseInt(subject, 10, 64); parseErr == nil {
			telegram[telegramID] = struct{}{}
		}
	}
	return sources, telegram, nil
}
