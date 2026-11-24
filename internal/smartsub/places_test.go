package smartsub

import (
	"testing"
	"time"
)

func TestGeocheckPlaceWhenDatabasesSplit(t *testing.T) {
	// Half the databases say Britain, half the United States, and the
	// reputation database names a British town; every CDN edge is American.
	split := `{"reputation": {"city": "Bolton", "country_code": "GB"},
	 "consensus": {"ipv4": [{"code": "GB", "percent": 46.67}, {"code": "US", "percent": 46.67}, {"code": "EE", "percent": 6.67}]},
	 "geo": {"cdn": [{"id": "cdn_cloudflare", "ipv4": {"value": "US"}}, {"id": "cdn_youtube", "ipv4": {"value": "US"}},
	  {"id": "cdn_netflix", "ipv4": {"value": "US"}}]}}`
	check, err := SummarizeGeocheck([]byte(split), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if check.Place == nil || check.Place.Country != "US" || check.Place.City != "" {
		t.Errorf("place = %+v, want the United States' usual city", check.Place)
	}
	// Edges that disagree settle nothing, and the named city stands.
	mixed := `{"reputation": {"city": "Bolton", "country_code": "GB"},
	 "consensus": {"ipv4": [{"code": "GB", "percent": 50}, {"code": "US", "percent": 50}]},
	 "geo": {"cdn": [{"ipv4": {"value": "US"}}, {"ipv4": {"value": "GB"}}, {"ipv4": {}}]}}`
	check, err = SummarizeGeocheck([]byte(mixed), time.Unix(0, 0))
	if err != nil || check.Place == nil || check.Place.City != "Bolton" {
		t.Errorf("mixed edges place = %+v, %v", check.Place, err)
	}
}
