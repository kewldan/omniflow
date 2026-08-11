//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/adminauthpg"
	"github.com/omniflow/omniflow/internal/rbac"
)

// adminTestKey is a fixed 32-byte key. Test secrets are deterministic so a
// failure is reproducible; production reads APP_DATA_ENCRYPTION_KEY.
var adminTestKey = []byte("0123456789abcdef0123456789abcdef")

// cheapPasswordParams keep argon2 fast. The parameters under test here are the
// storage and lifecycle rules, not the hash cost, which password_test.go covers.
var cheapPasswordParams = adminauth.PasswordParams{
	Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
}

func newAdminService(t *testing.T, harness *harness, now func() time.Time) *adminauthpg.Service {
	t.Helper()
	service, err := adminauthpg.New(harness.pool, adminTestKey, adminauthpg.Options{
		PasswordParams: cheapPasswordParams,
		Clock:          now,
	})
	if err != nil {
		t.Fatalf("build admin service: %v", err)
	}
	return service
}

// bootstrapOwner runs the real first-owner flow and returns the owner account.
func bootstrapOwner(t *testing.T, ctx context.Context, service *adminauthpg.Service) adminauthpg.Account {
	t.Helper()
	token, err := service.IssueSetupToken(ctx)
	if err != nil {
		t.Fatalf("issue setup token: %v", err)
	}
	account, err := service.CompleteBootstrap(
		ctx, token, "Owner@example.com", "Owner", "correct horse battery staple", "en",
		adminauthpg.RequestContext{RequestID: "bootstrap"},
	)
	if err != nil {
		t.Fatalf("complete bootstrap: %v", err)
	}
	return account
}

func TestAdminBootstrapCreatesExactlyOneOwner(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)

	state, err := service.BootstrapStatus(ctx)
	if err != nil || !state.Required {
		t.Fatalf("a bare database should require setup: %v %v", state, err)
	}

	owner := bootstrapOwner(t, ctx, service)
	if len(owner.Roles) != 1 || owner.Roles[0] != rbac.RoleOwner {
		t.Fatalf("first account holds %v, want [owner]", owner.Roles)
	}
	// The address is stored normalized for lookup but displayed as entered.
	if owner.Email != "Owner@example.com" {
		t.Fatalf("display email = %q", owner.Email)
	}

	state, err = service.BootstrapStatus(ctx)
	if err != nil || state.Required {
		t.Fatalf("setup should be closed after the first owner: %v %v", state, err)
	}

	// A second token cannot be issued, and a replayed bootstrap is refused, so
	// the path cannot be used twice to mint a second owner.
	if _, err = service.IssueSetupToken(ctx); !errors.Is(err, adminauthpg.ErrBootstrapClosed) {
		t.Fatalf("expected ErrBootstrapClosed issuing a second token, got %v", err)
	}
	if _, err = service.CompleteBootstrap(
		ctx, "any-token", "second@example.com", "Second", "correct horse battery staple", "en",
		adminauthpg.RequestContext{},
	); !errors.Is(err, adminauthpg.ErrBootstrapClosed) {
		t.Fatalf("expected ErrBootstrapClosed, got %v", err)
	}
}

func TestAdminSetupTokenIsSingleUse(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)

	token, err := service.IssueSetupToken(ctx)
	if err != nil {
		t.Fatalf("issue setup token: %v", err)
	}
	// Issuing again retires the first token rather than leaving two usable.
	if _, err = service.IssueSetupToken(ctx); err != nil {
		t.Fatalf("reissue setup token: %v", err)
	}
	if _, err = service.CompleteBootstrap(
		ctx, token, "owner@example.com", "Owner", "correct horse battery staple", "en",
		adminauthpg.RequestContext{},
	); !errors.Is(err, adminauthpg.ErrBootstrapClosed) {
		t.Fatalf("a retired token was accepted: %v", err)
	}
}

func TestAdminSignInAndSessionLifecycle(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	address := netip.MustParseAddr("198.51.100.10")
	result, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{IP: &address, UserAgent: "test", RequestID: "login"})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if result.ChallengeRequired {
		t.Fatal("a fresh account has no second factor to challenge")
	}

	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !principal.Grant.Allows(rbac.PermissionAdminsWrite) {
		t.Fatal("the owner session lost admins.write")
	}

	// Case and surrounding whitespace in the address must not matter.
	if _, err = service.Authenticate(ctx, "  OWNER@EXAMPLE.COM ", "correct horse battery staple",
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("normalized sign-in failed: %v", err)
	}

	sessions, err := service.ListSessions(ctx, principal.Account.ID, principal.SessionID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) < 2 {
		t.Fatalf("expected both sessions to be live, got %d", len(sessions))
	}

	// Logging out invalidates only that token.
	if err = service.Logout(ctx, result.Token, adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err = service.Resolve(ctx, result.Token); !errors.Is(err, adminauthpg.ErrSessionInvalid) {
		t.Fatalf("a revoked session still resolves: %v", err)
	}
}

func TestAdminUnknownAddressAndWrongPasswordAreIndistinguishable(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	_, unknownErr := service.Authenticate(ctx, "nobody@example.com", "whatever at all",
		adminauthpg.RequestContext{})
	_, wrongErr := service.Authenticate(ctx, "owner@example.com", "not the password",
		adminauthpg.RequestContext{})

	if !errors.Is(unknownErr, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("unknown address returned %v", unknownErr)
	}
	if !errors.Is(wrongErr, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("wrong password returned %v", wrongErr)
	}
}

// Repeated failures lock the account, and the lockout is enforced even when the
// correct password is finally supplied.
func TestAdminLockoutAfterRepeatedFailures(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	for attempt := 0; attempt < adminauth.DefaultLockoutPolicy.Threshold; attempt++ {
		if _, err := service.Authenticate(ctx, "owner@example.com", "wrong password here",
			adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
			t.Fatalf("attempt %d returned %v", attempt, err)
		}
	}

	if _, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrAccountLocked) {
		t.Fatalf("expected ErrAccountLocked once the threshold is crossed, got %v", err)
	}
}

func TestAdminTOTPEnrolmentAndRecoveryCodes(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	// Frozen so the expected TOTP code is derived from the same step the service
	// uses, but frozen at the real current time rather than an arbitrary past
	// one: session rows carry deadlines from this clock and a `created_at` from
	// the database's, and the two have to agree about roughly when "now" is.
	now := time.Now().UTC()
	service := newAdminService(t, harness, func() time.Time { return now })
	owner := bootstrapOwner(t, ctx, service)

	enrolment, err := service.BeginTOTPEnrolment(ctx, owner.ID, "Omniflow", adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("begin enrolment: %v", err)
	}

	// An unconfirmed secret must not yet gate sign-in.
	result, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate during enrolment: %v", err)
	}
	if result.ChallengeRequired {
		t.Fatal("an unconfirmed enrolment gated sign-in")
	}

	code, err := adminauth.TOTPCode(enrolment.Secret, uint64(now.Unix()/30))
	if err != nil {
		t.Fatalf("derive code: %v", err)
	}
	codes, err := service.ConfirmTOTPEnrolment(ctx, owner.ID, code, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("confirm enrolment: %v", err)
	}
	if len(codes) != adminauth.RecoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), adminauth.RecoveryCodeCount)
	}

	// Now sign-in requires the second factor.
	result, err = service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate after enrolment: %v", err)
	}
	if !result.ChallengeRequired {
		t.Fatal("a confirmed enrolment did not gate sign-in")
	}
	// A pending session authorizes nothing until the challenge passes.
	if _, err = service.Resolve(ctx, result.Token); !errors.Is(err, adminauthpg.ErrChallengeRequired) {
		t.Fatalf("a pending session resolved: %v", err)
	}

	// A recovery code satisfies the challenge exactly once.
	completed, err := service.VerifyChallenge(ctx, result.Token, codes[0], adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("verify with recovery code: %v", err)
	}
	if _, err = service.Resolve(ctx, completed.Token); err != nil {
		t.Fatalf("completed session does not resolve: %v", err)
	}

	next, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate again: %v", err)
	}
	if _, err = service.VerifyChallenge(ctx, next.Token, codes[0], adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("a recovery code was accepted twice: %v", err)
	}
}

// The installation must never be left without an owner, whether by revoking the
// role or by suspending the last account that holds it.
func TestAdminLastOwnerIsProtected(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	result, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	principal, err := service.Resolve(ctx, result.Token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if _, err = service.SetRoles(ctx, owner.ID, []rbac.Role{rbac.RoleSupport}, principal,
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrLastOwner) {
		t.Fatalf("demoting the last owner returned %v", err)
	}
	if _, err = service.SetStatus(ctx, owner.ID, "suspended", owner.ID,
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrLastOwner) {
		t.Fatalf("suspending the last owner returned %v", err)
	}

	// With a second owner present, demoting the first is allowed.
	second, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		Email: "second@example.com", DisplayName: "Second",
		Password: "another good passphrase", Roles: []rbac.Role{rbac.RoleOwner},
	}, owner.ID, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("create second owner: %v", err)
	}
	if _, err = service.SetRoles(ctx, owner.ID, []rbac.Role{rbac.RoleSupport}, principal,
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("demoting an owner with a peer failed: %v", err)
	}
	if second.ID == "" {
		t.Fatal("second owner has no identifier")
	}
}

// Only an owner may grant the owner role, which is the escalation path that
// matters most in the panel.
func TestAdminOnlyOwnerGrantsOwner(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	administrator, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		Email: "admin@example.com", DisplayName: "Admin",
		Password: "another good passphrase", Roles: []rbac.Role{rbac.RoleAdministrator},
	}, owner.ID, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("create administrator: %v", err)
	}

	actingAdministrator := adminauthpg.Principal{
		Account: administrator,
		Grant:   rbac.NewGrant(rbac.RoleAdministrator),
	}
	if _, err = service.SetRoles(ctx, administrator.ID, []rbac.Role{rbac.RoleOwner}, actingAdministrator,
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrForbidden) {
		t.Fatalf("an administrator promoted itself to owner: %v", err)
	}
}

// Suspending an operator must end their live sessions immediately rather than
// leaving them usable until they expire.
func TestAdminSuspensionRevokesLiveSessions(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	support, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		Email: "support@example.com", DisplayName: "Support",
		Password: "another good passphrase", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner.ID, adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("create support: %v", err)
	}

	session, err := service.Authenticate(ctx, "support@example.com", "another good passphrase",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("authenticate support: %v", err)
	}
	if _, err = service.Resolve(ctx, session.Token); err != nil {
		t.Fatalf("support session does not resolve: %v", err)
	}

	if _, err = service.SetStatus(ctx, support.ID, "suspended", owner.ID,
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err = service.Resolve(ctx, session.Token); err == nil {
		t.Fatal("a suspended operator's session still resolves")
	}
}

// Changing a password ends every other session but spares the one that made the
// change, so the operator is not signed out of the browser they are securing.
func TestAdminPasswordChangeRevokesOtherSessions(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	first, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	second, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{})
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
	keeper, err := service.Resolve(ctx, second.Token)
	if err != nil {
		t.Fatalf("resolve second: %v", err)
	}

	if err = service.ChangePassword(ctx, keeper.Account.ID,
		"correct horse battery staple", "a brand new passphrase", keeper.SessionID,
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("change password: %v", err)
	}

	if _, err = service.Resolve(ctx, first.Token); !errors.Is(err, adminauthpg.ErrSessionInvalid) {
		t.Fatalf("the other session survived a password change: %v", err)
	}
	if _, err = service.Resolve(ctx, second.Token); err != nil {
		t.Fatalf("the changing session was signed out: %v", err)
	}
	if _, err = service.Authenticate(ctx, "owner@example.com", "a brand new passphrase",
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("the new password does not work: %v", err)
	}
}

func TestAdminPasswordResetIsSingleUseAndSilentForUnknownAddresses(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	// An unknown address produces no token and no error, so the response cannot
	// disclose whether the account exists.
	token, _, err := service.RequestPasswordReset(ctx, "nobody@example.com", adminauthpg.RequestContext{})
	if err != nil || token != "" {
		t.Fatalf("unknown address produced token=%q err=%v", token, err)
	}

	token, _, err = service.RequestPasswordReset(ctx, "owner@example.com", adminauthpg.RequestContext{})
	if err != nil || token == "" {
		t.Fatalf("known address produced token=%q err=%v", token, err)
	}

	if err = service.CompletePasswordReset(ctx, token, "yet another passphrase",
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("complete reset: %v", err)
	}
	// A replayed link must not work a second time.
	if err = service.CompletePasswordReset(ctx, token, "a different passphrase",
		adminauthpg.RequestContext{}); !errors.Is(err, adminauthpg.ErrInvalidCredentials) {
		t.Fatalf("a reset token was accepted twice: %v", err)
	}
}

// The audit trail must record what happened, with the classification the panel
// filters on.
func TestAdminAuditTrailRecordsAuthenticationAndAuthorization(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	if _, err := service.Authenticate(ctx, "owner@example.com", "wrong password here",
		adminauthpg.RequestContext{}); err == nil {
		t.Fatal("a wrong password succeeded")
	}
	if _, err := service.Authenticate(ctx, "owner@example.com", "correct horse battery staple",
		adminauthpg.RequestContext{}); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	page, err := service.SearchAudit(ctx, adminauthpg.AuditFilter{Category: "authentication"})
	if err != nil {
		t.Fatalf("search audit: %v", err)
	}
	var sawSuccess, sawFailure bool
	for _, event := range page.Events {
		if event.Action != "admin.login" {
			continue
		}
		switch event.Outcome {
		case "success":
			sawSuccess = true
		case "failure":
			sawFailure = true
		}
	}
	if !sawSuccess || !sawFailure {
		t.Fatalf("trail is missing outcomes: success=%v failure=%v", sawSuccess, sawFailure)
	}

	// Filtering is exact, so an unrelated category returns nothing.
	empty, err := service.SearchAudit(ctx, adminauthpg.AuditFilter{Category: "marketing"})
	if err != nil {
		t.Fatalf("search audit: %v", err)
	}
	if len(empty.Events) != 0 {
		t.Fatalf("marketing filter returned %d events", len(empty.Events))
	}

	// Both directions must work, and they must disagree about which event comes
	// first — that is what proves the ordering is actually applied.
	descending, err := service.SearchAudit(ctx, adminauthpg.AuditFilter{})
	if err != nil {
		t.Fatalf("search descending: %v", err)
	}
	ascending, err := service.SearchAudit(ctx, adminauthpg.AuditFilter{Ascending: true})
	if err != nil {
		t.Fatalf("search ascending: %v", err)
	}
	if len(descending.Events) < 2 || len(ascending.Events) < 2 {
		t.Fatalf("not enough events to compare ordering")
	}
	if descending.Events[0].ID == ascending.Events[0].ID {
		t.Fatal("both sort directions returned the same first event")
	}
	if owner.ID == "" {
		t.Fatal("owner has no identifier")
	}
}

// Keyset pagination must not repeat or skip a row at a page boundary.
func TestAdminAuditPaginationIsStable(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	bootstrapOwner(t, ctx, service)

	for attempt := 0; attempt < 12; attempt++ {
		if err := service.AppendAudit(ctx, adminauthpg.AuditEntry{
			ActorType: "system", Action: "test.event", Category: "system",
			TargetType: "test", TargetID: "t",
		}); err != nil {
			t.Fatalf("append audit: %v", err)
		}
	}

	seen := map[string]bool{}
	filter := adminauthpg.AuditFilter{Action: "test.event", PageSize: 5}
	for page := 0; page < 10; page++ {
		result, err := service.SearchAudit(ctx, filter)
		if err != nil {
			t.Fatalf("search page %d: %v", page, err)
		}
		for _, event := range result.Events {
			if seen[event.ID] {
				t.Fatalf("event %s appeared on two pages", event.ID)
			}
			seen[event.ID] = true
		}
		if result.NextCursor == "" {
			break
		}
		filter.Cursor = result.NextCursor
	}
	if len(seen) != 12 {
		t.Fatalf("paged over %d events, want 12", len(seen))
	}
}

func TestAdminPreferencesRoundTripAndReject(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	saved, err := service.SavePreferences(ctx, owner.ID, adminauthpg.Preferences{
		PageSize: 50, Density: "compact", AuditSort: "asc",
	})
	if err != nil {
		t.Fatalf("save preferences: %v", err)
	}
	if saved.PageSize != 50 || saved.Density != "compact" || saved.AuditSort != "asc" {
		t.Fatalf("preferences did not round trip: %+v", saved)
	}

	// Out-of-range values are dropped rather than stored, and a merge must not
	// wipe the keys the patch does not mention.
	merged, err := service.SavePreferences(ctx, owner.ID, adminauthpg.Preferences{
		PageSize: 9999, Density: "spacious",
	})
	if err != nil {
		t.Fatalf("save preferences: %v", err)
	}
	if merged.PageSize != 50 {
		t.Fatalf("an out-of-range page size overwrote a good one: %+v", merged)
	}
	if merged.Density != "compact" {
		t.Fatalf("an invalid density overwrote a good one: %+v", merged)
	}

	account, err := service.GetAccount(ctx, owner.ID)
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if account.Preferences.AuditSort != "asc" {
		t.Fatalf("preferences are not returned with the account: %+v", account.Preferences)
	}
}

// A duplicate address must be refused rather than creating a second account
// that could shadow the first at sign-in.
func TestAdminDuplicateEmailIsRejected(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	service := newAdminService(t, harness, time.Now)
	owner := bootstrapOwner(t, ctx, service)

	_, err := service.CreateAccount(ctx, adminauthpg.CreateAccountInput{
		Email: strings.ToUpper("owner@example.com"), DisplayName: "Impostor",
		Password: "another good passphrase", Roles: []rbac.Role{rbac.RoleSupport},
	}, owner.ID, adminauthpg.RequestContext{})
	if !errors.Is(err, adminauthpg.ErrConflict) {
		t.Fatalf("expected ErrConflict for a duplicate address, got %v", err)
	}
}
