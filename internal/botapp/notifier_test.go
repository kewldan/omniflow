package botapp

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/commerce"
)

// fakeCandidatePages is an in-memory stand-in for the candidate query: it
// serves identifiers in order, honouring the cursor and the limit exactly as
// the SQL does, and counts how many pages were asked for.
func fakeCandidatePages(total int) (func(ctx context.Context, after string, limit int) ([]notificationCandidate, error), *int) {
	ids := make([]string, 0, total)
	for index := range total {
		ids = append(ids, fmt.Sprintf("%08d-0000-4000-8000-000000000000", index))
	}
	calls := 0
	page := func(ctx context.Context, after string, limit int) ([]notificationCandidate, error) {
		calls++
		candidates := make([]notificationCandidate, 0, limit)
		for _, id := range ids {
			if after != "" && id <= after {
				continue
			}
			candidates = append(candidates, notificationCandidate{UserID: id})
			if len(candidates) == limit {
				break
			}
		}
		return candidates, nil
	}
	return page, &calls
}

// TestWalkCandidatePagesReachesEveryCustomer is the defect that motivated the
// walk: a single bounded read visited the first page and nobody after it.
func TestWalkCandidatePagesReachesEveryCustomer(t *testing.T) {
	for _, total := range []int{0, 1, 199, 200, 201, 450, 600} {
		t.Run(fmt.Sprintf("%d customers", total), func(t *testing.T) {
			page, calls := fakeCandidatePages(total)
			seen := map[string]int{}
			var last string
			err := walkCandidatePages(context.Background(), 200, page, func(candidate notificationCandidate) bool {
				seen[candidate.UserID]++
				last = candidate.UserID
				return true
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if len(seen) != total {
				t.Fatalf("visited %d distinct customers, want %d", len(seen), total)
			}
			for id, count := range seen {
				if count != 1 {
					t.Fatalf("customer %s was visited %d times", id, count)
				}
			}
			if total > 0 {
				want := fmt.Sprintf("%08d-0000-4000-8000-000000000000", total-1)
				if last != want {
					t.Fatalf("the last customer visited was %s, want %s", last, want)
				}
			}
			// One page per full batch, plus the short page that ends the walk.
			wantCalls := total/200 + 1
			if *calls != wantCalls {
				t.Fatalf("read %d pages, want %d", *calls, wantCalls)
			}
		})
	}
}

// TestWalkCandidatePagesStopsWhenAsked covers the two ways a walk ends early:
// the visitor declining to continue, and the context being cancelled.
func TestWalkCandidatePagesStopsWhenAsked(t *testing.T) {
	page, calls := fakeCandidatePages(500)
	visited := 0
	err := walkCandidatePages(context.Background(), 200, page, func(notificationCandidate) bool {
		visited++
		return visited < 250
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if visited != 250 || *calls != 2 {
		t.Fatalf("visited %d customers over %d pages, want 250 over 2", visited, *calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	page, calls = fakeCandidatePages(500)
	err = walkCandidatePages(ctx, 200, page, func(notificationCandidate) bool {
		t.Fatal("a cancelled walk visited a customer")
		return false
	})
	if err != nil || *calls != 0 {
		t.Fatalf("a cancelled walk read %d pages with error %v", *calls, err)
	}
}

// TestApplySuppressionVetoesOnlyMarketing pins the suppression list's reach: a
// marketing message to a suppressed customer is held back under its own
// reason whether the policy allowed or deferred it, a refusal already on
// record keeps its reason, and a transactional message is untouched.
func TestApplySuppressionVetoesOnlyMarketing(t *testing.T) {
	suppressed := notificationCandidate{Suppressed: true}
	unsuppressed := notificationCandidate{}
	later := time.Now().Add(time.Hour)

	allowed := commerce.DeliveryDecision{Allow: true, Class: commerce.ClassMarketing, Reason: "allowed"}
	if got := applySuppression(allowed, suppressed); got.Allow || got.Reason != "suppressed" || got.Class != commerce.ClassMarketing {
		t.Fatalf("an allowed marketing message to a suppressed customer became %+v", got)
	}
	deferred := commerce.DeliveryDecision{Class: commerce.ClassMarketing, Reason: "deferred", DeferUntil: later}
	if got := applySuppression(deferred, suppressed); got.Allow || !got.DeferUntil.IsZero() || got.Reason != "suppressed" {
		t.Fatalf("a deferred marketing message to a suppressed customer became %+v", got)
	}
	refused := commerce.DeliveryDecision{Class: commerce.ClassMarketing, Reason: "frequency_cap"}
	if got := applySuppression(refused, suppressed); got.Reason != "frequency_cap" {
		t.Fatalf("a refusal already on record lost its reason: %+v", got)
	}
	transactional := commerce.DeliveryDecision{Allow: true, Class: commerce.ClassTransactional, Reason: "allowed"}
	if got := applySuppression(transactional, suppressed); !got.Allow {
		t.Fatalf("a transactional message was held back by a suppression: %+v", got)
	}
	if got := applySuppression(allowed, unsuppressed); !got.Allow {
		t.Fatalf("a customer with no suppression was held back: %+v", got)
	}
	if campaignSuppression(commerce.DeliveryDecision{Reason: "suppressed"}) != "suppressed" {
		t.Fatal("the campaign record does not name the suppression list")
	}
}

// TestReminderPlanSendsOneCountdown is the duplicate-reminder defect: a
// customer with an entitlement used to get the Remnawave expiry countdown and
// the entitlement renewal countdown for the same end date. Exactly one schedule
// applies, chosen by whether the subscription has an entitlement behind it.
func TestReminderPlanSendsOneCountdown(t *testing.T) {
	sold := reminderPlan(true)
	if sold.ExpiryFromRemnawave || !sold.RenewalFromEntitlement {
		t.Fatalf("a subscription with an entitlement plans %+v", sold)
	}
	legacy := reminderPlan(false)
	if !legacy.ExpiryFromRemnawave || legacy.RenewalFromEntitlement {
		t.Fatalf("a legacy subscription plans %+v", legacy)
	}
	for _, has := range []bool{true, false} {
		plan := reminderPlan(has)
		if plan.ExpiryFromRemnawave == plan.RenewalFromEntitlement {
			t.Fatalf("hasEntitlement=%v plans both or neither: %+v", has, plan)
		}
	}
}

// TestWalkCandidatePagesReportsAStoreFailure makes sure a failing page is
// surfaced rather than read as the end of the list.
func TestWalkCandidatePagesReportsAStoreFailure(t *testing.T) {
	failure := errors.New("connection reset")
	err := walkCandidatePages(context.Background(), 200, func(context.Context, string, int) ([]notificationCandidate, error) {
		return nil, failure
	}, func(notificationCandidate) bool { return true })
	if !errors.Is(err, failure) {
		t.Fatalf("walk returned %v, want the store failure", err)
	}
}
