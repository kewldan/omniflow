package customerauthpg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"golang.org/x/oauth2"
)

// VK ID is OAuth 2.1, not OpenID Connect: it publishes no discovery document
// and issues no ID token Omniflow could verify, the code exchange needs the
// `device_id` VK appends to the callback, and the profile comes from a POST to
// user_info. A provider whose issuer is VKIDIssuer is therefore driven by the
// functions below instead of discovery. Everything else — PKCE, state, the
// sealed flow cookie, identity linking, auto-provisioning — is the shared flow.
const (
	VKIDIssuer       = "https://id.vk.ru"
	vkidAuthorizeURL = "https://id.vk.ru/authorize"
	vkidTokenURL     = "https://id.vk.ru/oauth2/auth"
	vkidUserInfoURL  = "https://id.vk.ru/oauth2/user_info"
)

// IsVKID reports whether a stored provider is VK ID.
func IsVKID(issuer string) bool {
	return strings.TrimRight(strings.TrimSpace(issuer), "/") == VKIDIssuer
}

type oidcCallbackKey struct{}

// OIDCCallback carries provider-specific callback parameters: VK ID's
// `device_id`, and the `state` its token endpoint wants echoed back.
type OIDCCallback struct {
	DeviceID string
	State    string
}

// WithOIDCCallback attaches callback parameters to a completion request.
func WithOIDCCallback(ctx context.Context, callback OIDCCallback) context.Context {
	return context.WithValue(ctx, oidcCallbackKey{}, callback)
}

func oidcCallbackFrom(ctx context.Context) OIDCCallback {
	callback, _ := ctx.Value(oidcCallbackKey{}).(OIDCCallback)
	return callback
}

// vkidEndpoints lets tests point the flow at a stand-in.
var vkidEndpoints = struct{ authorize, token, userInfo string }{vkidAuthorizeURL, vkidTokenURL, vkidUserInfoURL}

func vkidConfig(stored dbgen.CustomerOidcProvider, secret, redirectURL string) *oauth2.Config {
	scopes := stored.Scopes
	if len(scopes) == 0 {
		scopes = []string{"vkid.personal_info", "email"}
	}
	return &oauth2.Config{
		ClientID: stored.ClientID, ClientSecret: secret, RedirectURL: redirectURL, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: vkidEndpoints.authorize, TokenURL: vkidEndpoints.token},
	}
}

// exchangeVKID redeems the code and reads the profile. A refusal at either
// step is ErrSignInRejected: VK's error bodies are not shown to the customer.
func (service *Service) exchangeVKID(
	ctx context.Context, stored dbgen.CustomerOidcProvider, code, verifier, redirectURL string,
) (customerauth.Claims, error) {
	callback := oidcCallbackFrom(ctx)
	if callback.DeviceID == "" || code == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}
	var token struct {
		AccessToken string `json:"access_token"`
		UserID      any    `json:"user_id"`
	}
	if err := service.vkidPost(ctx, vkidEndpoints.token, url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURL},
		"client_id": {stored.ClientID}, "device_id": {callback.DeviceID}, "code_verifier": {verifier},
		"state": {callback.State},
	}, &token); err != nil || token.AccessToken == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}
	var profile struct {
		User struct {
			UserID    json.Number `json:"user_id"`
			FirstName string      `json:"first_name"`
			LastName  string      `json:"last_name"`
			Email     string      `json:"email"`
		} `json:"user"`
	}
	if err := service.vkidPost(ctx, vkidEndpoints.userInfo, url.Values{
		"access_token": {token.AccessToken}, "client_id": {stored.ClientID},
	}, &profile); err != nil {
		return customerauth.Claims{}, ErrSignInRejected
	}
	subject := profile.User.UserID.String()
	if _, err := strconv.ParseInt(subject, 10, 64); err != nil || subject == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}
	// VK hands out an address only when the user has a confirmed one, but it
	// asserts no `email_verified` claim; a provider set to require one refuses.
	return customerauth.ResolveClaims(customerauth.Claims{
		Issuer: VKIDIssuer, Subject: subject, Email: profile.User.Email,
		Name: strings.TrimSpace(profile.User.FirstName + " " + profile.User.LastName), Locale: "ru",
	}, stored.RequireVerifiedEmail)
}

func (service *Service) vkidPost(ctx context.Context, endpoint string, form url.Values, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := service.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return ErrSignInRejected
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	return decoder.Decode(target)
}
