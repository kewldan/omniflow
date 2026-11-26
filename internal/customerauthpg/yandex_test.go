package customerauthpg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

func TestYandexExchange(t *testing.T) {
	var tokenForm url.Values
	var authorization string
	profileClient := "app-1"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			_ = request.ParseForm()
			tokenForm = request.PostForm
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"access_token":"at","token_type":"bearer","expires_in":3600}`))
		case "/info":
			authorization = request.Header.Get("Authorization")
			_, _ = writer.Write([]byte(`{"id":"1000034426","login":"ivan","client_id":"` + profileClient +
				`","display_name":"ivan","real_name":"Иван Петров","default_email":"ivan@yandex.ru"}`))
		}
	}))
	t.Cleanup(server.Close)
	saved := yandexEndpoints
	yandexEndpoints.token, yandexEndpoints.userInfo = server.URL+"/token", server.URL+"/info?format=json"
	t.Cleanup(func() { yandexEndpoints = saved })

	service := &Service{httpClient: server.Client()}
	stored := dbgen.CustomerOidcProvider{Slug: "yandex", Issuer: YandexIssuer, ClientID: "app-1"}
	config := yandexConfig(stored, "secret-1", "https://vpn.test/cb")

	claims, err := service.exchangeYandex(context.Background(), config, stored, "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if claims.Subject != "1000034426" || claims.Issuer != YandexIssuer || claims.Email != "ivan@yandex.ru" ||
		claims.Name != "Иван Петров" {
		t.Fatalf("claims = %+v", claims)
	}
	if tokenForm.Get("code_verifier") != "verifier-1" || tokenForm.Get("code") != "code-1" ||
		tokenForm.Get("client_id") != "app-1" || tokenForm.Get("client_secret") != "secret-1" ||
		tokenForm.Get("grant_type") != "authorization_code" {
		t.Fatalf("token request = %v", tokenForm)
	}
	if authorization != "OAuth at" {
		t.Fatalf("user info Authorization = %q", authorization)
	}

	// A token the profile says was issued to another application is refused.
	profileClient = "someone-else"
	if _, err := service.exchangeYandex(context.Background(), config, stored, "code-1", "v"); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("foreign client_id: %v, want a rejection", err)
	}
	if _, err := service.exchangeYandex(context.Background(), config, stored, "", "v"); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("empty code: %v, want a rejection", err)
	}
	if !IsYandex("https://oauth.yandex.ru/") || IsYandex("https://id.vk.ru") {
		t.Fatal("IsYandex misclassifies issuers")
	}
}
