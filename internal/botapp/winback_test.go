package botapp

import (
	"testing"
	"time"
)

func TestDueWinbackWave(t *testing.T) {
	t.Parallel()
	waves := []WinbackWave{{Number: 2, AfterDays: 2, Percent: 10, ValidFor: 24 * time.Hour}, {Number: 3, AfterDays: 5, Percent: 20, ValidFor: 24 * time.Hour}}
	ended := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		after time.Duration
		wave  int
	}{
		{24 * time.Hour, 0},
		{2 * 24 * time.Hour, 2},
		{3*24*time.Hour + 23*time.Hour, 2},
		{4 * 24 * time.Hour, 0},
		{5 * 24 * time.Hour, 3},
		{7 * 24 * time.Hour, 0},
	}
	for _, testCase := range cases {
		wave, due := dueWinbackWave(waves, ended, ended.Add(testCase.after))
		got := 0
		if due {
			got = wave.Number
		}
		if got != testCase.wave {
			t.Errorf("%v after expiry: wave %d, want %d", testCase.after, got, testCase.wave)
		}
	}
}

func TestWinbackWavesFromEnv(t *testing.T) {
	t.Setenv("APP_WINBACK_WAVE2_PERCENT", "15")
	t.Setenv("APP_WINBACK_WAVE3_PERCENT", "0")
	waves := WinbackWavesFromEnv()
	if len(waves) != 1 || waves[0].Percent != 15 || waves[0].AfterDays != 2 || waves[0].ValidFor != 24*time.Hour {
		t.Fatalf("waves = %+v, want only wave 2 at 15%%", waves)
	}
	t.Setenv("APP_WINBACK_ENABLED", "false")
	if waves := WinbackWavesFromEnv(); len(waves) != 0 {
		t.Fatalf("disabled waves = %+v", waves)
	}
}
