package botapp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// telegramStub answers every Bot API call with success, so a handler can be
// driven end to end without Telegram. Only the requests are of interest.
func telegramStub(t *testing.T) (*telegram.Bot, *[]string) {
	t.Helper()
	var (
		mutex sync.Mutex
		calls []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		calls = append(calls, request.URL.Path)
		mutex.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}))
	t.Cleanup(server.Close)
	client, err := telegram.New("test-token", telegram.WithServerURL(server.URL), telegram.WithSkipGetMe())
	if err != nil {
		t.Fatalf("telegram client: %v", err)
	}
	return client, &calls
}

// cancellingStore counts the flow cancellations a handler performs.
type cancellingStore struct {
	fakeIdentityStore
	cancelled int
}

func (store *cancellingStore) CancelSession(context.Context, int64) error {
	store.cancelled++
	return nil
}

func callbackUpdate(data string) *models.Update {
	return &models.Update{ID: 7, CallbackQuery: &models.CallbackQuery{
		ID:   "cb-1",
		From: models.User{ID: 123456789, LanguageCode: "en"},
		Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 5, Chat: models.Chat{ID: 123456789, Type: models.ChatTypePrivate},
		}},
	}}
}

func TestAnyCallbackAbandonsTheOpenFreeTextFlow(t *testing.T) {
	t.Parallel()
	client, _ := telegramStub(t)
	store := &cancellingStore{fakeIdentityStore: fakeIdentityStore{lookupID: 44}}
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, &fakeRemnawave{telegramUser: remnawave.User{ID: 44, Username: "linked", Status: "ACTIVE"}}, "")

	app.HandleCallback(context.Background(), client, callbackUpdate(callbackPrefix+routeSupport))
	if store.cancelled != 1 {
		t.Fatalf("a navigation tap must abandon the open prompt, got %d cancellations", store.cancelled)
	}
	app.HandleCallback(context.Background(), client, callbackUpdate(actionPrefix+"revoke-confirm"))
	if store.cancelled != 2 {
		t.Fatalf("an action tap must abandon the open prompt too, got %d cancellations", store.cancelled)
	}
}

func TestAGroupCallbackDoesNothing(t *testing.T) {
	t.Parallel()
	client, calls := telegramStub(t)
	store := &cancellingStore{fakeIdentityStore: fakeIdentityStore{lookupID: 44}}
	app := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, &fakeRemnawave{}, "")
	update := callbackUpdate(callbackPrefix + routeHome)
	update.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeGroup

	app.HandleCallback(context.Background(), client, update)
	if store.cancelled != 0 {
		t.Fatal("a group chat must not touch private flow state")
	}
	for _, call := range *calls {
		if call != "/bottest-token/answerCallbackQuery" {
			t.Fatalf("a group callback may only be acknowledged, got %s", call)
		}
	}
}

// levelCounter counts the records logged at Error or above.
type levelCounter struct {
	mutex  sync.Mutex
	errors int
}

func (counter *levelCounter) Enabled(context.Context, slog.Level) bool { return true }
func (counter *levelCounter) Handle(_ context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		counter.mutex.Lock()
		counter.errors++
		counter.mutex.Unlock()
	}
	return nil
}
func (counter *levelCounter) WithAttrs([]slog.Attr) slog.Handler { return counter }
func (counter *levelCounter) WithGroup(string) slog.Handler      { return counter }

// A button the previous bot left in the chat opens the main menu as a new
// message — the old one may be a photo — and is not an error.
func TestAnEarlierBotsButtonOpensTheMainMenu(t *testing.T) {
	t.Parallel()
	client, calls := telegramStub(t)
	counter := &levelCounter{}
	store := &cancellingStore{fakeIdentityStore: fakeIdentityStore{lookupID: 44}}
	app := New(slog.New(counter), store, &fakeRemnawave{telegramUser: remnawave.User{ID: 44, Username: "linked", Status: "ACTIVE"}}, "")

	app.HandleDefault(context.Background(), client, callbackUpdate("menu_buy"))
	seen := map[string]int{}
	for _, call := range *calls {
		seen[call]++
	}
	if seen["/bottest-token/answerCallbackQuery"] != 1 || seen["/bottest-token/sendMessage"] != 1 ||
		seen["/bottest-token/editMessageReplyMarkup"] != 1 || seen["/bottest-token/editMessageText"] != 0 {
		t.Fatalf("calls %v", *calls)
	}
	if counter.errors != 0 {
		t.Fatalf("an earlier bot's button logged %d errors", counter.errors)
	}

	// An action this build does not have shows the main menu in place.
	*calls = nil
	app.HandleCallback(context.Background(), client, callbackUpdate(actionPrefix+"no-such-action"))
	if counter.errors != 0 {
		t.Fatalf("an unknown action logged %d errors", counter.errors)
	}
}
