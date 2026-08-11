package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// MaintenanceState is the installation's current maintenance posture.
//
// `Source` distinguishes an operator switching it on from a dependency probe
// doing so, which is the difference between "we are working on it" and
// "something broke". Both block new purchases and defer fulfillment; neither
// cancels, refunds, or expires anything already paid for.
type MaintenanceState struct {
	Active           bool       `json:"active"`
	Source           string     `json:"source"`
	Reason           string     `json:"reason"`
	NoticeRU         string     `json:"noticeRu"`
	NoticeEN         string     `json:"noticeEn"`
	ExpectedReturnAt *time.Time `json:"expectedReturnAt,omitempty"`
	ActivatedAt      *time.Time `json:"activatedAt,omitempty"`
	ClearedAt        *time.Time `json:"clearedAt,omitempty"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// MaintenanceState reads the current posture, creating the singleton row on
// first use so a fresh installation reports "inactive" rather than "missing".
func (service *Service) MaintenanceState(ctx context.Context) (MaintenanceState, error) {
	row, err := service.queries().GetMaintenanceState(ctx)
	if err != nil {
		return MaintenanceState{}, err
	}
	return maintenanceFrom(row), nil
}

// MaintenanceInput is an operator's manual change.
type MaintenanceInput struct {
	Active           bool
	NoticeRU         string
	NoticeEN         string
	ExpectedReturnAt *time.Time
}

// SetMaintenance switches maintenance mode manually.
//
// The source is always `manual` here: a change made from the panel must never
// masquerade as automatic detection, because the two are cleared by different
// things — automatic detection clears itself on recovery, and a manual
// activation stays until somebody clears it.
//
// Both notices are required when activating. A customer who reads only one
// language and gets a blank screen is worse served than one who gets a notice
// they can read.
func (service *Service) SetMaintenance(
	ctx context.Context, input MaintenanceInput, actor Actor,
) (MaintenanceState, error) {
	if strings.TrimSpace(actor.Reason) == "" {
		return MaintenanceState{}, ErrValidaton
	}
	if input.Active && (strings.TrimSpace(input.NoticeRU) == "" || strings.TrimSpace(input.NoticeEN) == "") {
		return MaintenanceState{}, ErrValidaton
	}

	var state MaintenanceState
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.SetMaintenanceState(ctx, dbgen.SetMaintenanceStateParams{
			Active: input.Active, Source: "manual", Reason: actor.Reason,
			NoticeRu: input.NoticeRU, NoticeEn: input.NoticeEN,
			ExpectedReturnAt: optionalTimestamp(input.ExpectedReturnAt),
		})
		if txErr != nil {
			return txErr
		}
		state = maintenanceFrom(row)

		action := "cleared"
		if input.Active {
			action = "activated"
		}
		if _, txErr = queries.InsertMaintenanceEvent(ctx, dbgen.InsertMaintenanceEventParams{
			Action: action, Source: "manual", Reason: actor.Reason,
			ActorType: "operator", ActorID: optionalText(actor.AdminID),
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.maintenance."+action, "system", "maintenance", "installation",
			map[string]any{"active": input.Active},
		))
	})
	return state, err
}

func maintenanceFrom(row dbgen.MaintenanceState) MaintenanceState {
	return MaintenanceState{
		Active:           row.Active,
		Source:           row.Source,
		Reason:           row.Reason,
		NoticeRU:         row.NoticeRu,
		NoticeEN:         row.NoticeEn,
		ExpectedReturnAt: timePointer(row.ExpectedReturnAt),
		ActivatedAt:      timePointer(row.ActivatedAt),
		ClearedAt:        timePointer(row.ClearedAt),
		UpdatedAt:        timeValue(row.UpdatedAt),
	}
}

// ProviderHealth is what one configured payment provider last reported.
//
// It is deliberately separate from a live probe: "configured but never reached"
// and "reached and failing" are different problems, and collapsing them into
// one status would hide the first behind the second.
type ProviderHealth struct {
	Provider            string     `json:"provider"`
	MerchantID          string     `json:"merchantId,omitempty"`
	Enabled             bool       `json:"enabled"`
	ConnectionStatus    string     `json:"connectionStatus"`
	WebhookStatus       string     `json:"webhookStatus"`
	ConnectionCheckedAt *time.Time `json:"connectionCheckedAt,omitempty"`
	WebhookLastEventAt  *time.Time `json:"webhookLastEventAt,omitempty"`
}

// ProviderHealth lists what every configured payment provider last reported.
func (service *Service) ProviderHealth(ctx context.Context) ([]ProviderHealth, error) {
	rows, err := service.queries().DashboardProviderHealth(ctx)
	if err != nil {
		return nil, err
	}
	health := make([]ProviderHealth, 0, len(rows))
	for _, row := range rows {
		health = append(health, ProviderHealth{
			Provider:            row.Provider,
			MerchantID:          row.MerchantID,
			Enabled:             row.Enabled,
			ConnectionStatus:    row.ConnectionStatus,
			WebhookStatus:       row.WebhookStatus,
			ConnectionCheckedAt: timePointer(row.ConnectionCheckedAt),
			WebhookLastEventAt:  timePointer(row.WebhookLastEventAt),
		})
	}
	return health, nil
}

// GoodsProviderHealth lists what every digital-goods provider last reported,
// reusing the same record the shop page shows.
func (service *Service) GoodsProviderHealth(ctx context.Context) ([]GoodsProvider, error) {
	return service.ListGoodsProviders(ctx)
}
