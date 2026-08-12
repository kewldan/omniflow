package accountshop

import (
	"testing"

	"github.com/omniflow/omniflow/internal/goods"
)

// The delivery state is what the customer reads, so every stored combination
// has to land on exactly one of them — and the ambiguous outcome has to land
// somewhere that offers a person rather than a retry.
func TestStoredStateMapsOntoOneCustomerVisibleState(t *testing.T) {
	cases := []struct {
		name    string
		record  deliveryRecord
		want    DeliveryState
		handoff bool
	}{
		{
			name:   "an unpaid order has submitted nothing",
			record: deliveryRecord{orderState: "pending", goodsStatus: "quoted"},
			want:   StateAwaitingPayment,
		},
		{
			name:   "an abandoned order is not waiting for payment forever",
			record: deliveryRecord{orderState: "expired", goodsStatus: "quoted"},
			want:   StateCancelled,
		},
		{
			name:   "paid, with the delivery row not yet visible",
			record: deliveryRecord{orderState: "paid", goodsStatus: "paid"},
			want:   StateQueued,
		},
		{
			name: "paid and waiting for the worker",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "paid", deliveryStatus: "pending",
			},
			want: StateQueued,
		},
		{
			name: "handed to the gateway, no answer yet",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "delivering",
				deliveryStatus: "submitted", attempts: 1,
			},
			want: StateSubmitted,
		},
		{
			name: "the gateway took it and gave a reference, so we ask rather than resubmit",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "delivering",
				deliveryStatus: "submitted", attempts: 2, submitted: true,
			},
			want: StatePolling,
		},
		{
			name: "a transient fault on the backoff schedule is a wait, not a failure",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "delivering", deliveryStatus: "submitted",
				attempts: 2, failureClass: goods.FailureProviderUnavailable,
			},
			want: StateDelayed,
		},
		{
			name: "an exhausted operator balance is still the operator's to fix",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "delivering", deliveryStatus: "submitted",
				attempts: 3, failureClass: goods.FailureProviderBalance,
			},
			want: StateDelayed,
		},
		{
			name: "delivered",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "delivered", deliveryStatus: "delivered", attempts: 1,
			},
			want: StateDelivered,
		},
		{
			name: "an ambiguous outcome parks for a person",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "needs_review", deliveryStatus: "needs_review",
				attempts: 1, failureClass: goods.FailureAmbiguous,
			},
			want: StateNeedsReview, handoff: true,
		},
		{
			name: "a permanent failure with the credit already in the wallet",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "refunded", deliveryStatus: "failed",
				attempts: 1, failureClass: goods.FailureRecipientInvalid, refunded: true,
			},
			want: StateRefunded,
		},
		{
			name: "a terminal failure whose refund has not landed is not called refunded",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "failed", deliveryStatus: "failed",
				attempts: 6, failureClass: goods.FailurePermanent,
			},
			want: StateFailed, handoff: true,
		},
		{
			name: "an operator stopped it",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "failed", deliveryStatus: "cancelled",
			},
			want: StateCancelled,
		},
		{
			name: "a status this build does not know is reported as in flight, not invented",
			record: deliveryRecord{
				orderState: "paid", goodsStatus: "paid", deliveryStatus: "teleported",
			},
			want: StateQueued,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			delivery := classifyDelivery(testCase.record)
			if delivery.State != testCase.want {
				t.Fatalf("state = %q, want %q", delivery.State, testCase.want)
			}
			if delivery.SupportHandoff != testCase.handoff {
				t.Fatalf("supportHandoff = %v, want %v", delivery.SupportHandoff, testCase.handoff)
			}
			if delivery.Attempts != testCase.record.attempts {
				t.Fatalf("attempts = %d, want %d", delivery.Attempts, testCase.record.attempts)
			}
		})
	}
}

// A delivery with nothing wrong with it must not carry a failure reason: a
// screen that renders one would tell somebody their purchase failed while it is
// still on its way. A delayed one does keep its class, because "why is this
// taking so long" is the question that state exists to answer.
func TestAFailureReasonAppearsOnlyWhereSomethingActuallyFailed(t *testing.T) {
	inFlight := classifyDelivery(deliveryRecord{
		orderState: "paid", goodsStatus: "delivering", deliveryStatus: "submitted",
		attempts: 2, submitted: true,
	})
	if inFlight.State != StatePolling {
		t.Fatalf("state = %q, want %q", inFlight.State, StatePolling)
	}
	if inFlight.FailureReason != "" {
		t.Fatalf("a polling delivery reported a failure reason: %q", inFlight.FailureReason)
	}

	delayed := classifyDelivery(deliveryRecord{
		orderState: "paid", goodsStatus: "delivering", deliveryStatus: "submitted",
		attempts: 2, submitted: true, failureClass: goods.FailureProviderUnavailable,
	})
	// A reference plus a retryable class is a poll that failed, which is a wait
	// rather than a success in progress.
	if delayed.State != StateDelayed {
		t.Fatalf("state = %q, want %q", delayed.State, StateDelayed)
	}
	if delayed.FailureReason != goods.FailureProviderUnavailable {
		t.Fatalf("a delayed delivery lost its reason: %q", delayed.FailureReason)
	}

	delivered := classifyDelivery(deliveryRecord{
		orderState: "paid", goodsStatus: "delivered", deliveryStatus: "delivered",
		failureClass: goods.FailureRetryable,
	})
	if delivered.FailureReason != "" {
		t.Fatalf("a delivered order kept an earlier failure: %q", delivered.FailureReason)
	}
}

// A parked delivery always names a reason, even if the row was written before
// the failure class was recorded, because "under review" with no explanation is
// the state customers most need explained.
func TestAParkedDeliveryAlwaysExplainsItself(t *testing.T) {
	parked := classifyDelivery(deliveryRecord{
		orderState: "paid", goodsStatus: "needs_review", deliveryStatus: "needs_review",
	})
	if parked.FailureReason != goods.FailureAmbiguous {
		t.Fatalf("failureReason = %q, want %q", parked.FailureReason, goods.FailureAmbiguous)
	}
	if !parked.SupportHandoff {
		t.Fatal("a parked delivery offered no route to a person")
	}
}
