package recurring

import (
	"testing"
	"time"
)

func TestTheDefaultScheduleKeepsTheBuiltInCadence(t *testing.T) {
	t.Parallel()
	var zero Schedule
	now := time.Unix(0, 0)
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		got, want := zero.ScheduleNext(attempt, now), ScheduleNext(attempt, now)
		if got != want {
			t.Fatalf("attempt %d: %+v, want %+v", attempt, got, want)
		}
	}
	if zero.MaxAttempts() != MaxAttempts || zero.DunningWindow() != DunningWindow() {
		t.Fatal("the zero schedule is not the default")
	}
	// The second failure is the first one the customer hears about.
	if ScheduleNext(1, now).Notify || !ScheduleNext(2, now).Notify {
		t.Fatal("notification cadence changed")
	}
}

func TestParseSchedule(t *testing.T) {
	t.Parallel()
	schedule, err := ParseSchedule("1, 6", "1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	first := schedule.ScheduleNext(1, now)
	if schedule.MaxAttempts() != 3 || first.At != now.Add(time.Hour) || !first.Notify {
		t.Fatalf("schedule = %+v, first = %+v", schedule, first)
	}
	if last := schedule.ScheduleNext(3, now); !last.Abandon {
		t.Fatalf("the third attempt must be the last: %+v", last)
	}
	if schedule.CycleOrderLifetime() != 7*time.Hour+CycleOrderMargin {
		t.Fatalf("order lifetime = %v", schedule.CycleOrderLifetime())
	}
	for _, bad := range [][2]string{{"0", ""}, {"x", ""}, {"200,200", ""}, {"", "0"}, {"1,1,1,1,1,1,1,1,1,1", ""}} {
		if _, err := ParseSchedule(bad[0], bad[1]); err == nil {
			t.Fatalf("%v was accepted", bad)
		}
	}
	if defaults, err := ParseSchedule("", ""); err != nil || defaults.MaxAttempts() != MaxAttempts {
		t.Fatalf("empty settings: %+v %v", defaults, err)
	}
}
