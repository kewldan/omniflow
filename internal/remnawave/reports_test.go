package remnawave

import (
	"context"
	"net/http"
	"testing"
)

func TestReadOnlyReportRoutes(t *testing.T) {
	t.Parallel()
	client := contractClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s", request.Method)
		}
		switch request.URL.Path {
		case "/api/hwid/devices/stats":
			_, _ = writer.Write([]byte(`{"response":{"byPlatform":[{"platform":"iOS","count":3,"byApp":[{"app":"Happ","count":2}]}],
				"stats":{"totalUniqueDevices":3,"totalHwidDevices":4,"averageHwidDevicesPerUser":1.5}}}`))
		case "/api/users/7/subscription-request-history":
			_, _ = writer.Write([]byte(`{"response":{"total":1,"records":[{"id":1,"userId":7,
				"requestAt":"2026-11-01T10:00:00.000Z","requestIp":"203.0.113.5","userAgent":"Happ/3"}]}}`))
		case "/api/hwid/devices":
			if request.URL.RawQuery != "start=0&size=500" {
				t.Errorf("query = %s", request.URL.RawQuery)
			}
			_, _ = writer.Write([]byte(`{"response":{"devices":[{"hwid":"x","userId":7,"updatedAt":"2026-11-01T10:00:00.000Z"}],"total":1}}`))
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	})
	stats, err := client.DeviceStats(context.Background())
	if err != nil || len(stats.ByPlatform) != 1 || stats.ByPlatform[0].ByApp[0].App != "Happ" || stats.Stats.TotalHWIDDevices != 4 {
		t.Fatalf("stats = %+v (%v)", stats, err)
	}
	records, err := client.SubscriptionRequests(context.Background(), 7)
	if err != nil || len(records) != 1 || records[0].UserAgent != "Happ/3" {
		t.Fatalf("records = %+v (%v)", records, err)
	}
	devices, total, err := client.AllDevices(context.Background(), 0, 500)
	if err != nil || total != 1 || devices[0].UserID != 7 {
		t.Fatalf("devices = %+v %d (%v)", devices, total, err)
	}
}
