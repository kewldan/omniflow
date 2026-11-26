package customerauthpg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/omniflow/omniflow/internal/customerauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"golang.org/x/oauth2"
)

// Yandex ID is plain OAuth 2.0, not OpenID Connect: oauth.yandex.ru publishes
// no discovery document (the well-known path answers an HTML error page) and
// the token response carries no ID token. A provider whose issuer is
// YandexIssuer is therefore driven by fixed endpoints, and the profile comes
// from login.yandex.ru/info. PKCE, state, the sealed flow cookie, and identity
// linking stay the shared flow; Yandex accepts the S256 code challenge.
const (
	YandexIssuer       = "https://oauth.yandex.ru"
	yandexAuthorizeURL = "https://oauth.yandex.ru/authorize"
	yandexTokenURL     = "https://oauth.yandex.ru/token"
	yandexUserInfoURL  = "https://login.yandex.ru/info?format=json"
)

// IsYandex reports whether a stored provider is Yandex ID.
func IsYandex(issuer string) bool {
	return strings.TrimRight(strings.TrimSpace(issuer), "/") == YandexIssuer
}

// yandexEndpoints lets tests point the flow at a stand-in.
var yandexEndpoints = struct{ authorize, token, userInfo string }{
	yandexAuthorizeURL, yandexTokenURL, yandexUserInfoURL,
}

func yandexConfig(stored dbgen.CustomerOidcProvider, secret, redirectURL string) *oauth2.Config {
	scopes := stored.Scopes
	if len(scopes) == 0 {
		scopes = []string{"login:email", "login:info"}
	}
	return &oauth2.Config{
		ClientID: stored.ClientID, ClientSecret: secret, RedirectURL: redirectURL, Scopes: scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL: yandexEndpoints.authorize, TokenURL: yandexEndpoints.token,
			// Stated rather than auto-detected: detection retries a refused
			// exchange with the other style, and a code is single-use.
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// exchangeYandex redeems the code and reads the profile. A refusal at either
// step is ErrSignInRejected: Yandex's error bodies are not shown to the
// customer.
func (service *Service) exchangeYandex(
	ctx context.Context, config *oauth2.Config, stored dbgen.CustomerOidcProvider, code, verifier string,
) (customerauth.Claims, error) {
	if code == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}
	token, err := config.Exchange(
		context.WithValue(ctx, oauth2.HTTPClient, service.httpClient), code,
		oauth2.SetAuthURLParam("code_verifier", verifier),
	)
	if err != nil || token.AccessToken == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, yandexEndpoints.userInfo, nil)
	if err != nil {
		return customerauth.Claims{}, err
	}
	request.Header.Set("Authorization", "OAuth "+token.AccessToken)
	response, err := service.httpClient.Do(request)
	if err != nil {
		return customerauth.Claims{}, ErrSignInRejected
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		return customerauth.Claims{}, ErrSignInRejected
	}
	var profile struct {
		ID           json.Number `json:"id"`
		ClientID     string      `json:"client_id"`
		DisplayName  string      `json:"display_name"`
		RealName     string      `json:"real_name"`
		DefaultEmail string      `json:"default_email"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.UseNumber()
	if err := decoder.Decode(&profile); err != nil {
		return customerauth.Claims{}, ErrSignInRejected
	}
	// The profile names the application the token was issued to; a token for
	// any other client is not this sign-in.
	if profile.ClientID != stored.ClientID {
		return customerauth.Claims{}, ErrSignInRejected
	}
	subject := profile.ID.String()
	if _, err := strconv.ParseUint(subject, 10, 64); err != nil || subject == "" {
		return customerauth.Claims{}, ErrSignInRejected
	}
	name := strings.TrimSpace(profile.RealName)
	if name == "" {
		name = strings.TrimSpace(profile.DisplayName)
	}
	// Yandex returns the account's default address but asserts no
	// `email_verified`; a provider set to require one refuses.
	return customerauth.ResolveClaims(customerauth.Claims{
		Issuer: YandexIssuer, Subject: subject, Email: profile.DefaultEmail, Name: name, Locale: "ru",
	}, stored.RequireVerifiedEmail)
}
