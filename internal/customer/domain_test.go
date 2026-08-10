package customer

import (
	"errors"
	"testing"
	"time"
)

func TestCannotUnlinkLastVerifiedIdentity(t *testing.T) {
	identities := []Identity{{ID: "telegram", Active: true, Verified: true}}
	if err := CanUnlink(identities, "telegram"); !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("expected ErrLastIdentity, got %v", err)
	}
}

func TestDeletionMustObserveRetentionBeforeAnonymization(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	profile, err := (Profile{Status: StatusActive}).Transition("delete", "customer request", now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Transition("anonymize", "retention elapsed", now.Add(29*24*time.Hour), 0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected retention guard, got %v", err)
	}
	if _, err := profile.Transition("anonymize", "retention elapsed", now.Add(30*24*time.Hour), 0); err != nil {
		t.Fatalf("expected anonymization after retention: %v", err)
	}
}

func TestImportPreviewDetectsBatchAndExistingConflicts(t *testing.T) {
	telegramID := int64(42)
	items := PreviewImport([]ImportCandidate{
		{SourceID: "rw-1", TelegramID: &telegramID, Payload: map[string]any{"id": "rw-1"}},
		{SourceID: "rw-1", TelegramID: &telegramID, Payload: map[string]any{"id": "rw-1"}},
	}, map[string]struct{}{}, map[int64]struct{}{})
	if items[0].Status != "valid" || items[1].Status != "conflict" {
		t.Fatalf("unexpected preview statuses: %#v", items)
	}
}
