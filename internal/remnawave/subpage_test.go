package remnawave

import (
	"context"
	"net/http"
	"testing"
)

const samplePage = `{"response":{"uuid":"0b2e8c1d-0000-4000-8000-000000000001","name":"Default","config":{
 "platforms":{
  "ios":{"displayName":{"en":"iPhone","ru":"Айфон"},"apps":[
    {"name":"Streisand","urlScheme":"streisand://import/","blocks":[]},
    {"name":"Happ","isFeatured":true,"blocks":[
      {"title":{"en":"Install","ru":"Установите"},"description":{"en":"From the App Store","ru":"Из App Store"},
       "buttons":[{"type":"externalLink","link":"https://apps.apple.com/app/happ","text":{"en":"Get"}}]},
      {"title":{"en":"Add"},"buttons":[{"type":"subscriptionLink","link":"happ://add/{{SUBSCRIPTION_LINK}}"}]}
    ]}
  ]},
  "linux":{"displayName":"Linux","apps":[]}
 }}}}`

func TestSubscriptionPageConfigIsRead(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/subscription-page-configs/0b2e8c1d-0000-4000-8000-000000000001" {
			t.Errorf("path = %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte(samplePage))
	})
	page, err := client.SubscriptionPageConfig(context.Background(), "0b2e8c1d-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if page.Name != "Default" || len(page.Platforms) != 1 {
		t.Fatalf("a platform with no apps is dropped: %+v", page)
	}
	ios := page.Platforms[0]
	if ios.Key != "ios" || ios.Label.RU != "Айфон" || len(ios.Apps) != 2 {
		t.Fatalf("ios = %+v", ios)
	}
	happ := ios.Apps[1]
	if !happ.Featured || happ.URLScheme != "happ://add/" || happ.DownloadURL != "https://apps.apple.com/app/happ" {
		t.Fatalf("happ = %+v", happ)
	}
	if happ.Steps.EN != "Install\nFrom the App Store\n\nAdd" || happ.Steps.RU != "Установите\nИз App Store\n\nAdd" {
		t.Fatalf("steps = %+v", happ.Steps)
	}
}

func TestSubscriptionPageStoredAsAString(t *testing.T) {
	t.Parallel()
	page, err := parseSubscriptionPage([]byte(`"{\"platforms\":{\"android\":[{\"name\":\"v2rayNG\",\"urlScheme\":\"v2rayng://install-config?url=\"}]}}"`))
	if err != nil || len(page.Platforms) != 1 || page.Platforms[0].Apps[0].Name != "v2rayNG" {
		t.Fatalf("page = %+v (%v)", page, err)
	}
}
