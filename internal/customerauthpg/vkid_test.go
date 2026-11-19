package customerauthpg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omniflow/omniflow/internal/database/dbgen"
)

func TestVKIDExchange(t *testing.T) {
	var tokenForm, infoForm map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		form := map[string]string{}
		for key := range request.PostForm {
			form[key] = request.PostForm.Get(key)
		}
		switch request.URL.Path {
		case "/token":
			tokenForm = form
			if form["device_id"] == "" {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"at","user_id":12345}`))
		case "/info":
			infoForm = form
			_, _ = writer.Write([]byte(`{"user":{"user_id":"12345","first_name":"Иван","last_name":"Петров","email":"ivan@example.com"}}`))
		}
	}))
	t.Cleanup(server.Close)
	saved := vkidEndpoints
	vkidEndpoints.token, vkidEndpoints.userInfo = server.URL+"/token", server.URL+"/info"
	t.Cleanup(func() { vkidEndpoints = saved })

	service := &Service{httpClient: server.Client()}
	stored := dbgen.CustomerOidcProvider{Slug: "vk", Issuer: VKIDIssuer, ClientID: "app-1"}

	ctx := WithOIDCCallback(context.Background(), OIDCCallback{DeviceID: "dev-1", State: "st"})
	claims, err := service.exchangeVKID(ctx, stored, "code-1", "verifier-1", "https://vpn.test/cb")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if claims.Subject != "12345" || claims.Issuer != VKIDIssuer || claims.Email != "ivan@example.com" || claims.Name != "Иван Петров" {
		t.Fatalf("claims = %+v", claims)
	}
	if tokenForm["code_verifier"] != "verifier-1" || tokenForm["device_id"] != "dev-1" || tokenForm["state"] != "st" ||
		tokenForm["client_id"] != "app-1" || tokenForm["grant_type"] != "authorization_code" {
		t.Fatalf("token request = %v", tokenForm)
	}
	if infoForm["access_token"] != "at" || infoForm["client_id"] != "app-1" {
		t.Fatalf("user_info request = %v", infoForm)
	}

	if _, err := service.exchangeVKID(context.Background(), stored, "code-1", "v", "https://vpn.test/cb"); !errors.Is(err, ErrSignInRejected) {
		t.Fatalf("missing device_id: %v, want a rejection", err)
	}
	if !IsVKID("https://id.vk.ru/") || IsVKID("https://accounts.google.com") {
		t.Fatal("IsVKID misclassifies issuers")
	}
}
