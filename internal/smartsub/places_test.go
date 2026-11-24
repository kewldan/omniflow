package smartsub

import (
	"math"
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

func TestHopAcrossTheAtlantic(t *testing.T) {
	// Measured between the production nodes: a continental hop keeps its
	// rate, and an ocean crossing costs less per kilometre.
	for _, testCase := range []struct {
		from, to Place
		measured float64
	}{
		{*locate("Warsaw", "PL", "PL"), *locate("Amsterdam", "NL", "NL"), 23.5},
		{*locate("Warsaw", "PL", "PL"), *locate("Ashburn", "US", "US"), 112.5},
		{*locate("Amsterdam", "NL", "NL"), *locate("Ashburn", "US", "US"), 88.0},
		{*locate("Geneva", "CH", "CH"), *locate("Ashburn", "US", "US"), 96.4},
	} {
		from, to := testCase.from, testCase.to
		hop, ok := HopMs(NodeCheck{Place: &from}, NodeCheck{Place: &to})
		if !ok || math.Abs(hop-testCase.measured) > 15 {
			t.Errorf("%s→%s = %.1f ms, measured %.1f", from.City, to.City, hop, testCase.measured)
		}
	}
}
