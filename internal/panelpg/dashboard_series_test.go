package panelpg

import (
	"testing"
	"time"
)

// The bucket arithmetic is easy to get backwards, and a backwards series draws
// a falling line for a rising measure.
func TestSeriesLayout(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	value := func(bucket int) int64 { return int64(bucket) * 10 }

	history := historySeries(now, 3, value)
	if len(history) != 3 {
		t.Fatalf("%d history points, want 3", len(history))
	}
	// Oldest first: the bucket two days back leads, the last 24 hours close.
	if history[0].Value != 20 || history[2].Value != 0 {
		t.Fatalf("history values %d…%d, want 20…0", history[0].Value, history[2].Value)
	}
	if !history[0].Start.Equal(now.Add(-72*time.Hour)) || !history[2].Start.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("history starts %s…%s", history[0].Start, history[2].Start)
	}

	upcoming := upcomingSeries(now, 3, value)
	// Soonest first: the next 24 hours lead.
	if upcoming[0].Value != 0 || upcoming[2].Value != 20 {
		t.Fatalf("upcoming values %d…%d, want 0…20", upcoming[0].Value, upcoming[2].Value)
	}
	if !upcoming[0].Start.Equal(now) || !upcoming[2].Start.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("upcoming starts %s…%s", upcoming[0].Start, upcoming[2].Start)
	}
}
