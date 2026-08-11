package panelpg

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/channelgate"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// RequiredChannel is one channel customers must be members of.
type RequiredChannel struct {
	ID                   string `json:"id"`
	TelegramChatID       int64  `json:"telegramChatId"`
	Username             string `json:"username,omitempty"`
	Title                string `json:"title"`
	InviteURL            string `json:"inviteUrl,omitempty"`
	Enabled              bool   `json:"enabled"`
	RequireForPurchase   bool   `json:"requireForPurchase"`
	RequireForActivation bool   `json:"requireForActivation"`
	SortOrder            int32  `json:"sortOrder"`
}

// RequiredChannels lists every configured channel, enabled or not.
func (service *Service) RequiredChannels(ctx context.Context) ([]RequiredChannel, error) {
	rows, err := service.queries().ListRequiredChannels(ctx)
	if err != nil {
		return nil, err
	}
	channels := make([]RequiredChannel, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, requiredChannelFrom(row))
	}
	return channels, nil
}

// SaveRequiredChannel creates or updates a channel.
//
// Enabling one that gates activation is the consequential change: it can take
// access away from customers who already paid, so it carries the same reason
// requirement as any other adverse action.
func (service *Service) SaveRequiredChannel(
	ctx context.Context, channel RequiredChannel, actor Actor,
) (RequiredChannel, error) {
	if channel.TelegramChatID == 0 || strings.TrimSpace(channel.Title) == "" {
		return RequiredChannel{}, ErrValidaton
	}
	var saved RequiredChannel
	err := service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.UpsertRequiredChannel(ctx, dbgen.UpsertRequiredChannelParams{
			TelegramChatID:       channel.TelegramChatID,
			Username:             optionalText(channel.Username),
			Title:                channel.Title,
			InviteUrl:            optionalText(channel.InviteURL),
			Enabled:              channel.Enabled,
			RequireForPurchase:   channel.RequireForPurchase,
			RequireForActivation: channel.RequireForActivation,
			SortOrder:            channel.SortOrder,
			CreatedBy:            optionalUUID(actor.AdminID),
		})
		if txErr != nil {
			return txErr
		}
		saved = requiredChannelFrom(row)
		return appendAudit(ctx, queries, actor.audit(
			"panel.required_channel.saved", "configuration", "required_channel", saved.ID,
			map[string]any{
				"title": saved.Title, "enabled": saved.Enabled,
				"requireForPurchase":   saved.RequireForPurchase,
				"requireForActivation": saved.RequireForActivation,
			},
		))
	})
	return saved, err
}

// DeleteRequiredChannel removes a channel from the rule set.
func (service *Service) DeleteRequiredChannel(
	ctx context.Context, channelID string, actor Actor,
) error {
	id, err := parseUUID(channelID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.DeleteRequiredChannel(ctx, id); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.required_channel.deleted", "configuration", "required_channel", channelID, nil,
		))
	})
}

// ChannelExemption is one customer the rule does not apply to.
type ChannelExemption struct {
	CustomerID string     `json:"customerId"`
	Reason     string     `json:"reason"`
	GrantedBy  string     `json:"grantedByName,omitempty"`
	GrantedAt  time.Time  `json:"grantedAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

// ChannelExemptions lists the customers the rule does not apply to.
func (service *Service) ChannelExemptions(
	ctx context.Context, limit int32,
) ([]ChannelExemption, error) {
	rows, err := service.queries().ListChannelExemptions(ctx, pageSize(limit))
	if err != nil {
		return nil, err
	}
	exemptions := make([]ChannelExemption, 0, len(rows))
	for _, row := range rows {
		exemption := ChannelExemption{
			CustomerID: uuidString(row.UserID), Reason: row.Reason,
			GrantedBy: row.GrantedByName, GrantedAt: timeValue(row.GrantedAt),
		}
		if row.ExpiresAt.Valid {
			expires := timeValue(row.ExpiresAt)
			exemption.ExpiresAt = &expires
		}
		exemptions = append(exemptions, exemption)
	}
	return exemptions, nil
}

// GrantChannelExemption exempts one customer.
//
// The reason is required, because an exemption nobody can explain is
// indistinguishable from a mistake — and this one is the difference between a
// customer being gated and not.
func (service *Service) GrantChannelExemption(
	ctx context.Context, customerID, reason string, expiresAt *time.Time, actor Actor,
) error {
	if strings.TrimSpace(reason) == "" {
		return ErrValidaton
	}
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		var expires pgtype.Timestamptz
		if expiresAt != nil {
			expires = pgtype.Timestamptz{Time: *expiresAt, Valid: true}
		}
		if _, txErr := queries.GrantChannelExemption(ctx, dbgen.GrantChannelExemptionParams{
			UserID: id, Reason: reason,
			GrantedBy: optionalUUID(actor.AdminID), ExpiresAt: expires,
		}); txErr != nil {
			return txErr
		}
		// Exempting somebody who is suspended restores them immediately: the
		// rule no longer applies, so neither should its consequence.
		if _, txErr := queries.SetChannelEnforcement(ctx, dbgen.SetChannelEnforcementParams{
			UserID: id, State: channelgate.Exempt, Warn: false, Restore: true,
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.channel_exemption.granted", "configuration", "customer", customerID,
			map[string]any{"reason": reason},
		))
	})
}

// RevokeChannelExemption removes an exemption.
//
// The customer returns to `compliant` rather than straight to `suspended`: the
// next check decides, and it will warn first like anybody else. Suspending on
// revocation would take access away with no notice.
func (service *Service) RevokeChannelExemption(
	ctx context.Context, customerID string, actor Actor,
) error {
	id, err := parseUUID(customerID)
	if err != nil {
		return err
	}
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		if txErr := queries.RevokeChannelExemption(ctx, id); txErr != nil {
			return txErr
		}
		if _, txErr := queries.SetChannelEnforcement(ctx, dbgen.SetChannelEnforcementParams{
			UserID: id, State: channelgate.Compliant,
		}); txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, actor.audit(
			"panel.channel_exemption.revoked", "configuration", "customer", customerID, nil,
		))
	})
}

func requiredChannelFrom(row dbgen.RequiredChannel) RequiredChannel {
	return RequiredChannel{
		ID: uuidString(row.ID), TelegramChatID: row.TelegramChatID,
		Username: textValue(row.Username), Title: row.Title,
		InviteURL: textValue(row.InviteUrl), Enabled: row.Enabled,
		RequireForPurchase:   row.RequireForPurchase,
		RequireForActivation: row.RequireForActivation,
		SortOrder:            row.SortOrder,
	}
}
