package customerauth

import (
	"errors"
	"testing"
)

func TestOIDCProviderNamespacesTheSubject(t *testing.T) {
	provider := OIDCProvider("google")
	if provider != "oidc:google" {
		t.Fatalf("provider = %q", provider)
	}
	slug, ok := OIDCSlug(provider)
	if !ok || slug != "google" {
		t.Fatalf("slug = %q, ok = %v", slug, ok)
	}
	// Telegram is not an OIDC provider, and mistaking it for one would put a
	// Telegram identity under a provider registry it does not belong to.
	if _, ok = OIDCSlug(ProviderTelegram); ok {
		t.Fatal("the Telegram provider parsed as an OIDC slug")
	}
	if _, ok = OIDCSlug("oidc:"); ok {
		t.Fatal("an empty slug was accepted")
	}
}

func TestCanUnlinkRefusesTheLastMethod(t *testing.T) {
	only := []SignInMethod{{IdentityID: "a", Provider: ProviderTelegram}}
	if err := CanUnlink(only, "a"); !errors.Is(err, ErrLastSignInMethod) {
		t.Fatalf("error = %v, want ErrLastSignInMethod", err)
	}

	two := []SignInMethod{
		{IdentityID: "a", Provider: ProviderTelegram},
		{IdentityID: "b", Provider: OIDCProvider("google")},
	}
	// Either may go while the other remains. The rule is about how many are left,
	// not about which one is being removed.
	if err := CanUnlink(two, "a"); err != nil {
		t.Fatalf("removing one of two was refused: %v", err)
	}
	if err := CanUnlink(two, "b"); err != nil {
		t.Fatalf("removing the other of two was refused: %v", err)
	}
}

func TestCanUnlinkReportsAnUnknownIdentity(t *testing.T) {
	methods := []SignInMethod{
		{IdentityID: "a", Provider: ProviderTelegram},
		{IdentityID: "b", Provider: OIDCProvider("google")},
	}
	if err := CanUnlink(methods, "missing"); err == nil {
		t.Fatal("unlinking an identity the customer does not hold was allowed")
	}
}

func TestResolveClaimsRequiresASubject(t *testing.T) {
	if _, err := ResolveClaims(Claims{Subject: "   "}, false); !errors.Is(err, ErrMissingSubject) {
		t.Fatalf("error = %v, want ErrMissingSubject", err)
	}
}

func TestResolveClaimsEnforcesTheVerifiedEmailRequirement(t *testing.T) {
	unverified := Claims{Subject: "sub-1", Email: "Person@Example.test"}

	if _, err := ResolveClaims(unverified, true); !errors.Is(err, ErrUnverifiedEmail) {
		t.Fatalf("error = %v, want ErrUnverifiedEmail", err)
	}
	// A provider that never asserts the claim is a real configuration, so the
	// requirement is a per-provider switch rather than a fixed rule.
	resolved, err := ResolveClaims(unverified, false)
	if err != nil {
		t.Fatalf("ResolveClaims: %v", err)
	}
	if resolved.Email != "person@example.test" {
		t.Fatalf("email = %q, want it folded to lower case", resolved.Email)
	}
}

func TestResolveClaimsRejectsAVerifiedFlagWithNoAddress(t *testing.T) {
	if _, err := ResolveClaims(
		Claims{Subject: "sub-1", EmailVerified: true}, true,
	); !errors.Is(err, ErrUnverifiedEmail) {
		t.Fatalf("error = %v, want ErrUnverifiedEmail", err)
	}
}

func TestProviderPresetsAreOrdinaryConfiguration(t *testing.T) {
	presets := ProviderPresets()
	if len(presets) == 0 {
		t.Fatal("no presets are shipped")
	}
	seen := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		if _, duplicate := seen[preset.Slug]; duplicate {
			t.Fatalf("duplicate preset slug %q", preset.Slug)
		}
		seen[preset.Slug] = struct{}{}

		// Every preset must be a complete, ordinary OIDC entry: discovery over
		// https, an issuer, and the openid scope. Anything less would need a
		// provider-specific code path somewhere, which is what presets exist to
		// avoid.
		if preset.Issuer == "" || preset.DiscoveryURL == "" {
			t.Fatalf("preset %q is missing its issuer or discovery URL", preset.Slug)
		}
		if got := preset.DiscoveryURL[:8]; got != "https://" {
			t.Fatalf("preset %q discovery URL is not https", preset.Slug)
		}
		openid := false
		for _, scope := range preset.Scopes {
			if scope == "openid" {
				openid = true
			}
		}
		if !openid {
			t.Fatalf("preset %q does not request the openid scope", preset.Slug)
		}
		// A preset that defaults the verified-email requirement on for a provider
		// that does not send the claim would refuse every sign-in through it, so
		// any preset turning it off has to say why.
		if !preset.RequireVerifiedEmail && preset.Note == "" {
			t.Fatalf("preset %q relaxes the verified-email requirement without explaining why", preset.Slug)
		}
	}
}
