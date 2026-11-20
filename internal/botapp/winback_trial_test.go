package botapp

import (
	"strings"
	"testing"
	"time"
)

func TestTrialWinbackFromEnv(t *testing.T) {
	wave, enabled := TrialWinbackFromEnv()
	if !enabled || !wave.Trial || wave.Percent != 20 || wave.AfterHours != 2 || wave.ValidFor != 48*time.Hour {
		t.Fatalf("default trial win-back = %+v (%v)", wave, enabled)
	}
	t.Setenv("APP_TRIAL_WINBACK_PERCENT", "35")
	t.Setenv("APP_TRIAL_WINBACK_AFTER_HOURS", "0")
	if wave, enabled = TrialWinbackFromEnv(); !enabled || wave.Percent != 35 || wave.AfterHours != 0 {
		t.Fatalf("configured trial win-back = %+v (%v)", wave, enabled)
	}
	t.Setenv("APP_TRIAL_WINBACK_PERCENT", "0")
	if _, enabled = TrialWinbackFromEnv(); enabled {
		t.Fatal("a zero percent must turn the trial win-back off")
	}
	t.Setenv("APP_TRIAL_WINBACK_PERCENT", "20")
	t.Setenv("APP_TRIAL_WINBACK_ENABLED", "false")
	if _, enabled = TrialWinbackFromEnv(); enabled {
		t.Fatal("APP_TRIAL_WINBACK_ENABLED=false must turn it off")
	}
}

func TestTrialWinbackDue(t *testing.T) {
	t.Parallel()
	wave := WinbackWave{Trial: true, AfterHours: 2, Percent: 20, ValidFor: 48 * time.Hour}
	ended := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		after time.Duration
		due   bool
	}{
		{-time.Hour, false},
		{time.Hour, false},
		{2 * time.Hour, true},
		{49 * time.Hour, true},
		{50 * time.Hour, false},
	}
	for _, testCase := range cases {
		if got := trialWinbackDue(wave, ended, ended.Add(testCase.after)); got != testCase.due {
			t.Errorf("%v after the trial: due %v, want %v", testCase.after, got, testCase.due)
		}
	}
}

func TestTrialWinbackViewOpensThePlans(t *testing.T) {
	t.Parallel()
	view := trialWinbackView(LocaleEnglish, WinbackWave{Trial: true, Percent: 20}, "TRIAL20-ABCDEF", time.Now().Add(48*time.Hour))
	if !strings.Contains(view.Text, "20% off") || !strings.Contains(view.Text, "TRIAL20-ABCDEF") {
		t.Fatalf("trial win-back text = %q", view.Text)
	}
	if callbacks := strings.Join(callbackData(view), " "); !strings.Contains(callbacks, routePlans) {
		t.Fatalf("the trial win-back must open the plans: %s", callbacks)
	}
}
