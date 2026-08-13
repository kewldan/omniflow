//go:build integration

package integrationtest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/customerauthpg"
	apihttp "github.com/omniflow/omniflow/internal/httpapi"
)

const customerBotToken = "424242:AA-integration-token"

func newCustomerService(t *testing.T, harness *harness, now func() time.Time) *customerauthpg.Service {
	t.Helper()
	service, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		TelegramBotToken: customerBotToken,
		MagicLinkEnabled: true,
		PublicURL:        "https://vpn.test",
		Clock:            now,
	})
	if err != nil {
		t.Fatalf("build customer service: %v", err)
	}
	return service
}

// newAccountHandlersForTest builds the customer API over a real service.
//
// CookieSecure is off because httptest speaks plain HTTP; every other gate —
// the session lookup, the CSRF check, the security headers — is the production
// one.
func newAccountHandlersForTest(service *customerauthpg.Service) *apihttp.AccountHandlers {
	// Secure, because the __Host- prefix and the Secure attribute travel
	// together: a browser rejects the prefix without it, so the API reads the
	// unprefixed name when the cookie is not Secure. Building the handlers
	// insecure and then presenting a __Host- cookie describes a pairing that
	// cannot exist, and the request arrives with no session the API can see.
	return apihttp.NewAccountHandlers(apihttp.AccountOptions{
		Auth: service, Logger: slog.New(slog.DiscardHandler), CookieSecure: true,
	})
}

func mountAccount(handlers *apihttp.AccountHandlers) http.Handler {
	router := chi.NewRouter()
	handlers.Mount(router)
	return router
}

// signedWidget produces the payload Telegram's login widget would deliver.
func signedWidget(telegramID int64, at time.Time) url.Values {
	values := url.Values{
		"id":         {strconv.FormatInt(telegramID, 10)},
		"first_name": {"Alexey"},
		"auth_date":  {strconv.FormatInt(at.Unix(), 10)},
	}
	pairs := make([]string, 0, len(values))
	for key, entries := range values {
		pairs = append(pairs, key+"="+entries[0])
	}
	sort.Strings(pairs)
	secret := sha256.Sum256([]byte(customerBotToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(pairs, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values
}

func TestTelegramSignInProvisionsThenReusesOneCustomer(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	first, err := service.SignInWithTelegram(ctx, signedWidget(700100, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	second, err := service.SignInWithTelegram(ctx, signedWidget(700100, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}

	// The same Telegram account must land on the same customer. A second sign-in
	// creating a second customer would strand the first one's subscription.
	if first.Customer.ID != second.Customer.ID {
		t.Fatalf("two sign-ins produced two customers: %s and %s", first.Customer.ID, second.Customer.ID)
	}
	if first.Token == second.Token {
		t.Fatal("two sign-ins reused one session token")
	}

	// The bot's own identity lookup must find the customer the web created, which
	// is what makes the two surfaces one account.
	var identities int
	if err = harness.pool.QueryRow(ctx,
		`SELECT count(*) FROM identities WHERE provider = 'telegram' AND provider_subject = $1 AND status = 'active'`,
		"700100",
	).Scan(&identities); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if identities != 1 {
		t.Fatalf("identities = %d, want exactly 1", identities)
	}
}

func TestResolveSlidesRotatesAndRefusesRevokedSessions(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	result, err := service.SignInWithTelegram(ctx, signedWidget(700200, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}

	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if principal.RotatedToken != "" {
		t.Fatal("a freshly created session was rotated immediately")
	}
	if principal.ReauthenticationRequired {
		t.Fatal("a session that just signed in was asked to re-authenticate")
	}

	// Past the re-authentication window but well inside the session, the session
	// is still valid and only the sensitive actions are gated.
	clock = clock.Add(customerauth.DefaultSessionPolicy.ReauthWindow + time.Minute)
	if principal, err = service.Resolve(ctx, result.Token); err != nil {
		t.Fatalf("resolve after the reauth window: %v", err)
	}
	if !principal.ReauthenticationRequired {
		t.Fatal("an old session was not flagged for re-authentication")
	}

	// Past the rotation window the token is swapped, and the old one stops
	// resolving so a captured cookie has a bounded life.
	clock = clock.Add(customerauth.DefaultSessionPolicy.RotateAfter + time.Minute)
	rotated, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve at rotation: %v", err)
	}
	if rotated.RotatedToken == "" {
		t.Fatal("a session past the rotation window was not rotated")
	}
	if _, err = service.Resolve(ctx, result.Token); !errors.Is(err, customerauthpg.ErrSessionInvalid) {
		t.Fatalf("the pre-rotation token still resolves: %v", err)
	}
	if _, err = service.Resolve(ctx, rotated.RotatedToken); err != nil {
		t.Fatalf("the rotated token does not resolve: %v", err)
	}

	// Signing out everywhere must end the calling session too.
	if _, err = service.SignOutEverywhere(
		ctx, rotated.Customer.ID, rotated.SessionID, false, customerauthpg.RequestContext{},
	); err != nil {
		t.Fatalf("sign out everywhere: %v", err)
	}
	if _, err = service.Resolve(ctx, rotated.RotatedToken); !errors.Is(err, customerauthpg.ErrSessionInvalid) {
		t.Fatalf("a revoked session still resolves: %v", err)
	}
}

func TestSuspendedCustomersCannotUseALiveSession(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	result, err := service.SignInWithTelegram(ctx, signedWidget(700300, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if _, err = harness.pool.Exec(ctx,
		`UPDATE users SET status = 'suspended', suspended_at = now() WHERE id = $1::uuid`,
		result.Customer.ID,
	); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// The session itself is untouched; the account is what became unusable, and
	// the distinction is what lets the panel explain it instead of looping the
	// customer through a sign-in that would also fail.
	if _, err = service.Resolve(ctx, result.Token); !errors.Is(err, customerauthpg.ErrAccountInactive) {
		t.Fatalf("resolve = %v, want ErrAccountInactive", err)
	}
	if _, err = service.SignInWithTelegram(
		ctx, signedWidget(700300, now), customerauthpg.RequestContext{},
	); !errors.Is(err, customerauthpg.ErrAccountInactive) {
		t.Fatalf("sign-in = %v, want ErrAccountInactive", err)
	}
}

func TestMagicLinkIsSingleUseUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	seed, err := service.SignInWithTelegram(ctx, signedWidget(700400, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	link, err := service.IssueMagicLink(ctx, seed.Customer.ID, customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("issue magic link: %v", err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatal("the issued link carries no token")
	}

	// Two browsers racing on one link must produce exactly one session. Single
	// use is a property of the consuming UPDATE, not of the code around it.
	var (
		wait      sync.WaitGroup
		mutex     sync.Mutex
		succeeded int
		failures  []error
	)
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, redeemErr := service.CompleteMagicLink(ctx, token, customerauthpg.RequestContext{})
			mutex.Lock()
			defer mutex.Unlock()
			if redeemErr == nil {
				succeeded++
				return
			}
			failures = append(failures, redeemErr)
		}()
	}
	wait.Wait()

	if succeeded != 1 {
		t.Fatalf("%d concurrent redemptions succeeded, want exactly 1 (failures: %v)", succeeded, failures)
	}
	for _, failure := range failures {
		if !errors.Is(failure, customerauth.ErrMagicLinkInvalid) {
			t.Fatalf("losing redemption failed with %v, want ErrMagicLinkInvalid", failure)
		}
	}

	// A fifth attempt after the race is the same refusal, so a spent link never
	// becomes usable again.
	if _, err = service.CompleteMagicLink(
		ctx, token, customerauthpg.RequestContext{},
	); !errors.Is(err, customerauth.ErrMagicLinkInvalid) {
		t.Fatalf("replay = %v, want ErrMagicLinkInvalid", err)
	}
}

func TestMagicLinkExpiresAndIsThrottled(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	clock := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return clock })

	seed, err := service.SignInWithTelegram(ctx, signedWidget(700500, clock), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	link, err := service.IssueMagicLink(ctx, seed.Customer.ID, customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	parsed, _ := url.Parse(link)

	// The expiry is computed by the database, so it is aged there too rather than
	// by moving this process's clock. Both timestamps move: the table refuses an
	// expiry that precedes its own creation, which is the constraint doing its
	// job rather than an obstacle to work around.
	if _, err = harness.pool.Exec(ctx,
		`UPDATE customer_magic_links
		 SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 minute'
		 WHERE user_id = $1::uuid`,
		seed.Customer.ID,
	); err != nil {
		t.Fatalf("age the link: %v", err)
	}
	if _, err = service.CompleteMagicLink(
		ctx, parsed.Query().Get("token"), customerauthpg.RequestContext{},
	); !errors.Is(err, customerauth.ErrMagicLinkInvalid) {
		t.Fatalf("expired link = %v, want ErrMagicLinkInvalid", err)
	}

	// The per-customer limit exists to stop somebody's chat being flooded with
	// sign-in prompts, so it counts requests rather than redemptions.
	for range customerauth.MagicLinkRequestLimit {
		if _, err = service.IssueMagicLink(ctx, seed.Customer.ID, customerauthpg.RequestContext{}); err != nil &&
			!errors.Is(err, customerauth.ErrMagicLinkThrottled) {
			t.Fatalf("issue within the limit: %v", err)
		}
	}
	if _, err = service.IssueMagicLink(
		ctx, seed.Customer.ID, customerauthpg.RequestContext{},
	); !errors.Is(err, customerauth.ErrMagicLinkThrottled) {
		t.Fatalf("issue past the limit = %v, want ErrMagicLinkThrottled", err)
	}
}

func TestMagicLinkStaysOffWhenTheOperatorHasNotEnabledIt(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()

	enabled := newCustomerService(t, harness, func() time.Time { return now })
	seed, err := enabled.SignInWithTelegram(ctx, signedWidget(700600, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	disabled, err := customerauthpg.New(harness.pool, adminTestKey, customerauthpg.Options{
		TelegramBotToken: customerBotToken,
		PublicURL:        "https://vpn.test",
		Clock:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	if _, err = disabled.IssueMagicLink(
		ctx, seed.Customer.ID, customerauthpg.RequestContext{},
	); !errors.Is(err, customerauth.ErrMagicLinkUnavailable) {
		t.Fatalf("issue = %v, want ErrMagicLinkUnavailable", err)
	}
}

func TestUnlinkRefusesTheLastSignInMethod(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	result, err := service.SignInWithTelegram(ctx, signedWidget(700700, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	methods, err := service.ListSignInMethods(ctx, result.Customer.ID)
	if err != nil {
		t.Fatalf("list methods: %v", err)
	}
	if len(methods) != 1 || methods[0].Removable {
		t.Fatalf("methods = %+v, want exactly one non-removable entry", methods)
	}

	// Removing the only way back into an account holding a paid subscription is
	// unrecoverable from the panel, so it is refused rather than confirmed.
	if err = service.UnlinkIdentity(
		ctx, result.Customer.ID, methods[0].ID, customerauthpg.RequestContext{},
	); !errors.Is(err, customerauth.ErrLastSignInMethod) {
		t.Fatalf("unlink = %v, want ErrLastSignInMethod", err)
	}

	// With a second method linked, either may go.
	if _, err = harness.pool.Exec(ctx,
		`INSERT INTO identities (user_id, provider, provider_subject, verified_at, status)
		 VALUES ($1::uuid, 'oidc:google', 'external-subject', now(), 'active')`,
		result.Customer.ID,
	); err != nil {
		t.Fatalf("link a second method: %v", err)
	}
	if methods, err = service.ListSignInMethods(ctx, result.Customer.ID); err != nil {
		t.Fatalf("list methods: %v", err)
	}
	if len(methods) != 2 {
		t.Fatalf("methods = %d, want 2", len(methods))
	}
	for _, method := range methods {
		if !method.Removable {
			t.Fatalf("method %q is not removable with two linked", method.Label)
		}
	}
	if err = service.UnlinkIdentity(
		ctx, result.Customer.ID, methods[0].ID, customerauthpg.RequestContext{},
	); err != nil {
		t.Fatalf("unlink one of two: %v", err)
	}
}

func TestSecurityEventsRecordSignInsWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	result, err := service.SignInWithTelegram(ctx, signedWidget(700800, now), customerauthpg.RequestContext{
		UserAgent: "Mozilla/5.0 (integration)", RequestID: "req-1",
	})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}

	events, err := service.ListSecurityEvents(ctx, result.Customer.ID, nil, "", 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 || events[0].Event != "signed_in" {
		t.Fatalf("events = %+v, want one signed_in entry", events)
	}
	// The log is written to be shown to the customer, so it must never carry the
	// credential that produced it.
	for _, value := range events[0].Metadata {
		if text, ok := value.(string); ok && strings.Contains(text, result.Token) {
			t.Fatal("a security event carries the session token")
		}
	}
}

func TestRevokingSessionsForAProviderEndsOnlyThose(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	telegram, err := service.SignInWithTelegram(ctx, signedWidget(700900, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	// A session that an OIDC provider established, written directly because the
	// flow itself needs a live provider to exercise.
	if _, err = harness.pool.Exec(ctx,
		`INSERT INTO customer_sessions (
			user_id, token_hash, csrf_secret, auth_method, auth_provider,
			idle_expires_at, absolute_expires_at
		) VALUES ($1::uuid, digest('oidc-token', 'sha256'), digest('csrf', 'sha256'),
			'oidc', 'acme', now() + interval '1 day', now() + interval '7 days')`,
		telegram.Customer.ID,
	); err != nil {
		t.Fatalf("insert an OIDC session: %v", err)
	}

	revoked, err := service.RevokeSessionsForProvider(ctx, "acme")
	if err != nil {
		t.Fatalf("revoke for provider: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("revoked = %d, want 1", revoked)
	}
	// Disabling a provider must not sign out customers who never used it.
	if _, err = service.Resolve(ctx, telegram.Token); err != nil {
		t.Fatalf("the Telegram session was ended by an unrelated provider change: %v", err)
	}
}

// The route-level gate is covered in internal/httpapi; this asserts the pairing
// that matters end to end: a request carrying a real session cookie is admitted,
// and the same request without the CSRF token is refused on an unsafe method.
func TestAccountSurfaceAcceptsARealSessionAndEnforcesCSRF(t *testing.T) {
	ctx := context.Background()
	harness := newHarness(t)
	now := time.Now().UTC()
	service := newCustomerService(t, harness, func() time.Time { return now })

	result, err := service.SignInWithTelegram(ctx, signedWidget(701000, now), customerauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	handlers := newAccountHandlersForTest(service)
	router := mountAccount(handlers)

	read := httptest.NewRequest(http.MethodGet, "/v1/account/me", nil)
	read.AddCookie(&http.Cookie{Name: "__Host-omniflow_account", Value: result.Token})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, read)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /v1/account/me = %d, want 200", recorder.Code)
	}

	unsafe := httptest.NewRequest(http.MethodPost, "/v1/account/auth/logout", nil)
	unsafe.AddCookie(&http.Cookie{Name: "__Host-omniflow_account", Value: result.Token})
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, unsafe)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("POST without a CSRF token = %d, want 403", recorder.Code)
	}

	unsafe = httptest.NewRequest(http.MethodPost, "/v1/account/auth/logout", nil)
	unsafe.AddCookie(&http.Cookie{Name: "__Host-omniflow_account", Value: result.Token})
	unsafe.Header.Set("X-CSRF-Token", principal.CSRFToken)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, unsafe)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("POST with a CSRF token = %d, want 204", recorder.Code)
	}
}
