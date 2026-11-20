package referralplus

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/panelpg"
)

// Partner programme errors.
var (
	// ErrPartnerPending refuses a second application while one is open.
	ErrPartnerPending = errors.New("an application is already waiting for a decision")
	// ErrAlreadyPartner refuses an application from a current partner.
	ErrAlreadyPartner = errors.New("the customer is already a partner")
	// ErrApplicationNotFound reports an application that does not exist.
	ErrApplicationNotFound = errors.New("no such partner application")
	// ErrApplicationDecided refuses a decision on a decided application.
	ErrApplicationDecided = errors.New("the application was already decided")
)

// Application sources.
const (
	SourceBot = "bot"
	SourceWeb = "web"
)

// PartnerApplication is one customer's request to become a partner.
type PartnerApplication struct {
	ID                        string     `json:"id"`
	CustomerID                string     `json:"customerId"`
	Status                    string     `json:"status"`
	Source                    string     `json:"source"`
	Channel                   string     `json:"channel"`
	Message                   string     `json:"message"`
	CommissionBPS             *int32     `json:"commissionBps,omitempty"`
	FirstPaymentCommissionBPS *int32     `json:"firstPaymentCommissionBps,omitempty"`
	DecisionNote              string     `json:"decisionNote,omitempty"`
	DecidedAt                 *time.Time `json:"decidedAt,omitempty"`
	CreatedAt                 time.Time  `json:"createdAt"`
	// Invited and Qualified are the applicant's referrals so far, which is
	// what an operator weighs the application against. Panel listing only.
	Invited   int64 `json:"invited"`
	Qualified int64 `json:"qualified"`
}

// PartnerState is what the customer's partner screen shows.
type PartnerState struct {
	// Partner is true while an approved application's rate is in force.
	Partner                   bool                `json:"partner"`
	CommissionBPS             *int32              `json:"commissionBps,omitempty"`
	FirstPaymentCommissionBPS *int32              `json:"firstPaymentCommissionBps,omitempty"`
	Latest                    *PartnerApplication `json:"latest,omitempty"`
	CanApply                  bool                `json:"canApply"`
}

const applicationColumns = `a.id::text, a.user_id::text, a.status, a.source, a.channel, a.message,
	a.commission_bps, a.first_payment_commission_bps, a.decision_note, a.decided_at, a.created_at`

func scanApplication(row pgx.Row, extra ...any) (PartnerApplication, error) {
	var (
		application PartnerApplication
		rate, first pgtype.Int4
		decidedAt   pgtype.Timestamptz
	)
	targets := []any{&application.ID, &application.CustomerID, &application.Status, &application.Source,
		&application.Channel, &application.Message, &rate, &first, &application.DecisionNote, &decidedAt,
		&application.CreatedAt}
	if err := row.Scan(append(targets, extra...)...); err != nil {
		return PartnerApplication{}, err
	}
	application.CommissionBPS, application.FirstPaymentCommissionBPS = int4Pointer(rate), int4Pointer(first)
	if decidedAt.Valid {
		at := decidedAt.Time
		application.DecidedAt = &at
	}
	return application, nil
}

func int4Pointer(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	return &value.Int32
}

// PartnerStatus reads the customer's partner standing and latest application.
// A customer is a partner while their latest decided application is an
// approval and a personal rate is in force; an operator who removes the rate
// on the customer card ends the partnership, and the customer may apply again.
func (service *Service) PartnerStatus(ctx context.Context, customerID string) (PartnerState, error) {
	state := PartnerState{}
	latest, err := scanApplication(service.pool.QueryRow(ctx, `SELECT `+applicationColumns+`
		FROM partner_applications a WHERE a.user_id = $1::uuid ORDER BY a.created_at DESC LIMIT 1`, customerID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return PartnerState{}, err
	default:
		state.Latest = &latest
	}
	var rate, first pgtype.Int4
	err = service.pool.QueryRow(ctx, `SELECT commission_bps, first_payment_commission_bps
		FROM referral_inviter_terms WHERE user_id = $1::uuid`, customerID).Scan(&rate, &first)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PartnerState{}, err
	}
	termsInForce := err == nil && (rate.Valid || first.Valid)
	state.Partner = termsInForce && state.Latest != nil && state.Latest.Status == "approved"
	if state.Partner {
		state.CommissionBPS, state.FirstPaymentCommissionBPS = int4Pointer(rate), int4Pointer(first)
	}
	state.CanApply = !state.Partner && (state.Latest == nil || state.Latest.Status != "pending")
	return state, nil
}

// ApplyPartner records a customer's application. One application may be
// open at a time, and a current partner cannot apply.
func (service *Service) ApplyPartner(ctx context.Context, customerID, source, channel, message string) (PartnerApplication, error) {
	channel, message = strings.TrimSpace(channel), strings.TrimSpace(message)
	if (source != SourceBot && source != SourceWeb) || utf8.RuneCountInString(channel) > 200 ||
		utf8.RuneCountInString(message) > 1000 {
		return PartnerApplication{}, ErrInvalid
	}
	state, err := service.PartnerStatus(ctx, customerID)
	if err != nil {
		return PartnerApplication{}, err
	}
	if state.Partner {
		return PartnerApplication{}, ErrAlreadyPartner
	}
	if !state.CanApply {
		return PartnerApplication{}, ErrPartnerPending
	}
	application, err := scanApplication(service.pool.QueryRow(ctx, `INSERT INTO partner_applications AS a
			(user_id, source, channel, message)
		VALUES ($1::uuid, $2, $3, $4) RETURNING `+applicationColumns, customerID, source, channel, message))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Two taps raced past the check; the index kept one.
		return PartnerApplication{}, ErrPartnerPending
	}
	return application, err
}

// PartnerApplications lists applications, newest first, optionally of one
// status, with each applicant's referrals so far.
func (service *Service) PartnerApplications(ctx context.Context, status string, limit int) ([]PartnerApplication, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := service.pool.Query(ctx, `SELECT `+applicationColumns+`,
			(SELECT count(*) FROM referral_attributions r WHERE r.referrer_user_id = a.user_id),
			(SELECT count(*) FROM referral_attributions r WHERE r.referrer_user_id = a.user_id
				AND r.qualified_at IS NOT NULL AND r.review_state <> 'rejected')
		FROM partner_applications a
		WHERE $1 = '' OR a.status = $1
		ORDER BY (a.status = 'pending') DESC, a.created_at DESC
		LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []PartnerApplication{}
	for rows.Next() {
		var invited, qualified int64
		application, scanErr := scanApplication(rows, &invited, &qualified)
		if scanErr != nil {
			return nil, scanErr
		}
		application.Invited, application.Qualified = invited, qualified
		list = append(list, application)
	}
	return list, rows.Err()
}

// PartnerDecision is an operator's answer to an application.
type PartnerDecision struct {
	Approve                   bool   `json:"approve"`
	CommissionBPS             *int32 `json:"commissionBps"`
	FirstPaymentCommissionBPS *int32 `json:"firstPaymentCommissionBps"`
	Note                      string `json:"note"`
}

// DecidePartner approves or rejects a pending application. Approval sets the
// customer's personal commission rate in the same transaction, which is what
// makes them earn the partner rate on every later commission; nothing is paid
// here. Both outcomes are audited, and a decided application cannot be
// decided again.
func (service *Service) DecidePartner(
	ctx context.Context, applicationID string, decision PartnerDecision, actor panelpg.Actor,
) (PartnerApplication, error) {
	decision.Note = strings.TrimSpace(decision.Note)
	if utf8.RuneCountInString(decision.Note) > 400 {
		return PartnerApplication{}, ErrInvalid
	}
	if decision.Approve {
		if decision.CommissionBPS == nil {
			return PartnerApplication{}, ErrInvalid
		}
		for _, bps := range []*int32{decision.CommissionBPS, decision.FirstPaymentCommissionBPS} {
			if bps != nil && (*bps < 0 || *bps > 10000) {
				return PartnerApplication{}, ErrInvalid
			}
		}
	} else {
		decision.CommissionBPS, decision.FirstPaymentCommissionBPS = nil, nil
	}
	tx, err := service.pool.Begin(ctx)
	if err != nil {
		return PartnerApplication{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var customerID, status string
	err = tx.QueryRow(ctx, `SELECT user_id::text, status FROM partner_applications
		WHERE id = $1::uuid FOR UPDATE`, applicationID).Scan(&customerID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return PartnerApplication{}, ErrApplicationNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return PartnerApplication{}, ErrApplicationNotFound
		}
		return PartnerApplication{}, err
	}
	if status != "pending" {
		return PartnerApplication{}, ErrApplicationDecided
	}
	outcome := "rejected"
	if decision.Approve {
		outcome = "approved"
	}
	application, err := scanApplication(tx.QueryRow(ctx, `UPDATE partner_applications AS a
		SET status = $2, commission_bps = $3, first_payment_commission_bps = $4, decision_note = $5,
			decided_by = NULLIF($6, '')::uuid, decided_at = now()
		WHERE a.id = $1::uuid RETURNING `+applicationColumns,
		applicationID, outcome, decision.CommissionBPS, decision.FirstPaymentCommissionBPS, decision.Note, actor.AdminID))
	if err != nil {
		return PartnerApplication{}, err
	}
	if decision.Approve {
		note := "partner application"
		if decision.Note != "" {
			note = "partner application: " + decision.Note
		}
		if _, err = tx.Exec(ctx, `INSERT INTO referral_inviter_terms
				(user_id, commission_bps, first_payment_commission_bps, note, updated_by)
			VALUES ($1::uuid, $2, $3, left($4, 400), NULLIF($5, '')::uuid)
			ON CONFLICT (user_id) DO UPDATE SET commission_bps = EXCLUDED.commission_bps,
				first_payment_commission_bps = EXCLUDED.first_payment_commission_bps,
				note = EXCLUDED.note, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			customerID, decision.CommissionBPS, decision.FirstPaymentCommissionBPS, note, actor.AdminID); err != nil {
			return PartnerApplication{}, err
		}
	}
	if err = panelpg.AppendAudit(ctx, tx, actor, "panel.referral.partner_"+outcome, "marketing", "customer", customerID,
		map[string]any{
			"applicationId": applicationID, "commissionBps": decision.CommissionBPS,
			"firstPaymentBps": decision.FirstPaymentCommissionBPS,
		}); err != nil {
		return PartnerApplication{}, err
	}
	return application, tx.Commit(ctx)
}

// NetworkMember is one customer in an inviter's network.
type NetworkMember struct {
	CustomerID string `json:"customerId"`
	// Level is 1 for a customer the root invited, 2 for one invited by them;
	// Via names the level-1 inviter of a level-2 member.
	Level       int       `json:"level"`
	Via         string    `json:"via,omitempty"`
	JoinedAt    time.Time `json:"joinedAt"`
	Qualified   bool      `json:"qualified"`
	ReviewState string    `json:"reviewState"`
	// EarnedMinor is what this member's payments earned the root and was
	// not reversed: first-level rewards and commissions, or second-level
	// shares. EarnedDays is first-level rewards paid in days.
	EarnedMinor int64 `json:"earnedMinor"`
	EarnedDays  int64 `json:"earnedDays"`
}

// NetworkLevel is one level's totals and its newest members.
type NetworkLevel struct {
	Level       int             `json:"level"`
	Count       int64           `json:"count"`
	Qualified   int64           `json:"qualified"`
	EarnedMinor int64           `json:"earnedMinor"`
	Members     []NetworkMember `json:"members"`
}

// Network is a customer's referral network: who invited them, and two levels
// below them.
type Network struct {
	CustomerID string `json:"customerId"`
	// Currency is the referral programme's: money earned is summed in it,
	// and rewards paid in any other currency are left out.
	Currency  string `json:"currency"`
	InvitedBy string `json:"invitedBy,omitempty"`
	// InvitedByUpline is the inviter's own inviter: the customer whose
	// second level this customer is.
	InvitedByUpline string         `json:"invitedByUpline,omitempty"`
	Levels          []NetworkLevel `json:"levels"`
}

// ReferralNetwork reads a customer's network for the panel: their inviter and
// that inviter's inviter, and the members of levels one and two with what each
// earned the customer. Each level lists at most limit members, newest first;
// its totals count all of them.
func (service *Service) ReferralNetwork(ctx context.Context, customerID string, limit int) (Network, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	network := Network{CustomerID: customerID, Levels: []NetworkLevel{}}
	var exists bool
	if err := service.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid)`, customerID).
		Scan(&exists); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return Network{}, ErrApplicationNotFound
		}
		return Network{}, err
	}
	if !exists {
		return Network{}, ErrApplicationNotFound
	}
	var upline, upline2 pgtype.Text
	err := service.pool.QueryRow(ctx, `SELECT a.referrer_user_id::text, b.referrer_user_id::text
		FROM referral_attributions a
		LEFT JOIN referral_attributions b ON b.referred_user_id = a.referrer_user_id
		WHERE a.referred_user_id = $1::uuid`, customerID).Scan(&upline, &upline2)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Network{}, err
	}
	network.InvitedBy, network.InvitedByUpline = upline.String, upline2.String
	if err = service.pool.QueryRow(ctx, `SELECT COALESCE((SELECT currency FROM referral_programs WHERE singleton), 'RUB')`).
		Scan(&network.Currency); err != nil {
		return Network{}, err
	}

	rows, err := service.pool.Query(ctx, `WITH level1 AS (
			SELECT a.referred_user_id AS member, NULL::uuid AS via, 1 AS level, a.created_at,
				a.qualified_at IS NOT NULL AS qualified, a.review_state
			FROM referral_attributions a WHERE a.referrer_user_id = $1::uuid
		), level2 AS (
			SELECT b.referred_user_id, l.member, 2, b.created_at, b.qualified_at IS NOT NULL, b.review_state
			FROM level1 l JOIN referral_attributions b ON b.referrer_user_id = l.member
			WHERE b.referred_user_id <> $1::uuid
		), members AS (
			SELECT * FROM level1 UNION ALL SELECT * FROM level2
		), ranked AS (
			SELECT m.*, row_number() OVER (PARTITION BY m.level ORDER BY m.created_at DESC) AS position,
				CASE WHEN m.level = 1 THEN COALESCE((SELECT sum(w.amount_minor) FROM referral_rewards w
						WHERE w.beneficiary_user_id = $1::uuid AND w.referred_user_id = m.member
						  AND w.reversed_at IS NULL AND w.role IN ('inviter', 'commission') AND w.currency = $2), 0)
					ELSE COALESCE((SELECT sum(s.amount_minor) FROM referral_level2_rewards s
						JOIN referral_rewards w ON w.id = s.source_reward_id
						WHERE s.beneficiary_user_id = $1::uuid AND w.referred_user_id = m.member
						  AND s.reversed_at IS NULL AND s.currency = $2), 0)
				END::bigint AS earned_minor,
				CASE WHEN m.level = 1 THEN COALESCE((SELECT sum(w.days) FROM referral_rewards w
						WHERE w.beneficiary_user_id = $1::uuid AND w.referred_user_id = m.member
						  AND w.reversed_at IS NULL AND w.role = 'inviter'), 0)
					ELSE 0 END::bigint AS earned_days
			FROM members m
		)
		SELECT member::text, COALESCE(via::text, ''), level, created_at, qualified, review_state,
			earned_minor, earned_days, position
		FROM ranked ORDER BY level, created_at DESC`, customerID, network.Currency)
	if err != nil {
		return Network{}, err
	}
	defer rows.Close()
	levels := map[int]*NetworkLevel{1: {Level: 1, Members: []NetworkMember{}}, 2: {Level: 2, Members: []NetworkMember{}}}
	for rows.Next() {
		var member NetworkMember
		var position int64
		if err = rows.Scan(&member.CustomerID, &member.Via, &member.Level, &member.JoinedAt, &member.Qualified,
			&member.ReviewState, &member.EarnedMinor, &member.EarnedDays, &position); err != nil {
			return Network{}, err
		}
		level := levels[member.Level]
		level.Count++
		if member.Qualified && member.ReviewState != "rejected" {
			level.Qualified++
		}
		level.EarnedMinor += member.EarnedMinor
		if position <= int64(limit) {
			level.Members = append(level.Members, member)
		}
	}
	if err = rows.Err(); err != nil {
		return Network{}, err
	}
	network.Levels = append(network.Levels, *levels[1], *levels[2])
	return network, nil
}
