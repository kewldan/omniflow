package botapp

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
)

// reachabilityStore records what the inbound side told it.
type reachabilityStore struct {
	fakeIdentityStore
	reachable []int64
	blocked   []int64
}

func (store *reachabilityStore) RecordReachable(_ context.Context, telegramID int64) error {
	store.reachable = append(store.reachable, telegramID)
	return nil
}

func (store *reachabilityStore) RecordBlockedChat(_ context.Context, telegramID int64) error {
	store.blocked = append(store.blocked, telegramID)
	return nil
}

func membershipUpdate(telegramID int64, status models.ChatMemberType) *models.Update {
	return &models.Update{MyChatMember: &models.ChatMemberUpdated{
		Chat:          models.Chat{ID: telegramID, Type: models.ChatTypePrivate},
		From:          models.User{ID: telegramID},
		NewChatMember: models.ChatMember{Type: status},
	}}
}

// Any update from a customer's private chat proves the chat works, and the
// bot's own membership in it says when the customer blocked or restarted it.
// Nothing else could clear a blocked state: blocked customers are never sent
// anything, so no send ever succeeds against them.
func TestInboundUpdatesKeepDeliveryHealthHonest(t *testing.T) {
	t.Parallel()
	client, _ := telegramStub(t)
	store := &reachabilityStore{fakeIdentityStore: fakeIdentityStore{lookupID: 44}}
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, &fakeRemnawave{}, "")

	app.HandleDefault(context.Background(), client, membershipUpdate(101, models.ChatMemberTypeBanned))
	app.HandleDefault(context.Background(), client, membershipUpdate(102, models.ChatMemberTypeMember))
	app.HandleCallback(context.Background(), client, callbackUpdate(callbackPrefix+routeSupport))
	app.inboundMiddleware(app.HandleCallback)(context.Background(), client, callbackUpdate(callbackPrefix+routeSupport))

	if len(store.blocked) != 1 || store.blocked[0] != 101 {
		t.Fatalf("blocked = %v, want the account that blocked the bot", store.blocked)
	}
	// The bare HandleCallback call is a registered handler's body, which the
	// middleware covers; only the wrapped call notes the update.
	if len(store.reachable) != 2 || store.reachable[0] != 102 || store.reachable[1] != 123456789 {
		t.Fatalf("reachable = %v, want the restarted account and the account that tapped", store.reachable)
	}
}

// A group message says nothing about the customer's private chat with the bot.
func TestInboundSenderIgnoresGroups(t *testing.T) {
	t.Parallel()
	group := &models.Update{Message: &models.Message{From: &models.User{ID: 5}, Chat: models.Chat{ID: -100, Type: models.ChatTypeSupergroup}}}
	if _, found := inboundSender(group); found {
		t.Fatal("a group message was taken as proof the private chat works")
	}
	private := &models.Update{Message: &models.Message{From: &models.User{ID: 5}, Chat: models.Chat{ID: 5, Type: models.ChatTypePrivate}}}
	if id, found := inboundSender(private); !found || id != 5 {
		t.Fatalf("inboundSender(private) = %d, %t", id, found)
	}
}
