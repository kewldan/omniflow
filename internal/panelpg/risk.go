package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/anomaly"
	"github.com/omniflow/omniflow/internal/blocklist"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// ---------------------------------------------------------------------------
// Blocklist sources
// ---------------------------------------------------------------------------

// BlocklistSource is one subscribed list.
//
// No credential is present. `AuthConfigured` reports whether one is stored,
// which is all the form needs to render its state.
type BlocklistSource struct {
	ID              string     `json:"id"`
	Slug            string     `json:"slug"`
	DisplayName     string     `json:"displayName"`
	SubjectKind     string     `json:"subjectKind"`
	URL             string     `json:"url"`
	AuthConfigured  bool       `json:"authConfigured"`
	Enabled         bool       `json:"enabled"`
	RefreshInterval int64      `json:"refreshIntervalSeconds"`
	EntryCount      int32      `json:"entryCount"`
	Status          string     `json:"status"`
	LastErrorCode   string     `json:"lastErrorCode,omitempty"`
	LastRefreshAt   *time.Time `json:"lastRefreshAt,omitempty"`
	NextRefreshAt   time.Time  `json:"nextRefreshAt"`
}

// BlocklistSourceInput is a panel save. `AuthHeader` is a pointer so that
// leaving it unset keeps the stored credential.
type BlocklistSourceInput struct {
	Slug            string
	DisplayName     string
	SubjectKind     string
	URL             string
	AuthHeader      *string
	Enabled         bool
	RefreshInterval int64
}

// ListBlocklistSources reads every configured source.
func (service *Service) ListBlocklistSources(ctx context.Context) ([]BlocklistSource, error) {
	rows, err := service.queries().ListBlocklistSources(ctx)
	if err != nil {
		return nil, err
	}
	sources := make([]BlocklistSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, blocklistSourceFrom(row))
	}
	return sources, nil
}

// SaveBlocklistSource stores a source configuration.
func (service *Service) SaveBlocklistSource(
	ctx context.Context, input BlocklistSourceInput, actor Actor,
) (BlocklistSource, error) {
	switch input.SubjectKind {
	case blocklist.SubjectTelegramID, blocklist.SubjectEmail, blocklist.SubjectUsername:
	default:
		return BlocklistSource{}, ErrValidaton
	}
	if !strings.HasPrefix(input.URL, "https://") {
		// Plain HTTP would let anyone on the path decide who this installation
		// refuses to serve.
		return BlocklistSource{}, ErrValidaton
	}

	var auth []byte
	if input.AuthHeader != nil {
		sealed, err := service.sealSecret(*input.AuthHeader, SecretBlocklistAuth)
		if err != nil {
			return BlocklistSource{}, err
		}
		auth = sealed
	}

	var saved BlocklistSource
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertBlocklistSource(ctx, dbgen.UpsertBlocklistSourceParams{
			Slug:                   input.Slug,
			DisplayName:            input.DisplayName,
			SubjectKind:            input.SubjectKind,
			Url:                    input.URL,
			AuthHeaderCiphertext:   auth,
			Enabled:                input.Enabled,
			RefreshIntervalSeconds: input.RefreshInterval,
			UpdatedBy:              optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = blocklistSourceFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.blocklist.source_updated", "risk", "blocklist_source", input.Slug,
			map[string]any{
				"enabled": input.Enabled, "subjectKind": input.SubjectKind,
				"credentialRotated": input.AuthHeader != nil,
			},
		))
	})
	return saved, err
}

// DeleteBlocklistSource removes a source and, by cascade, its entries.
//
// The matches it produced are removed with it: a match is evidence from a list
// the operator no longer subscribes to, and keeping it would leave a customer
// flagged by something nobody can look up any more. Decisions already recorded
// survive in the audit trail, and an allowlist entry is independent of any
// source.
func (service *Service) DeleteBlocklistSource(ctx context.Context, sourceID string, actor Actor) error {
	id, err := parseUUID(sourceID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.DeleteBlocklistSource(ctx, id); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.blocklist.source_deleted", "risk", "blocklist_source", sourceID, nil,
		))
	})
}

// DueBlocklistSources lists sources whose refresh interval has elapsed.
func (service *Service) DueBlocklistSources(ctx context.Context, limit int32) ([]BlocklistSource, error) {
	rows, err := service.queries().ListDueBlocklistSources(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	sources := make([]BlocklistSource, 0, len(rows))
	for _, row := range rows {
		sources = append(sources, blocklistSourceFrom(row))
	}
	return sources, nil
}

// BlocklistSourceCredential returns the decrypted authorization header a
// refresh must send. It is never part of a listing.
func (service *Service) BlocklistSourceCredential(ctx context.Context, sourceID string) (string, error) {
	id, err := parseUUID(sourceID)
	if err != nil {
		return "", err
	}
	row, err := service.queries().GetBlocklistSource(ctx, id)
	if err != nil {
		return "", notFound(err)
	}
	return service.OpenSecret(row.AuthHeaderCiphertext, SecretBlocklistAuth)
}

// ReplaceBlocklistEntries swaps a source's entries for a freshly fetched set.
//
// The delete and the inserts run in one transaction, so an entry the publisher
// removed stops matching in the same instant the new set starts. A refresh that
// fails part way through leaves the previous list intact rather than an
// arbitrary prefix of the new one.
func (service *Service) ReplaceBlocklistEntries(
	ctx context.Context, sourceID string, entries []blocklist.Entry,
) error {
	id, err := parseUUID(sourceID)
	if err != nil {
		return err
	}
	if len(entries) > blocklist.MaxEntries {
		return blocklist.ErrTooManyEntries
	}

	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.DeleteBlocklistEntries(ctx, id); txErr != nil {
			return txErr
		}
		for _, entry := range entries {
			if txErr := queries.InsertBlocklistEntry(ctx, dbgen.InsertBlocklistEntryParams{
				SourceID: id, ValueFingerprint: entry.Fingerprint,
				ReasonCode: optionalText(entry.ReasonCode),
			}); txErr != nil {
				return txErr
			}
		}
		return nil
	})
}

// RecordBlocklistRefresh stores the outcome of a refresh and schedules the next.
func (service *Service) RecordBlocklistRefresh(
	ctx context.Context, sourceID, status, errorCode string, entryCount int32,
) error {
	id, err := parseUUID(sourceID)
	if err != nil {
		return err
	}
	_, err = service.queries().RecordBlocklistRefresh(ctx, dbgen.RecordBlocklistRefreshParams{
		SourceID: id, Status: status, LastErrorCode: optionalText(errorCode), EntryCount: entryCount,
	})
	return notFound(err)
}

func blocklistSourceFrom(row dbgen.BlocklistSource) BlocklistSource {
	return BlocklistSource{
		ID:              uuidString(row.ID),
		Slug:            row.Slug,
		DisplayName:     row.DisplayName,
		SubjectKind:     row.SubjectKind,
		URL:             row.Url,
		AuthConfigured:  len(row.AuthHeaderCiphertext) > 0,
		Enabled:         row.Enabled,
		RefreshInterval: row.RefreshIntervalSeconds,
		EntryCount:      row.EntryCount,
		Status:          row.Status,
		LastErrorCode:   textValue(row.LastErrorCode),
		LastRefreshAt:   timePointer(row.LastRefreshAt),
		NextRefreshAt:   timeValue(row.NextRefreshAt),
	}
}

// ---------------------------------------------------------------------------
// Matches
// ---------------------------------------------------------------------------

// Identifier is one of a customer's addressable values, ready to be checked.
type Identifier struct {
	Kind  string
	Value string
}

// BlocklistMatch is one identifier that appeared on a list.
type BlocklistMatch struct {
	ID             string     `json:"id"`
	CustomerID     string     `json:"customerId"`
	SourceID       string     `json:"sourceId"`
	SourceSlug     string     `json:"sourceSlug"`
	SourceName     string     `json:"sourceName"`
	SubjectKind    string     `json:"subjectKind"`
	Status         string     `json:"status"`
	DecisionReason string     `json:"decisionReason,omitempty"`
	DecidedBy      string     `json:"decidedBy,omitempty"`
	DetectedAt     time.Time  `json:"detectedAt"`
	DecidedAt      *time.Time `json:"decidedAt,omitempty"`
}

// CheckCustomer looks a customer's identifiers up against every enabled list.
//
// It records what it finds and returns it. It does not suspend the customer,
// refuse their purchase, or change any state beyond the match rows: the whole
// point of the review queue is that a human decides. An allowlisted customer is
// skipped entirely, which is what makes an operator's "this one is fine"
// decision survive the next refresh of the source.
func (service *Service) CheckCustomer(
	ctx context.Context, customerID string, identifiers []Identifier,
) ([]BlocklistMatch, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	queries := service.queries()

	allowlisted, err := queries.IsBlocklistAllowlisted(ctx, id)
	if err != nil {
		return nil, err
	}
	if allowlisted {
		return nil, nil
	}

	// Fingerprints are computed per kind, and the kind is part of the digest, so
	// a username that reads like a number cannot match a Telegram-ID list.
	fingerprints := make([][]byte, 0, len(identifiers))
	byFingerprint := make(map[string]string, len(identifiers))
	for _, identifier := range identifiers {
		fingerprint, fingerprintErr := blocklist.Fingerprint(identifier.Kind, identifier.Value)
		if fingerprintErr != nil {
			// An identifier that cannot be normalised cannot appear on a list
			// either, so skipping it changes no outcome.
			continue
		}
		fingerprints = append(fingerprints, fingerprint)
		byFingerprint[string(fingerprint)] = identifier.Kind
	}
	if len(fingerprints) == 0 {
		return nil, nil
	}

	hits, err := queries.MatchBlocklistFingerprints(ctx, fingerprints)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	matches := make([]BlocklistMatch, 0, len(hits))
	err = service.inTx(ctx, func(tx *dbgen.Queries) error {
		for _, hit := range hits {
			row, txErr := tx.UpsertBlocklistMatch(ctx, dbgen.UpsertBlocklistMatchParams{
				UserID:           id,
				SourceID:         hit.BlocklistEntry.SourceID,
				SubjectKind:      hit.SubjectKind,
				ValueFingerprint: hit.BlocklistEntry.ValueFingerprint,
			})
			if errors.Is(txErr, pgx.ErrNoRows) {
				// Already recorded, and possibly already decided. Re-detecting
				// must never reopen a decision an operator has made.
				continue
			}
			if txErr != nil {
				return txErr
			}
			matches = append(matches, BlocklistMatch{
				ID:          uuidString(row.ID),
				CustomerID:  customerID,
				SourceID:    uuidString(row.SourceID),
				SourceSlug:  hit.Slug,
				SourceName:  hit.DisplayName,
				SubjectKind: row.SubjectKind,
				Status:      row.Status,
				DetectedAt:  timeValue(row.DetectedAt),
			})
			if txErr := appendAudit(ctx, tx, auditEntry{
				ActorType: "system", Action: "risk.blocklist.matched",
				Category: "risk", Outcome: "success",
				TargetType: "customer", TargetID: customerID,
				// The fingerprint is not recorded: it is a digest of a customer
				// identifier, and the audit trail already names the customer.
				Metadata: map[string]any{"source": hit.Slug, "subjectKind": hit.SubjectKind},
			}); txErr != nil {
				return txErr
			}
		}
		return nil
	})
	return matches, err
}

// MatchFilter is what the review queue accepts.
type MatchFilter struct {
	Status     string
	CustomerID string
	Cursor     string
	PageSize   int32
}

// MatchPage is one page of the review queue.
type MatchPage struct {
	Items      []BlocklistMatch `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// SearchBlocklistMatches reads the review queue.
func (service *Service) SearchBlocklistMatches(
	ctx context.Context, filter MatchFilter,
) (MatchPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchBlocklistMatches(ctx, dbgen.SearchBlocklistMatchesParams{
		CursorDetectedAt: cursor.timestamp(),
		CursorID:         cursor.uuid(),
		Status:           optionalText(filter.Status),
		UserID:           optionalUUID(filter.CustomerID),
		PageSize:         size + 1,
	})
	if err != nil {
		return MatchPage{}, err
	}

	page := MatchPage{Items: make([]BlocklistMatch, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(
				timeValue(last.BlocklistMatch.DetectedAt), uuidString(last.BlocklistMatch.ID),
			)
			break
		}
		page.Items = append(page.Items, BlocklistMatch{
			ID:             uuidString(row.BlocklistMatch.ID),
			CustomerID:     uuidString(row.BlocklistMatch.UserID),
			SourceID:       uuidString(row.BlocklistMatch.SourceID),
			SourceSlug:     row.SourceSlug,
			SourceName:     row.SourceName,
			SubjectKind:    row.BlocklistMatch.SubjectKind,
			Status:         row.BlocklistMatch.Status,
			DecisionReason: textValue(row.BlocklistMatch.DecisionReason),
			DecidedBy:      uuidString(row.BlocklistMatch.DecidedBy),
			DetectedAt:     timeValue(row.BlocklistMatch.DetectedAt),
			DecidedAt:      timePointer(row.BlocklistMatch.DecidedAt),
		})
	}
	return page, nil
}

// CustomerMatches lists every match against one customer, for the profile page.
func (service *Service) CustomerMatches(ctx context.Context, customerID string) ([]BlocklistMatch, error) {
	id, err := parseUUID(customerID)
	if err != nil {
		return nil, err
	}
	rows, err := service.queries().ListBlocklistMatchesForCustomer(ctx, id)
	if err != nil {
		return nil, err
	}
	matches := make([]BlocklistMatch, 0, len(rows))
	for _, row := range rows {
		matches = append(matches, BlocklistMatch{
			ID:             uuidString(row.BlocklistMatch.ID),
			CustomerID:     customerID,
			SourceID:       uuidString(row.BlocklistMatch.SourceID),
			SourceSlug:     row.SourceSlug,
			SourceName:     row.SourceName,
			SubjectKind:    row.BlocklistMatch.SubjectKind,
			Status:         row.BlocklistMatch.Status,
			DecisionReason: textValue(row.BlocklistMatch.DecisionReason),
			DecidedBy:      uuidString(row.BlocklistMatch.DecidedBy),
			DetectedAt:     timeValue(row.BlocklistMatch.DetectedAt),
			DecidedAt:      timePointer(row.BlocklistMatch.DecidedAt),
		})
	}
	return matches, nil
}

// DecideBlocklistMatch records an operator's verdict.
//
// A reason is required and an operator identity is required, because this is
// the decision a customer may later ask about and a reviewer may later
// question. Deciding does not itself suspend anybody: the operator applies that
// through the customer surface, with its own permission and its own audit
// event, so an adverse action is never a side effect of clearing a queue.
func (service *Service) DecideBlocklistMatch(
	ctx context.Context, matchID, decision string, actor Actor,
) (BlocklistMatch, error) {
	if decision != blocklist.DecisionAllowed && decision != blocklist.DecisionBlocked {
		return BlocklistMatch{}, ErrValidaton
	}
	if strings.TrimSpace(actor.Reason) == "" || actor.AdminID == "" {
		return BlocklistMatch{}, ErrValidaton
	}
	id, err := parseUUID(matchID)
	if err != nil {
		return BlocklistMatch{}, err
	}
	admin, err := parseUUID(actor.AdminID)
	if err != nil {
		return BlocklistMatch{}, err
	}

	var match BlocklistMatch
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.DecideBlocklistMatch(ctx, dbgen.DecideBlocklistMatchParams{
			MatchID: id, Status: decision,
			DecisionReason: optionalText(actor.Reason), DecidedBy: admin,
		})
		if txErr != nil {
			return rejected(txErr)
		}
		match = BlocklistMatch{
			ID:             uuidString(row.ID),
			CustomerID:     uuidString(row.UserID),
			SourceID:       uuidString(row.SourceID),
			SubjectKind:    row.SubjectKind,
			Status:         row.Status,
			DecisionReason: textValue(row.DecisionReason),
			DecidedBy:      uuidString(row.DecidedBy),
			DetectedAt:     timeValue(row.DetectedAt),
			DecidedAt:      timePointer(row.DecidedAt),
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.blocklist.decided", "risk", "customer", match.CustomerID,
			map[string]any{"matchId": matchID, "decision": decision},
		))
	})
	return match, err
}

// AppealBlocklistMatch reopens a blocked match for review.
func (service *Service) AppealBlocklistMatch(
	ctx context.Context, matchID string, actor Actor,
) error {
	if strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(matchID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.AppealBlocklistMatch(ctx, dbgen.AppealBlocklistMatchParams{
			MatchID: id, DecisionReason: optionalText(actor.Reason),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.blocklist.appealed", "risk", "customer", uuidString(row.UserID),
			map[string]any{"matchId": matchID},
		))
	})
}

// SetAllowlisted adds or removes a permanent override for one customer.
//
// Without it, an operator's decision would be undone the moment the source
// re-published the same entry. The reason is mandatory for the same reason it
// is on a block: an override is a decision somebody may have to defend.
func (service *Service) SetAllowlisted(
	ctx context.Context, customerID string, allowed bool, actor Actor,
) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	if allowed && strings.TrimSpace(actor.Reason) == "" {
		return ErrValidaton
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		action := "panel.blocklist.allowlist_removed"
		if allowed {
			if _, txErr := queries.AddBlocklistAllowlistEntry(ctx, dbgen.AddBlocklistAllowlistEntryParams{
				UserID: id, Reason: actor.Reason, AddedBy: optionalUUID(actor.AdminID),
			}); txErr != nil {
				return txErr
			}
			action = "panel.blocklist.allowlisted"
		} else if txErr := queries.RemoveBlocklistAllowlistEntry(ctx, id); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(action, "risk", "customer", customerID, nil))
	})
}

// ---------------------------------------------------------------------------
// Anomaly rules and signals
// ---------------------------------------------------------------------------

// AnomalyRule is one operator-configured threshold pair.
type AnomalyRule struct {
	Metric         string    `json:"metric"`
	Enabled        bool      `json:"enabled"`
	WindowSeconds  int64     `json:"windowSeconds"`
	WarnThreshold  int64     `json:"warnThreshold"`
	AlertThreshold int64     `json:"alertThreshold"`
	MinimumSample  int32     `json:"minimumSample"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ListAnomalyRules reads the configured thresholds.
func (service *Service) ListAnomalyRules(ctx context.Context) ([]AnomalyRule, error) {
	rows, err := service.queries().ListAnomalyRules(ctx)
	if err != nil {
		return nil, err
	}
	rules := make([]AnomalyRule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, anomalyRuleFrom(row))
	}
	return rules, nil
}

// SaveAnomalyRule stores a threshold configuration.
func (service *Service) SaveAnomalyRule(
	ctx context.Context, rule AnomalyRule, actor Actor,
) (AnomalyRule, error) {
	domain := anomaly.Rule{
		Metric: rule.Metric, Enabled: rule.Enabled,
		Window:         time.Duration(rule.WindowSeconds) * time.Second,
		WarnThreshold:  rule.WarnThreshold,
		AlertThreshold: rule.AlertThreshold,
		MinimumSample:  int(rule.MinimumSample),
	}
	// The same validity rule the evaluator applies, so a rule that would be
	// silently skipped at evaluation time is refused at save time instead.
	if !domain.Valid() {
		return AnomalyRule{}, ErrValidaton
	}

	var saved AnomalyRule
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertAnomalyRule(ctx, dbgen.UpsertAnomalyRuleParams{
			Metric: rule.Metric, Enabled: rule.Enabled,
			WindowSeconds:  rule.WindowSeconds,
			WarnThreshold:  rule.WarnThreshold,
			AlertThreshold: rule.AlertThreshold,
			MinimumSample:  rule.MinimumSample,
			UpdatedBy:      optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = anomalyRuleFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.anomaly.rule_updated", "risk", "anomaly_rule", rule.Metric,
			map[string]any{
				"enabled": rule.Enabled, "windowSeconds": rule.WindowSeconds,
				"warnThreshold": rule.WarnThreshold, "alertThreshold": rule.AlertThreshold,
			},
		))
	})
	return saved, err
}

func anomalyRuleFrom(row dbgen.AnomalyRule) AnomalyRule {
	return AnomalyRule{
		Metric:         row.Metric,
		Enabled:        row.Enabled,
		WindowSeconds:  row.WindowSeconds,
		WarnThreshold:  row.WarnThreshold,
		AlertThreshold: row.AlertThreshold,
		MinimumSample:  row.MinimumSample,
		UpdatedAt:      timeValue(row.UpdatedAt),
	}
}

// AnomalySignal is one raised anomaly with the evidence that produced it.
type AnomalySignal struct {
	ID           string          `json:"id"`
	Metric       string          `json:"metric"`
	Severity     string          `json:"severity"`
	SubjectType  string          `json:"subjectType"`
	SubjectID    string          `json:"subjectId"`
	Observed     int64           `json:"observed"`
	Threshold    int64           `json:"threshold"`
	SampleSize   int32           `json:"sampleSize"`
	WindowStart  time.Time       `json:"windowStart"`
	WindowEnd    time.Time       `json:"windowEnd"`
	Evidence     json.RawMessage `json:"evidence"`
	Status       string          `json:"status"`
	ReviewedBy   string          `json:"reviewedBy,omitempty"`
	ReviewReason string          `json:"reviewReason,omitempty"`
	DetectedAt   time.Time       `json:"detectedAt"`
	ReviewedAt   *time.Time      `json:"reviewedAt,omitempty"`
}

// SignalFilter is what the anomaly queue accepts.
type SignalFilter struct {
	Status   string
	Metric   string
	Severity string
	Cursor   string
	PageSize int32
}

// SignalPage is one page of the anomaly queue.
type SignalPage struct {
	Items      []AnomalySignal `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// SearchAnomalySignals reads the anomaly queue.
func (service *Service) SearchAnomalySignals(
	ctx context.Context, filter SignalFilter,
) (SignalPage, error) {
	size := pageSize(filter.PageSize)
	cursor := DecodeCursor(filter.Cursor)

	rows, err := service.queries().SearchAnomalySignals(ctx, dbgen.SearchAnomalySignalsParams{
		CursorDetectedAt: cursor.timestamp(),
		CursorID:         cursor.uuid(),
		Status:           optionalText(filter.Status),
		Metric:           optionalText(filter.Metric),
		Severity:         optionalText(filter.Severity),
		PageSize:         size + 1,
	})
	if err != nil {
		return SignalPage{}, err
	}

	page := SignalPage{Items: make([]AnomalySignal, 0, min(len(rows), int(size)))}
	for index, row := range rows {
		if index == int(size) {
			last := rows[index-1]
			page.NextCursor = EncodeCursor(timeValue(last.DetectedAt), uuidString(last.ID))
			break
		}
		page.Items = append(page.Items, anomalySignalFrom(row))
	}
	return page, nil
}

// ReviewAnomalySignal records an acknowledgement or a dismissal.
//
// Reviewing a signal changes nothing about the customer it names. That is the
// property that makes automated detection safe to run at all: the worst a false
// positive can do is cost an operator the time it takes to dismiss it.
func (service *Service) ReviewAnomalySignal(
	ctx context.Context, signalID, status string, actor Actor,
) (AnomalySignal, error) {
	if status != "acknowledged" && status != "dismissed" {
		return AnomalySignal{}, ErrValidaton
	}
	if actor.AdminID == "" {
		return AnomalySignal{}, ErrValidaton
	}
	id, err := parseUUID(signalID)
	if err != nil {
		return AnomalySignal{}, err
	}
	admin, err := parseUUID(actor.AdminID)
	if err != nil {
		return AnomalySignal{}, err
	}

	var signal AnomalySignal
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.ReviewAnomalySignal(ctx, dbgen.ReviewAnomalySignalParams{
			SignalID: id, Status: status, ReviewedBy: admin,
			ReviewReason: optionalText(actor.Reason),
		})
		if txErr != nil {
			return rejected(txErr)
		}
		signal = anomalySignalFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.anomaly.reviewed", "risk", "anomaly_signal", signalID,
			map[string]any{"status": status, "metric": row.Metric, "severity": row.Severity},
		))
	})
	return signal, err
}

func anomalySignalFrom(row dbgen.AnomalySignal) AnomalySignal {
	return AnomalySignal{
		ID:           uuidString(row.ID),
		Metric:       row.Metric,
		Severity:     row.Severity,
		SubjectType:  row.SubjectType,
		SubjectID:    row.SubjectID,
		Observed:     row.Observed,
		Threshold:    row.Threshold,
		SampleSize:   row.SampleSize,
		WindowStart:  timeValue(row.WindowStartedAt),
		WindowEnd:    timeValue(row.WindowEndedAt),
		Evidence:     row.Evidence,
		Status:       row.Status,
		ReviewedBy:   uuidString(row.ReviewedBy),
		ReviewReason: textValue(row.ReviewReason),
		DetectedAt:   timeValue(row.DetectedAt),
		ReviewedAt:   timePointer(row.ReviewedAt),
	}
}

// EvaluateAnomalies runs every enabled rule and records what it finds.
//
// Each raised signal is also queued as an operator notification, in the same
// transaction, so an alert cannot be announced for a signal that was rolled
// back. The notification carries the metric, the severity, and the numbers; it
// never carries a customer identifier, because an operator group is a chat.
//
// The evaluation itself is `internal/anomaly`, which has no database import and
// is unit-tested. This method only supplies observations and persists results.
func (service *Service) EvaluateAnomalies(ctx context.Context) ([]AnomalySignal, error) {
	rules, err := service.queries().ListAnomalyRules(ctx)
	if err != nil {
		return nil, err
	}
	now := service.now()

	var raised []AnomalySignal
	for _, stored := range rules {
		rule := anomaly.Rule{
			Metric: stored.Metric, Enabled: stored.Enabled,
			Window:         time.Duration(stored.WindowSeconds) * time.Second,
			WarnThreshold:  stored.WarnThreshold,
			AlertThreshold: stored.AlertThreshold,
			MinimumSample:  int(stored.MinimumSample),
		}
		if !rule.Enabled || !rule.Valid() {
			continue
		}

		observations, observeErr := service.observe(ctx, rule, now)
		if observeErr != nil {
			return nil, observeErr
		}
		for _, signal := range anomaly.Evaluate(rule, now, observations) {
			persisted, persistErr := service.raise(ctx, signal)
			if persistErr != nil {
				return nil, persistErr
			}
			raised = append(raised, persisted)
		}
	}
	return raised, nil
}

// observe gathers the aggregates one rule is evaluated against.
func (service *Service) observe(
	ctx context.Context, rule anomaly.Rule, now time.Time,
) ([]anomaly.Observation, error) {
	queries := service.queries()
	windowStart := now.Add(-rule.Window)

	switch rule.Metric {
	case anomaly.MetricPurchase:
		rows, err := queries.ObservePurchaseVolume(ctx, dbgen.ObservePurchaseVolumeParams{
			WindowStart: timestamp(windowStart), WindowEnd: timestamp(now),
			// The warning threshold doubles as the database-side floor: nothing
			// below it can raise anything, so there is no point transferring it.
			MinimumMinor: rule.WarnThreshold,
		})
		if err != nil {
			return nil, err
		}
		observations := make([]anomaly.Observation, 0, len(rows))
		for _, row := range rows {
			observations = append(observations, anomaly.Observation{
				SubjectType: anomaly.SubjectCustomer,
				SubjectID:   uuidString(row.UserID),
				Value:       row.PaidMinor,
				Sample:      int(row.OrderCount),
				Evidence: map[string]any{
					"orderCount": row.OrderCount, "paidMinor": row.PaidMinor,
					"currency": row.Currency,
				},
			})
		}
		return observations, nil

	case anomaly.MetricRefund:
		rows, err := queries.ObserveRefundVolume(ctx, dbgen.ObserveRefundVolumeParams{
			WindowStart: timestamp(windowStart), WindowEnd: timestamp(now),
			MinimumMinor: rule.WarnThreshold,
		})
		if err != nil {
			return nil, err
		}
		observations := make([]anomaly.Observation, 0, len(rows))
		for _, row := range rows {
			observations = append(observations, anomaly.Observation{
				SubjectType: anomaly.SubjectCustomer,
				SubjectID:   uuidString(row.UserID),
				Value:       row.RefundedMinor,
				Sample:      int(row.RefundCount),
				Evidence: map[string]any{
					"refundCount": row.RefundCount, "refundedMinor": row.RefundedMinor,
					"currency": row.Currency,
				},
			})
		}
		return observations, nil

	case anomaly.MetricReferral:
		rows, err := queries.ObserveReferralVolume(ctx, dbgen.ObserveReferralVolumeParams{
			WindowStart: timestamp(windowStart), WindowEnd: timestamp(now),
			MinimumCount: rule.WarnThreshold,
		})
		if err != nil {
			return nil, err
		}
		observations := make([]anomaly.Observation, 0, len(rows))
		for _, row := range rows {
			observations = append(observations, anomaly.Observation{
				SubjectType: anomaly.SubjectCustomer,
				SubjectID:   uuidString(row.UserID),
				Value:       row.QualifiedCount,
				Sample:      int(row.QualifiedCount),
				Evidence:    map[string]any{"qualifiedCount": row.QualifiedCount},
			})
		}
		return observations, nil

	case anomaly.MetricTraffic:
		// Remnawave owns traffic and Omniflow holds only the counter it last
		// observed, so this is a level check rather than a rate. The rule's
		// window controls how often a subscription that stays above the
		// threshold is re-raised, not what is measured.
		rows, err := queries.ObserveTrafficUsage(ctx, dbgen.ObserveTrafficUsageParams{
			ThresholdBytes: rule.WarnThreshold, PageSize: MaxPageSize,
		})
		if err != nil {
			return nil, err
		}
		observations := make([]anomaly.Observation, 0, len(rows))
		for _, row := range rows {
			evidence := map[string]any{"usedBytes": row.UsedBytes, "measure": "observed_level"}
			if reconciled := timePointer(row.ReconciledAt); reconciled != nil {
				evidence["reconciledAt"] = reconciled.Format(time.RFC3339)
			}
			observations = append(observations, anomaly.Observation{
				SubjectType: anomaly.SubjectCustomer,
				SubjectID:   uuidString(row.UserID),
				Value:       row.UsedBytes,
				// One subscription is one observation; the level check needs no
				// larger sample to be meaningful.
				Sample:   1,
				Evidence: evidence,
			})
		}
		return observations, nil

	default:
		return nil, nil
	}
}

// raise persists one signal and queues the operator notice for it.
func (service *Service) raise(ctx context.Context, signal anomaly.Signal) (AnomalySignal, error) {
	evidence, err := json.Marshal(signal.Evidence)
	if err != nil {
		return AnomalySignal{}, err
	}

	var persisted AnomalySignal
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RaiseAnomalySignal(ctx, dbgen.RaiseAnomalySignalParams{
			Metric: signal.Metric, Severity: signal.Severity,
			SubjectType: signal.SubjectType, SubjectID: signal.SubjectID,
			Observed: signal.Observed, Threshold: signal.Threshold,
			SampleSize:      int32(signal.SampleSize),
			WindowStartedAt: timestamp(signal.WindowStart),
			WindowEndedAt:   timestamp(signal.WindowEnd),
			Evidence:        evidence,
			DedupeKey:       signal.DedupeKey,
		})
		if txErr != nil {
			return txErr
		}
		persisted = anomalySignalFrom(row)

		// The notice names the metric and the numbers and nothing else. A
		// subject identifier in an operator group would put a customer
		// identifier in a chat, and the panel link is where an operator with the
		// permission looks it up.
		payload, marshalErr := json.Marshal(map[string]any{
			"signalId": persisted.ID, "metric": signal.Metric, "severity": signal.Severity,
			"observed": signal.Observed, "threshold": signal.Threshold,
			"subjectType": signal.SubjectType,
		})
		if marshalErr != nil {
			return marshalErr
		}
		_, txErr = queries.EnqueueOperatorNotification(ctx, dbgen.EnqueueOperatorNotificationParams{
			Kind: "anomaly", DedupeKey: signal.Metric + ":" + signal.DedupeKey, Payload: payload,
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			// Already queued for this window: the unique (kind, dedupe_key)
			// index is what keeps a persisting condition to one notice.
			return nil
		}
		return txErr
	})
	return persisted, err
}
