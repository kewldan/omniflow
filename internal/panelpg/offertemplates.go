package panelpg

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/audience"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// OfferTemplate is a reusable personal offer — Bedolaga's promo offer
// template — sent to a whole segment at once.
type OfferTemplate struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	PromotionID string `json:"promotionId"`
	PlanID      string `json:"planId,omitempty"`
	TitleRU     string `json:"titleRu"`
	TitleEN     string `json:"titleEn"`
	TermsRU     string `json:"termsRu"`
	TermsEN     string `json:"termsEn"`
	// ValidHours is how long a sent offer can be used — or, with a timer,
	// activated.
	ValidHours int32 `json:"validHours"`
	// ActivationHours starts a discount timer when the customer activates
	// the offer; nil applies it as soon as it is sent.
	ActivationHours *int32 `json:"activationHours,omitempty"`
	// BonusMinor is credited to the wallet when the offer is activated.
	BonusMinor    int64  `json:"bonusMinor"`
	BonusCurrency string `json:"bonusCurrency,omitempty"`
}

// OfferTemplates lists the templates not archived.
func (service *Service) OfferTemplates(ctx context.Context) ([]OfferTemplate, error) {
	rows, err := service.pool.Query(ctx, `SELECT id::text, code, promotion_id::text, COALESCE(plan_id::text, ''),
		title_ru, title_en, terms_ru, terms_en, valid_hours, activation_hours, bonus_minor, COALESCE(bonus_currency, '')
		FROM offer_templates WHERE archived_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	templates := []OfferTemplate{}
	for rows.Next() {
		var template OfferTemplate
		var activation pgtype.Int4
		if err := rows.Scan(&template.ID, &template.Code, &template.PromotionID, &template.PlanID,
			&template.TitleRU, &template.TitleEN, &template.TermsRU, &template.TermsEN,
			&template.ValidHours, &activation, &template.BonusMinor, &template.BonusCurrency); err != nil {
			return nil, err
		}
		template.ActivationHours = int32Pointer(activation)
		templates = append(templates, template)
	}
	return templates, rows.Err()
}

// SaveOfferTemplate creates a template or replaces the one with its code.
func (service *Service) SaveOfferTemplate(ctx context.Context, template OfferTemplate, actor Actor) (OfferTemplate, error) {
	template.Code = strings.ToLower(strings.TrimSpace(template.Code))
	template.BonusCurrency = strings.ToUpper(strings.TrimSpace(template.BonusCurrency))
	if template.Code == "" || strings.TrimSpace(template.TitleRU) == "" || strings.TrimSpace(template.TitleEN) == "" ||
		template.ValidHours < 1 || template.ValidHours > 8760 || template.BonusMinor < 0 ||
		template.ActivationHours != nil && (*template.ActivationHours < 1 || *template.ActivationHours > 8760) ||
		template.BonusMinor > 0 && len(template.BonusCurrency) != 3 {
		return OfferTemplate{}, ErrValidaton
	}
	promotion, err := parseUUID(template.PromotionID)
	if err != nil {
		return OfferTemplate{}, ErrValidaton
	}
	err = service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		if txErr := tx.QueryRow(ctx, `INSERT INTO offer_templates (code, promotion_id, plan_id, title_ru, title_en,
				terms_ru, terms_en, valid_hours, activation_hours, bonus_minor, bonus_currency, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), $12)
			ON CONFLICT (code) DO UPDATE SET promotion_id = EXCLUDED.promotion_id, plan_id = EXCLUDED.plan_id,
				title_ru = EXCLUDED.title_ru, title_en = EXCLUDED.title_en, terms_ru = EXCLUDED.terms_ru,
				terms_en = EXCLUDED.terms_en, valid_hours = EXCLUDED.valid_hours,
				activation_hours = EXCLUDED.activation_hours, bonus_minor = EXCLUDED.bonus_minor,
				bonus_currency = EXCLUDED.bonus_currency, archived_at = NULL
			RETURNING id::text`, template.Code, promotion, optionalUUID(template.PlanID), template.TitleRU, template.TitleEN,
			template.TermsRU, template.TermsEN, template.ValidHours, optionalInt4(template.ActivationHours),
			template.BonusMinor, template.BonusCurrency, optionalUUID(actor.AdminID)).Scan(&template.ID); txErr != nil {
			return rejected(txErr)
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.offer_template.saved", "marketing", "offer_template", template.ID,
			map[string]any{"code": template.Code, "promotionId": template.PromotionID, "bonusMinor": template.BonusMinor},
		))
	})
	return template, err
}

// SendOfferTemplate offers a template to every customer of a segment, in one
// statement. A customer who already holds a live offer of the same promotion
// is skipped, so sending twice offers nobody twice. It reports how many
// offers were created.
func (service *Service) SendOfferTemplate(ctx context.Context, templateID, segmentID string, actor Actor) (int64, error) {
	template, err := parseUUID(templateID)
	if err != nil {
		return 0, err
	}
	segment, err := parseUUID(segmentID)
	if err != nil {
		return 0, err
	}
	var offered int64
	err = service.inTxRaw(ctx, func(tx pgx.Tx, queries *dbgen.Queries) error {
		row, txErr := queries.GetAudienceSegment(ctx, segment)
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		filters := map[string]any{}
		if txErr = json.Unmarshal(row.Filters, &filters); txErr != nil {
			return txErr
		}
		compiled, txErr := audience.Compile(filters, time.Now())
		if txErr != nil {
			return ErrValidaton
		}
		base := len(compiled.Args)
		args := append(compiled.Args, template, optionalUUID(actor.AdminID))
		result, txErr := tx.Exec(ctx, `INSERT INTO personal_offers (user_id, promotion_id, plan_id, title_ru, title_en,
				terms_ru, terms_en, starts_at, expires_at, created_by, template_id, activation_hours, bonus_minor, bonus_currency)
			SELECT u.id, t.promotion_id, t.plan_id, t.title_ru, t.title_en, t.terms_ru, t.terms_en,
				now(), now() + make_interval(hours => t.valid_hours), $`+strconv.Itoa(base+2)+`::uuid, t.id,
				t.activation_hours, t.bonus_minor, t.bonus_currency
			FROM users u, offer_templates t
			WHERE t.id = $`+strconv.Itoa(base+1)+`::uuid AND t.archived_at IS NULL AND u.status = 'active' AND (`+compiled.Where+`)
			ON CONFLICT (user_id, promotion_id) WHERE status = 'active' DO NOTHING`, args...)
		if txErr != nil {
			return txErr
		}
		offered = result.RowsAffected()
		if _, txErr = tx.Exec(ctx, `INSERT INTO offer_sends (template_id, segment_id, offered, created_by)
			VALUES ($1, $2, $3, $4)`, template, segment, offered, optionalUUID(actor.AdminID)); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.offer_template.sent", "marketing", "offer_template", templateID,
			map[string]any{"segmentId": segmentID, "offered": offered},
		))
	})
	return offered, err
}
