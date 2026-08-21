//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
)

// Requesting deletion ends every other session the customer holds and keeps
// the one that asked, so cancelling stays possible from it and nothing else.
func TestRequestingDeletionEndsTheOtherSessions(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	identity := newCustomerService(t, harness, func() time.Time { return clock })
	referral := referralService(t, harness)
	handlers := apihttp.NewAccountHandlers(apihttp.AccountOptions{
		Auth: identity, Referral: referral, Logger: slog.New(slog.DiscardHandler), CookieSecure: true,
	})
	router := chi.NewRouter()
	handlers.Mount(router)

	// Two devices: a phone that will ask, and a laptop that will not.
	phone, err := identity.SignInWithTelegram(ctx, signedWidget(700800, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("phone sign-in: %v", err)
	}
	laptop, err := identity.SignInWithTelegram(ctx, signedWidget(700800, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("laptop sign-in: %v", err)
	}
	principal, err := identity.Resolve(ctx, phone.Token)
	if err != nil {
		t.Fatalf("resolve phone: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/account/privacy/deletion",
		strings.NewReader(`{"confirm":true,"reason":"moving on"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", principal.CSRFToken)
	request.AddCookie(&http.Cookie{Name: "__Host-omniflow_account", Value: phone.Token})
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("request deletion answered %d: %s", recorder.Code, recorder.Body.String())
	}

	if _, err = identity.Resolve(ctx, laptop.Token); !errors.Is(err, customerauthpg.ErrSessionInvalid) {
		t.Fatalf("the other device's session survived a deletion request: %v", err)
	}
	if _, err = identity.Resolve(ctx, phone.Token); err != nil {
		t.Fatalf("the requesting session was ended too, so the request cannot be cancelled: %v", err)
	}
}
