package customer

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrIdentityConflict  = errors.New("identity is already linked to another customer")
	ErrLastIdentity      = errors.New("cannot unlink the last verified identity")
	ErrInvalidTransition = errors.New("invalid customer lifecycle transition")
)

type Status string

const (
	StatusActive    Status = "active"
	StatusSuspended Status = "suspended"
	StatusDeleted   Status = "deleted"
)

type Profile struct {
	ID             string
	Status         Status
	Locale         string
	Timezone       string
	SuspendedAt    *time.Time
	DeletedAt      *time.Time
	AnonymizedAt   *time.Time
	RetentionUntil *time.Time
}

type Identity struct {
	ID        string
	UserID    string
	Provider  string
	Subject   string
	Verified  bool
	Active    bool
	CreatedAt time.Time
}

func (profile Profile) Transition(action, reason string, now time.Time, retention time.Duration) (Profile, error) {
	if strings.TrimSpace(reason) == "" {
		return Profile{}, errors.New("lifecycle reason is required")
	}
	switch action {
	case "suspend":
		if profile.Status != StatusActive {
			return Profile{}, ErrInvalidTransition
		}
		profile.Status = StatusSuspended
		profile.SuspendedAt = &now
	case "restore":
		if profile.Status != StatusSuspended {
			return Profile{}, ErrInvalidTransition
		}
		profile.Status = StatusActive
		profile.SuspendedAt = nil
	case "delete":
		if profile.Status == StatusDeleted {
			return Profile{}, ErrInvalidTransition
		}
		profile.Status = StatusDeleted
		profile.DeletedAt = &now
		until := now.Add(retention)
		profile.RetentionUntil = &until
	case "anonymize":
		if profile.Status != StatusDeleted || profile.RetentionUntil == nil || now.Before(*profile.RetentionUntil) {
			return Profile{}, ErrInvalidTransition
		}
		profile.AnonymizedAt = &now
	default:
		return Profile{}, ErrInvalidTransition
	}
	return profile, nil
}

func CanUnlink(identities []Identity, identityID string) error {
	verifiedActive := 0
	targetVerified := false
	for _, identity := range identities {
		if identity.Active && identity.Verified {
			verifiedActive++
		}
		if identity.ID == identityID && identity.Active && identity.Verified {
			targetVerified = true
		}
	}
	if targetVerified && verifiedActive <= 1 {
		return ErrLastIdentity
	}
	return nil
}

type ImportCandidate struct {
	SourceID   string
	TelegramID *int64
	Username   string
	Payload    map[string]any
}

type ImportItem struct {
	SourceID        string
	Status          string
	Fingerprint     [32]byte
	StagedData      json.RawMessage
	ValidationCodes []string
}

func PreviewImport(candidates []ImportCandidate, existingSourceIDs map[string]struct{}, existingTelegramIDs map[int64]struct{}) []ImportItem {
	items := make([]ImportItem, 0, len(candidates))
	seenSource := make(map[string]struct{}, len(candidates))
	seenTelegram := make(map[int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		data, _ := json.Marshal(candidate.Payload)
		item := ImportItem{SourceID: candidate.SourceID, Status: "valid", Fingerprint: sha256.Sum256(data), StagedData: data}
		if strings.TrimSpace(candidate.SourceID) == "" {
			item.ValidationCodes = append(item.ValidationCodes, "missing_source_id")
		}
		if _, exists := existingSourceIDs[candidate.SourceID]; exists {
			item.ValidationCodes = append(item.ValidationCodes, "source_already_linked")
		}
		if _, duplicate := seenSource[candidate.SourceID]; duplicate {
			item.ValidationCodes = append(item.ValidationCodes, "duplicate_source_id")
		}
		seenSource[candidate.SourceID] = struct{}{}
		if candidate.TelegramID != nil {
			if *candidate.TelegramID <= 0 {
				item.ValidationCodes = append(item.ValidationCodes, "invalid_telegram_id")
			}
			if _, exists := existingTelegramIDs[*candidate.TelegramID]; exists {
				item.ValidationCodes = append(item.ValidationCodes, "telegram_already_linked")
			}
			if _, duplicate := seenTelegram[*candidate.TelegramID]; duplicate {
				item.ValidationCodes = append(item.ValidationCodes, "duplicate_telegram_id")
			}
			seenTelegram[*candidate.TelegramID] = struct{}{}
		}
		if len(item.ValidationCodes) > 0 {
			item.Status = "conflict"
			for _, code := range item.ValidationCodes {
				if strings.HasPrefix(code, "missing_") || strings.HasPrefix(code, "invalid_") {
					item.Status = "invalid"
				}
			}
		}
		items = append(items, item)
	}
	return items
}

func ValidateLocaleTimezone(locale, timezone string) error {
	if locale != "ru" && locale != "en" {
		return fmt.Errorf("unsupported locale %q", locale)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	return nil
}
