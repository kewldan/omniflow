package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/customerauthpg"
)

// newTelegramSignInRouter mounts the customer surface over an identity service
// that holds a bot token but can reach no database.
//
// That is enough for what these tests assert: the widget handler decodes the
// body and verifies the signature before any query runs, so a payload whose
// hash does not verify is answered 401 without the pool ever being dialled. A
// payload the decoder refuses is answered 400 earlier still — and telling the
// two apart is the whole point.
func newTelegramSignInRouter(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://omniflow:omniflow@127.0.0.1:1/omniflow")
	if err != nil {
		t.Fatalf("build pool: %v", err)
	}
	t.Cleanup(pool.Close)
	key := bytes.Repeat([]byte{7}, 32)
	auth, err := customerauthpg.New(pool, key, customerauthpg.Options{TelegramBotToken: "000000:unit-token"})
	if err != nil {
		t.Fatalf("build identity service: %v", err)
	}
	handlers := NewAccountHandlers(AccountOptions{Auth: auth, Logger: slog.New(slog.DiscardHandler)})
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

func postTelegramWidget(router http.Handler, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/account/auth/telegram", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

// The widget posts `id` and `auth_date` as numbers. The handler used to decode
// into map[string]string and answer 400 to every real browser; it must now get
// as far as the signature check, which is what the 401 proves.
func TestTelegramSignInDecodesTheWidgetsNumericFields(t *testing.T) {
	router := newTelegramSignInRouter(t)
	recorder := postTelegramWidget(
		router, `{"id":770001,"first_name":"Playwright","auth_date":1786660859,"hash":"deadbeef"}`,
	)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("numeric widget payload answered %d (%s), want 401 from the signature check",
			recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "sign_in_rejected") {
		t.Fatalf("body = %s, want a sign_in_rejected problem", recorder.Body.String())
	}
}

// A large Telegram ID must survive the decoder with its digits intact; a
// float64 decode would render it in exponent notation and the signature would
// never verify. The 401 here is the same as above — the assertion is that the
// handler did not refuse the document shape.
func TestTelegramSignInAcceptsStringFieldsToo(t *testing.T) {
	router := newTelegramSignInRouter(t)
	recorder := postTelegramWidget(
		router, `{"id":"7700012345678901","first_name":"Playwright","auth_date":"1786660859","hash":"deadbeef"}`,
	)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("string widget payload answered %d, want 401", recorder.Code)
	}
}

func TestTelegramSignInRefusesANonWidgetDocument(t *testing.T) {
	router := newTelegramSignInRouter(t)
	cases := map[string]string{
		"nested":        `{"id":1,"auth_date":2,"hash":"x","user":{"id":1}}`,
		"array":         `[1,2]`,
		"two documents": `{"id":1}{"id":2}`,
		"empty":         ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := postTelegramWidget(router, body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("answered %d, want 400", recorder.Code)
			}
		})
	}
}
