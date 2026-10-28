//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	telegram "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/omniflow/omniflow/internal/botapp"
	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// botRequest is one Bot API call the bot made, with the form fields it sent.
type botRequest struct {
	Method string
	Fields map[string]string
}

// botTelegram is a Bot API stand-in that accepts every call and records it.
//
// The bot under test talks to it through the real go-telegram client, so what
// is recorded is exactly what Telegram would have received — including a
// callback longer than Telegram accepts, which a view-level test cannot see.
type botTelegram struct {
	mutex    sync.Mutex
	requests []botRequest
	// refuse, when set, answers a method with a Bot API error instead.
	refuse map[string]string
}

func (stub *botTelegram) serve(writer http.ResponseWriter, request *http.Request) {
	method := request.URL.Path[strings.LastIndex(request.URL.Path, "/")+1:]
	fields := map[string]string{}
	if err := request.ParseMultipartForm(1 << 20); err == nil && request.MultipartForm != nil {
		for name, values := range request.MultipartForm.Value {
			if len(values) > 0 {
				fields[name] = values[0]
			}
		}
	}
	stub.mutex.Lock()
	stub.requests = append(stub.requests, botRequest{Method: method, Fields: fields})
	refusal, refused := stub.refuse[method]
	stub.mutex.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	if refused {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"ok":false,"error_code":403,"description":` + strconvQuote(refusal) + `}`))
		return
	}
	switch method {
	case "answerCallbackQuery", "sendChatAction":
		_, _ = writer.Write([]byte(`{"ok":true,"result":true}`))
	default:
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`))
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// screens returns the text of every message sent or edited, in order.
func (stub *botTelegram) screens() []botRequest {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	screens := make([]botRequest, 0, len(stub.requests))
	for _, request := range stub.requests {
		if request.Method == "sendMessage" || request.Method == "editMessageText" {
			screens = append(screens, request)
		}
	}
	return screens
}

// lastScreen is the screen the customer is looking at now.
func (stub *botTelegram) lastScreen(t *testing.T) botRequest {
	t.Helper()
	screens := stub.screens()
	if len(screens) == 0 {
		t.Fatal("the bot sent no screen")
	}
	return screens[len(screens)-1]
}

// callbacks lists the callback data of every button on a screen.
func (request botRequest) callbacks(t *testing.T) []string {
	t.Helper()
	raw := request.Fields["reply_markup"]
	if raw == "" {
		return nil
	}
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		t.Fatalf("decode the keyboard: %v", err)
	}
	data := make([]string, 0, 8)
	for _, buttons := range markup.InlineKeyboard {
		for _, button := range buttons {
			if button.CallbackData != "" {
				data = append(data, button.CallbackData)
			}
		}
	}
	return data
}

// botRemnawave is a Remnawave stand-in keyed by VPN user, recording the
// deletions the bot asks for.
type botRemnawave struct {
	mutex   sync.Mutex
	users   map[int64]remnawave.User
	devices map[int64][]remnawave.Device
	deleted []string
}

func (fake *botRemnawave) User(_ context.Context, id int64) (remnawave.User, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if user, found := fake.users[id]; found {
		return user, nil
	}
	return remnawave.User{}, remnawave.ErrNotFound
}

func (fake *botRemnawave) UserByTelegramID(context.Context, int64) (remnawave.User, error) {
	return remnawave.User{}, remnawave.ErrNotFound
}

func (fake *botRemnawave) Subscription(context.Context, int64) (remnawave.Subscription, error) {
	return remnawave.Subscription{}, remnawave.ErrNotFound
}

func (fake *botRemnawave) Devices(_ context.Context, id int64) (remnawave.Devices, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	devices := fake.devices[id]
	return remnawave.Devices{Total: len(devices), Devices: devices}, nil
}

func (fake *botRemnawave) DeleteDevice(_ context.Context, id int64, hwid string) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.deleted = append(fake.deleted, strings.Join([]string{itoa64(id), hwid}, "/"))
	return nil
}

func (fake *botRemnawave) DeleteAllDevices(_ context.Context, id int64) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.deleted = append(fake.deleted, itoa64(id)+"/*")
	return nil
}

func (fake *botRemnawave) RevokeSubscription(context.Context, int64) error { return nil }

func itoa64(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// botFlow is the Telegram surface wired against the migrated database, the
// way cmd/bot wires it, and driven by updates rather than by method calls.
type botFlow struct {
	harness   *harness
	app       *botapp.App
	store     *botapp.PostgresStore
	commerce  *botapp.Commerce
	client    *telegram.Bot
	telegram  *botTelegram
	remnawave *botRemnawave
	update    int64
}

func newBotFlow(ctx context.Context, t *testing.T, harness *harness) *botFlow {
	t.Helper()
	stub := &botTelegram{refuse: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(stub.serve))
	t.Cleanup(server.Close)
	client, err := telegram.New("test-token", telegram.WithServerURL(server.URL), telegram.WithSkipGetMe())
	if err != nil {
		t.Fatalf("telegram client: %v", err)
	}
	store, err := botapp.NewPostgresStore(ctx, harness.url)
	if err != nil {
		t.Fatalf("build bot store: %v", err)
	}
	t.Cleanup(store.Close)
	logger := slog.New(slog.DiscardHandler)
	orders := commercepg.New(harness.pool, nil, testOptions())
	settings := botapp.CommerceSettings{
		Currency: "RUB", PublicURL: "https://example.test",
		TermsURL: "https://example.test/terms", MultiSubscription: true,
	}
	surface := botapp.NewCommerce(logger, store, orders, nil, settings)
	fake := &botRemnawave{users: map[int64]remnawave.User{}, devices: map[int64][]remnawave.Device{}}
	app := botapp.New(logger, store, fake, "")
	app.EnableCommerce(store, surface, nil, botapp.NewSender(client, store, logger), settings)
	return &botFlow{
		harness: harness, app: app, store: store, commerce: surface,
		client: client, telegram: stub, remnawave: fake,
	}
}

// tap delivers a button press on the message with the given identifier.
func (flow *botFlow) tap(ctx context.Context, telegramID int64, messageID int, data string) {
	flow.update++
	flow.app.HandleCallback(ctx, flow.client, &models.Update{ID: flow.update, CallbackQuery: &models.CallbackQuery{
		ID:   "cb-" + itoa64(flow.update),
		From: models.User{ID: telegramID, LanguageCode: "en", Username: "sender"},
		Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: messageID, Chat: models.Chat{ID: telegramID, Type: models.ChatTypePrivate},
		}},
	}})
}

// say delivers a plain text message from the customer.
func (flow *botFlow) say(ctx context.Context, telegramID int64, body string) {
	flow.update++
	flow.app.HandleDefault(ctx, flow.client, &models.Update{ID: flow.update, Message: &models.Message{
		ID:   int(flow.update),
		From: &models.User{ID: telegramID, LanguageCode: "en", Username: "sender"},
		Chat: models.Chat{ID: telegramID, Type: models.ChatTypePrivate},
		Text: body,
	}})
}

// customerOf resolves the customer a Telegram account is linked to.
func (flow *botFlow) customerOf(ctx context.Context, t *testing.T, telegramID int64) string {
	t.Helper()
	customer, err := flow.store.EnsureCustomer(ctx, telegramID, "en")
	if err != nil {
		t.Fatalf("resolve the customer: %v", err)
	}
	return customer.ID
}
