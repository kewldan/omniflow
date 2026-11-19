package errorlog

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestFingerprintIgnoresIdentifiersAndNumbers(t *testing.T) {
	first := Fingerprint("worker", "order 018f0000-0000-7000-8000-000000000001 failed after 3 attempts")
	second := Fingerprint("worker", "order 018f0000-0000-7000-8000-0000000000ff failed after 4 attempts")
	if first != second {
		t.Fatal("errors differing only in identifiers must share a fingerprint")
	}
	if first == Fingerprint("bot", "order 018f0000-0000-7000-8000-000000000001 failed after 3 attempts") {
		t.Fatal("the process is part of the fingerprint")
	}
}

func TestMilestonesAreAnnounced(t *testing.T) {
	for _, count := range []int64{1, 10, 100, 1000} {
		if !milestone(count) {
			t.Fatalf("%d is a milestone", count)
		}
	}
	for _, count := range []int64{2, 11, 20, 150} {
		if milestone(count) {
			t.Fatalf("%d is not a milestone", count)
		}
	}
}

func TestHandlerPassesEveryRecordThrough(t *testing.T) {
	buffer := &bytes.Buffer{}
	handler := New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelWarn}), "api")
	logger := slog.New(handler).With("component", "test")
	logger.Info("hidden")
	logger.Error("boom", "error", "disk full")
	if !bytes.Contains(buffer.Bytes(), []byte("boom")) || bytes.Contains(buffer.Bytes(), []byte("hidden")) {
		t.Fatalf("output = %s", buffer.String())
	}
	if !handler.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error level must stay enabled")
	}
	// Without a database nothing is queued, so logging can never block.
	if len(handler.state.queue) != 0 {
		t.Fatal("records must not queue before Attach")
	}
}
