package recurring

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// Schedule is an installation's dunning cadence: the waits before each retry
// and the first failed attempt the customer is told about. The zero value is
// DefaultSchedule.
type Schedule struct {
	// Delays are the waits before attempts two, three, and so on; there is
	// one more attempt than there are delays.
	Delays []time.Duration
	// NotifyFrom is the first failed attempt that is announced to the
	// customer (2: the second failure and later); the final abandonment is
	// always announced.
	NotifyFrom int
}

// DefaultSchedule is the built-in cadence: retries after 4, 12, and 24 hours,
// the customer told from the second failure on.
var DefaultSchedule = Schedule{Delays: []time.Duration{4 * time.Hour, 12 * time.Hour, 24 * time.Hour}, NotifyFrom: 2}

// MaxScheduleWindow bounds a configured cadence: every attempt has to fit
// inside the longest lead time, or a retry would land after access lapsed.
const MaxScheduleWindow = MaxLeadTime

// ParseSchedule reads a cadence from APP_RENEWAL_RETRY_HOURS (comma-separated
// hours, e.g. "4,12,24") and APP_RENEWAL_NOTIFY_FROM_ATTEMPT. Empty values
// keep the defaults.
func ParseSchedule(hours string, notifyFrom string) (Schedule, error) {
	schedule := DefaultSchedule
	if strings.TrimSpace(hours) != "" {
		delays := make([]time.Duration, 0, 4)
		var total time.Duration
		for _, part := range strings.Split(hours, ",") {
			value, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || value < 1 {
				return Schedule{}, errors.New("APP_RENEWAL_RETRY_HOURS must list whole hours of at least 1")
			}
			delay := time.Duration(value) * time.Hour
			total += delay
			delays = append(delays, delay)
		}
		if len(delays) > 9 || total > MaxScheduleWindow {
			return Schedule{}, errors.New("APP_RENEWAL_RETRY_HOURS allows at most nine retries within fourteen days")
		}
		schedule.Delays = delays
	}
	if strings.TrimSpace(notifyFrom) != "" {
		value, err := strconv.Atoi(strings.TrimSpace(notifyFrom))
		if err != nil || value < 1 {
			return Schedule{}, errors.New("APP_RENEWAL_NOTIFY_FROM_ATTEMPT must be a positive attempt number")
		}
		schedule.NotifyFrom = value
	}
	return schedule, nil
}

func (schedule Schedule) normalized() Schedule {
	if schedule.Delays == nil {
		schedule.Delays = DefaultSchedule.Delays
	}
	if schedule.NotifyFrom < 1 {
		schedule.NotifyFrom = DefaultSchedule.NotifyFrom
	}
	return schedule
}

// MaxAttempts is how many times one cycle is charged before giving up.
func (schedule Schedule) MaxAttempts() int { return len(schedule.normalized().Delays) + 1 }

// RetryDelay is the wait before attempt number `attempt`.
func (schedule Schedule) RetryDelay(attempt int) time.Duration {
	delays := schedule.normalized().Delays
	switch {
	case attempt <= 1:
		return 0
	case attempt-2 < len(delays):
		return delays[attempt-2]
	default:
		return delays[len(delays)-1]
	}
}

// DunningWindow is the sum of the retry waits.
func (schedule Schedule) DunningWindow() time.Duration {
	var total time.Duration
	for _, delay := range schedule.normalized().Delays {
		total += delay
	}
	return total
}

// CycleOrderLifetime is how long a cycle's one order stays payable.
func (schedule Schedule) CycleOrderLifetime() time.Duration {
	return schedule.DunningWindow() + CycleOrderMargin
}

// ScheduleNext decides what follows a failed charge under this cadence.
func (schedule Schedule) ScheduleNext(attempt int, now time.Time) NextAttempt {
	normalized := schedule.normalized()
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= normalized.MaxAttempts() {
		return NextAttempt{Abandon: true, Notify: true}
	}
	next := attempt + 1
	return NextAttempt{Attempt: next, At: now.Add(normalized.RetryDelay(next)), Notify: attempt >= normalized.NotifyFrom}
}
