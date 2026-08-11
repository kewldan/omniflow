package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/audience"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// AudienceSegment is a named, reviewable filter set.
type AudienceSegment struct {
	ID      string         `json:"id"`
	Code    string         `json:"code"`
	NameEN  string         `json:"nameEn"`
	NameRU  string         `json:"nameRu"`
	Filters map[string]any `json:"filters"`
	// Explain is what the filters mean, generated from the filters themselves so
	// it cannot describe something the query does not do.
	Explain []string `json:"explain"`
	// Size is how many customers match right now. It moves between reviews, and
	// showing it beside the definition is the whole point of a review step.
	Size int64 `json:"size"`
}

// Segments lists the saved segments with a live count for each.
func (service *Service) Segments(ctx context.Context) ([]AudienceSegment, error) {
	rows, err := service.queries().ListAudienceSegments(ctx)
	if err != nil {
		return nil, err
	}
	segments := make([]AudienceSegment, 0, len(rows))
	for _, row := range rows {
		segment, err := service.describeSegment(ctx, row)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

// describeSegment compiles a stored definition and counts what it selects.
func (service *Service) describeSegment(
	ctx context.Context, row dbgen.AudienceSegment,
) (AudienceSegment, error) {
	filters := map[string]any{}
	if err := json.Unmarshal(row.Filters, &filters); err != nil {
		return AudienceSegment{}, err
	}
	query, err := audience.Compile(filters, time.Now().UTC())
	if err != nil {
		return AudienceSegment{}, err
	}
	segment := AudienceSegment{
		ID: uuidString(row.ID), Code: row.Code, NameEN: row.NameEn, NameRU: row.NameRu,
		Filters: filters, Explain: query.Explain,
	}
	// The count is a plain read against the compiled fragment. It is the same
	// statement the send will walk, so the estimate and the reality cannot come
	// from different definitions.
	if err = service.pool.QueryRow(ctx,
		"SELECT count(*) FROM users u WHERE u.status = 'active' AND ("+query.Where+")",
		query.Args...,
	).Scan(&segment.Size); err != nil {
		return AudienceSegment{}, err
	}
	return segment, nil
}

// SaveSegment stores a filter set after checking it compiles.
//
// Refusing an unreadable segment on write rather than on send is what stops an
// operator discovering at send time that their audience means nothing.
func (service *Service) SaveSegment(
	ctx context.Context, segment AudienceSegment, actor Actor,
) (AudienceSegment, error) {
	if strings.TrimSpace(segment.Code) == "" ||
		strings.TrimSpace(segment.NameEN) == "" || strings.TrimSpace(segment.NameRU) == "" {
		return AudienceSegment{}, ErrValidaton
	}
	if err := audience.Validate(segment.Filters); err != nil {
		return AudienceSegment{}, ErrValidaton
	}
	payload, err := json.Marshal(segment.Filters)
	if err != nil {
		return AudienceSegment{}, err
	}

	var saved dbgen.AudienceSegment
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertAudienceSegment(ctx, dbgen.UpsertAudienceSegmentParams{
			Code:   strings.ToLower(strings.TrimSpace(segment.Code)),
			NameEn: segment.NameEN, NameRu: segment.NameRU,
			Filters: payload, CreatedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = row
		return appendAudit(ctx, queries, actor.audit(
			"panel.segment.saved", "configuration", "audience_segment", uuidString(row.ID),
			// The filters go into the audit record because "who changed what a
			// campaign meant by 'lapsed customers'" is exactly the question an
			// audit trail exists to answer.
			map[string]any{"code": row.Code, "filters": segment.Filters},
		))
	})
	if err != nil {
		return AudienceSegment{}, err
	}
	return service.describeSegment(ctx, saved)
}

// MessageTemplate is a reusable body with declared variables.
type MessageTemplate struct {
	ID        string   `json:"id"`
	Code      string   `json:"code"`
	Class     string   `json:"class"`
	SubjectEN string   `json:"subjectEn"`
	SubjectRU string   `json:"subjectRu"`
	BodyEN    string   `json:"bodyEn"`
	BodyRU    string   `json:"bodyRu"`
	Variables []string `json:"variables"`
}

// templateVariable matches the {{ name }} placeholders a body may use.
var templateVariable = regexp.MustCompile(`\{\{\s*([a-zA-Z][a-zA-Z0-9_]*)\s*\}\}`)

// Templates lists the reusable messages.
func (service *Service) Templates(ctx context.Context) ([]MessageTemplate, error) {
	rows, err := service.queries().ListMessageTemplates(ctx)
	if err != nil {
		return nil, err
	}
	templates := make([]MessageTemplate, 0, len(rows))
	for _, row := range rows {
		templates = append(templates, templateFrom(row))
	}
	return templates, nil
}

// SaveTemplate stores a template after validating its variables.
//
// A body referencing a variable that is not declared is refused on write. The
// alternative is a customer receiving "Hello {{name}}" because nobody checked,
// and by then it has been sent to everybody.
func (service *Service) SaveTemplate(
	ctx context.Context, template MessageTemplate, actor Actor,
) (MessageTemplate, error) {
	if strings.TrimSpace(template.Code) == "" ||
		strings.TrimSpace(template.BodyEN) == "" || strings.TrimSpace(template.BodyRU) == "" {
		return MessageTemplate{}, ErrValidaton
	}
	declared := make(map[string]bool, len(template.Variables))
	for _, variable := range template.Variables {
		declared[variable] = true
	}
	for _, body := range []string{template.BodyEN, template.BodyRU,
		template.SubjectEN, template.SubjectRU} {
		for _, match := range templateVariable.FindAllStringSubmatch(body, -1) {
			if !declared[match[1]] {
				return MessageTemplate{}, ErrValidaton
			}
		}
	}

	var saved MessageTemplate
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertMessageTemplate(ctx, dbgen.UpsertMessageTemplateParams{
			Code: strings.ToLower(strings.TrimSpace(template.Code)), Class: template.Class,
			SubjectEn: template.SubjectEN, SubjectRu: template.SubjectRU,
			BodyEn: template.BodyEN, BodyRu: template.BodyRU,
			Variables: template.Variables, UpdatedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = templateFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.template.saved", "configuration", "message_template", saved.ID,
			map[string]any{"code": saved.Code, "class": saved.Class},
		))
	})
	return saved, err
}

// Campaign is one send.
type Campaign struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	TemplateCode string     `json:"templateCode"`
	SegmentCode  string     `json:"segmentCode"`
	Status       string     `json:"status"`
	Estimated    int32      `json:"estimatedAudience"`
	Queued       int32      `json:"queuedCount"`
	Sent         int32      `json:"sentCount"`
	Failed       int32      `json:"failedCount"`
	Suppressed   int32      `json:"suppressedCount"`
	ScheduledFor *time.Time `json:"scheduledFor,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

// Campaigns lists the sends, newest first.
func (service *Service) Campaigns(ctx context.Context, limit int32) ([]Campaign, error) {
	rows, err := service.queries().ListCampaigns(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	campaigns := make([]Campaign, 0, len(rows))
	for _, row := range rows {
		campaign := campaignFrom(row.Campaign)
		campaign.TemplateCode = row.TemplateCode
		campaign.SegmentCode = row.SegmentCode
		campaigns = append(campaigns, campaign)
	}
	return campaigns, nil
}

// CreateCampaign records a send in the draft state.
//
// The audience is estimated now and stored, and nothing is queued: a campaign
// is reviewed before it runs, and a draft that had already queued its
// recipients would make the review a formality.
func (service *Service) CreateCampaign(
	ctx context.Context, name, templateID, segmentID string, actor Actor,
) (Campaign, error) {
	if strings.TrimSpace(name) == "" {
		return Campaign{}, ErrValidaton
	}
	template, err := parseUUID(templateID)
	if err != nil {
		return Campaign{}, err
	}
	segmentUUID, err := parseUUID(segmentID)
	if err != nil {
		return Campaign{}, err
	}
	segmentRow, err := service.queries().GetAudienceSegment(ctx, segmentUUID)
	if err != nil {
		return Campaign{}, notFound(err)
	}
	described, err := service.describeSegment(ctx, segmentRow)
	if err != nil {
		return Campaign{}, err
	}

	var campaign Campaign
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreateCampaign(ctx, dbgen.CreateCampaignParams{
			Name: name, TemplateID: template, SegmentID: segmentUUID,
			EstimatedAudience: int32(described.Size), CreatedBy: optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		campaign = campaignFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.campaign.created", "communication", "campaign", campaign.ID,
			map[string]any{
				"name": name, "segment": described.Code,
				"estimatedAudience": described.Size, "explain": described.Explain,
			},
		))
	})
	return campaign, err
}

// campaignTransitions is the permitted state machine.
//
// It is spelled out rather than implied, because the states a campaign may not
// reach are the interesting ones: a completed campaign cannot go back to
// running, and a cancelled one cannot be resurrected with a different audience.
var campaignTransitions = map[string][]string{
	"scheduled": {"draft", "paused"},
	"running":   {"draft", "scheduled", "paused"},
	"paused":    {"running"},
	"cancelled": {"draft", "scheduled", "paused"},
	"completed": {"running"},
}

// SetCampaignState moves a campaign, refusing a transition that is not allowed.
func (service *Service) SetCampaignState(
	ctx context.Context, campaignID, status string, scheduledFor *time.Time, actor Actor,
) (Campaign, error) {
	allowed, known := campaignTransitions[status]
	if !known {
		return Campaign{}, ErrValidaton
	}
	if status == "scheduled" && scheduledFor == nil {
		return Campaign{}, ErrValidaton
	}
	id, err := parseUUID(campaignID)
	if err != nil {
		return Campaign{}, err
	}

	var campaign Campaign
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		var schedule pgtype.Timestamptz
		if scheduledFor != nil {
			schedule = pgtype.Timestamptz{Time: *scheduledFor, Valid: true}
		}
		row, txErr := queries.SetCampaignState(ctx, dbgen.SetCampaignStateParams{
			CampaignID: id, Status: status, ScheduledFor: schedule, AllowedFrom: allowed,
		})
		if txErr != nil {
			// No row matched means the campaign was not in a state this
			// transition allows. Refusing is the point.
			return notFound(txErr)
		}
		campaign = campaignFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.campaign."+status, "communication", "campaign", campaignID,
			map[string]any{"status": status},
		))
	})
	return campaign, err
}

// Suppression is one customer the operator must not contact.
type Suppression struct {
	CustomerID string    `json:"customerId"`
	Reason     string    `json:"reason"`
	Note       string    `json:"note,omitempty"`
	CreatedBy  string    `json:"createdByName,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// Suppressions lists the standing instructions not to contact.
func (service *Service) Suppressions(ctx context.Context, limit int32) ([]Suppression, error) {
	rows, err := service.queries().ListSuppressions(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	suppressions := make([]Suppression, 0, len(rows))
	for _, row := range rows {
		suppressions = append(suppressions, Suppression{
			CustomerID: uuidString(row.UserID), Reason: row.Reason,
			Note: textValue(row.Note), CreatedBy: row.CreatedByName,
			CreatedAt: timeValue(row.CreatedAt),
		})
	}
	return suppressions, nil
}

// Suppress records a standing instruction not to contact a customer.
//
// It is separate from the marketing consent flag because they mean different
// things: consent is a preference the customer toggles, and a suppression
// survives them toggling it back on by accident.
func (service *Service) Suppress(
	ctx context.Context, customerID, reason, note string, actor Actor,
) error {
	if !validSuppressionReason[reason] {
		return ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if _, txErr := queries.SuppressCustomer(ctx, dbgen.SuppressCustomerParams{
			UserID: id, Reason: reason, Note: optionalText(note),
			CreatedBy: optionalUUID(actor.AdminID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.suppression.added", "communication", "customer", customerID,
			map[string]any{"reason": reason},
		))
	})
}

// Unsuppress lifts a standing instruction.
func (service *Service) Unsuppress(ctx context.Context, customerID string, actor Actor) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.UnsuppressCustomer(ctx, id); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.suppression.removed", "communication", "customer", customerID, nil,
		))
	})
}

var validSuppressionReason = map[string]bool{
	"customer_request": true, "bounced": true, "complaint": true, "operator": true,
}

func templateFrom(row dbgen.MessageTemplate) MessageTemplate {
	return MessageTemplate{
		ID: uuidString(row.ID), Code: row.Code, Class: row.Class,
		SubjectEN: row.SubjectEn, SubjectRU: row.SubjectRu,
		BodyEN: row.BodyEn, BodyRU: row.BodyRu, Variables: row.Variables,
	}
}

func campaignFrom(row dbgen.Campaign) Campaign {
	campaign := Campaign{
		ID: uuidString(row.ID), Name: row.Name, Status: row.Status,
		Estimated: row.EstimatedAudience, Queued: row.QueuedCount,
		Sent: row.SentCount, Failed: row.FailedCount, Suppressed: row.SuppressedCount,
		CreatedAt: timeValue(row.CreatedAt),
	}
	if row.ScheduledFor.Valid {
		scheduled := timeValue(row.ScheduledFor)
		campaign.ScheduledFor = &scheduled
	}
	if row.StartedAt.Valid {
		started := timeValue(row.StartedAt)
		campaign.StartedAt = &started
	}
	if row.CompletedAt.Valid {
		completed := timeValue(row.CompletedAt)
		campaign.CompletedAt = &completed
	}
	return campaign
}

// ErrSegmentUnreadable reports a stored segment that no longer compiles, which
// can only happen if the vocabulary shrank under it.
var ErrSegmentUnreadable = errors.New("audience segment cannot be compiled")
