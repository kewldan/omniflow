package smartsub

import (
	"math"
	"strings"
)

// Place is where a node is, as well as its geocheck report says: the city
// one reputation database names, checked against the country the
// geolocation databases agree on. Lat and Lon are the city's, or a
// representative city's for the country when the city is not one Omniflow
// knows.
type Place struct {
	Country string  `json:"country"`
	City    string  `json:"city,omitempty"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

type city struct {
	country  string
	lat, lon float64
}

// cities are the places hosting providers commonly sit in, by lowercased
// name. A node elsewhere is placed at its country's entry in countryPlaces.
var cities = map[string]city{
	"amsterdam": {"NL", 52.37, 4.90}, "rotterdam": {"NL", 51.92, 4.48}, "dronten": {"NL", 52.53, 5.72},
	"frankfurt": {"DE", 50.11, 8.68}, "frankfurt am main": {"DE", 50.11, 8.68}, "berlin": {"DE", 52.52, 13.40},
	"munich": {"DE", 48.14, 11.58}, "nuremberg": {"DE", 49.45, 11.08}, "falkenstein": {"DE", 50.48, 12.37},
	"dusseldorf": {"DE", 51.23, 6.78}, "düsseldorf": {"DE", 51.23, 6.78}, "hamburg": {"DE", 53.55, 9.99},
	"london": {"GB", 51.51, -0.13}, "manchester": {"GB", 53.48, -2.24}, "bolton": {"GB", 53.58, -2.43},
	"paris": {"FR", 48.86, 2.35}, "roubaix": {"FR", 50.69, 3.18}, "gravelines": {"FR", 50.99, 2.13},
	"strasbourg": {"FR", 48.57, 7.75}, "marseille": {"FR", 43.30, 5.37},
	"warsaw": {"PL", 52.23, 21.01}, "krakow": {"PL", 50.06, 19.94},
	"helsinki": {"FI", 60.17, 24.94}, "espoo": {"FI", 60.21, 24.66},
	"stockholm": {"SE", 59.33, 18.07}, "oslo": {"NO", 59.91, 10.75}, "copenhagen": {"DK", 55.68, 12.57},
	"vienna": {"AT", 48.21, 16.37}, "geneva": {"CH", 46.20, 6.14}, "zurich": {"CH", 47.38, 8.54},
	"prague": {"CZ", 50.08, 14.44}, "bratislava": {"SK", 48.15, 17.11}, "budapest": {"HU", 47.50, 19.04},
	"bucharest": {"RO", 44.43, 26.10}, "sofia": {"BG", 42.70, 23.32}, "belgrade": {"RS", 44.79, 20.45},
	"riga": {"LV", 56.95, 24.11}, "tallinn": {"EE", 59.44, 24.75}, "vilnius": {"LT", 54.69, 25.28},
	"milan": {"IT", 45.46, 9.19}, "rome": {"IT", 41.90, 12.50}, "madrid": {"ES", 40.42, -3.70},
	"barcelona": {"ES", 41.39, 2.17}, "lisbon": {"PT", 38.72, -9.14}, "dublin": {"IE", 53.35, -6.26},
	"brussels": {"BE", 50.85, 4.35}, "luxembourg": {"LU", 49.61, 6.13},
	"moscow": {"RU", 55.76, 37.62}, "saint petersburg": {"RU", 59.93, 30.34}, "st petersburg": {"RU", 59.93, 30.34},
	"kyiv": {"UA", 50.45, 30.52}, "kiev": {"UA", 50.45, 30.52}, "chisinau": {"MD", 47.01, 28.86},
	"istanbul": {"TR", 41.01, 28.98}, "tbilisi": {"GE", 41.72, 44.79}, "yerevan": {"AM", 40.18, 44.51},
	"almaty": {"KZ", 43.24, 76.89}, "astana": {"KZ", 51.17, 71.43}, "tashkent": {"UZ", 41.30, 69.24},
	"dubai": {"AE", 25.20, 55.27}, "tel aviv": {"IL", 32.09, 34.78},
	"new york": {"US", 40.71, -74.01}, "ashburn": {"US", 39.04, -77.49}, "secaucus": {"US", 40.79, -74.06},
	"chicago": {"US", 41.88, -87.63}, "dallas": {"US", 32.78, -96.80}, "miami": {"US", 25.76, -80.19},
	"atlanta": {"US", 33.75, -84.39}, "los angeles": {"US", 34.05, -118.24}, "san jose": {"US", 37.34, -121.89},
	"seattle": {"US", 47.61, -122.33}, "toronto": {"CA", 43.65, -79.38}, "montreal": {"CA", 45.50, -73.57},
	"sao paulo": {"BR", -23.55, -46.63}, "são paulo": {"BR", -23.55, -46.63},
	"tokyo": {"JP", 35.68, 139.69}, "osaka": {"JP", 34.69, 135.50}, "seoul": {"KR", 37.57, 126.98},
	"singapore": {"SG", 1.35, 103.82}, "hong kong": {"HK", 22.32, 114.17}, "mumbai": {"IN", 19.08, 72.88},
	"sydney": {"AU", -33.87, 151.21},
}

// countryPlaces place a node whose city is unknown: the city most hosting in
// the country is in, which for a latency estimate beats the country's middle.
var countryPlaces = map[string]city{
	"NL": cities["amsterdam"], "DE": cities["frankfurt"], "GB": cities["london"], "FR": cities["paris"],
	"PL": cities["warsaw"], "FI": cities["helsinki"], "SE": cities["stockholm"], "NO": cities["oslo"],
	"DK": cities["copenhagen"], "AT": cities["vienna"], "CH": cities["zurich"], "CZ": cities["prague"],
	"SK": cities["bratislava"], "HU": cities["budapest"], "RO": cities["bucharest"], "BG": cities["sofia"],
	"RS": cities["belgrade"], "LV": cities["riga"], "EE": cities["tallinn"], "LT": cities["vilnius"],
	"IT": cities["milan"], "ES": cities["madrid"], "PT": cities["lisbon"], "IE": cities["dublin"],
	"BE": cities["brussels"], "LU": cities["luxembourg"], "RU": cities["moscow"], "UA": cities["kyiv"],
	"MD": cities["chisinau"], "TR": cities["istanbul"], "GE": cities["tbilisi"], "AM": cities["yerevan"],
	"KZ": cities["almaty"], "UZ": cities["tashkent"], "AE": cities["dubai"], "IL": cities["tel aviv"],
	"US": cities["ashburn"], "CA": cities["toronto"], "BR": cities["sao paulo"], "JP": cities["tokyo"],
	"KR": cities["seoul"], "SG": cities["singapore"], "HK": cities["hong kong"], "IN": cities["mumbai"],
	"AU": cities["sydney"],
}

// locate places a node. The reputation database's city is trusted when it
// lies in the country the geolocation databases agree on, or when they do
// not agree on one; otherwise the agreed country's usual city is used.
func locate(cityName, cityCountry, consensus string) *Place {
	cityName = strings.TrimSpace(cityName)
	cityCountry = strings.ToUpper(strings.TrimSpace(cityCountry))
	consensus = strings.ToUpper(strings.TrimSpace(consensus))
	if known, ok := cities[strings.ToLower(cityName)]; ok && (consensus == "" || consensus == known.country) {
		return &Place{Country: known.country, City: cityName, Lat: known.lat, Lon: known.lon}
	}
	country := consensus
	if country == "" {
		country = cityCountry
	}
	if known, ok := countryPlaces[country]; ok {
		return &Place{Country: country, Lat: known.lat, Lon: known.lon}
	}
	return nil
}

// distanceKm is the great-circle distance between two places.
func distanceKm(a, b Place) float64 {
	const earthRadius = 6371.0
	radians := func(degrees float64) float64 { return degrees * math.Pi / 180 }
	dLat, dLon := radians(b.Lat-a.Lat), radians(b.Lon-a.Lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(radians(a.Lat))*math.Cos(radians(b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(h))
}
