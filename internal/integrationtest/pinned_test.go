//go:build integration

package integrationtest

import (
	"context"
	"testing"

	"github.com/omniflow/omniflow/internal/panelpg"
)

func (stub *botTelegram) count(method string) int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	count := 0
	for _, request := range stub.requests {
		if request.Method == method {
			count++
		}
	}
	return count
}

// A published pinned message is sent and pinned once per reachable customer,
// /start does not send it again, and retiring it unpins it.
func TestPinnedMessageReachesEverybodyOnceAndUnpins(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	flow := newBotFlow(ctx, t, harness)
	operations := newOperations(t, harness)
	actor := harness.operator(ctx, t, "pinned@omniflow.test")
	flow.customerOf(ctx, t, 715001)
	flow.customerOf(ctx, t, 715002)

	message, err := operations.PublishPinned(ctx, panelpg.PinnedMessage{
		TextRU: "Новые сервера", TextEN: "New servers", Pin: true, ShowOnStart: true,
	}, actor)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := operations.PublishPinned(ctx, panelpg.PinnedMessage{TextRU: "<b>broken", TextEN: "x"}, actor); err == nil {
		t.Fatal("broken markup was published")
	}
	notifier := flow.notifier()
	notifier.RunOnce(ctx)
	notifier.RunOnce(ctx)
	if pins := flow.telegram.count("pinChatMessage"); pins != 2 {
		t.Fatalf("pins = %d, want one per customer", pins)
	}
	flow.say(ctx, 715001, "/start")
	if pins := flow.telegram.count("pinChatMessage"); pins != 2 {
		t.Fatalf("/start pinned it again: %d", pins)
	}
	if err := operations.RetirePinned(ctx, message.ID, actor); err != nil {
		t.Fatalf("retire: %v", err)
	}
	notifier.RunOnce(ctx)
	if unpins := flow.telegram.count("unpinChatMessage"); unpins != 2 {
		t.Fatalf("unpins = %d, want 2", unpins)
	}
}
