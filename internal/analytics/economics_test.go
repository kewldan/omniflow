package analytics

import (
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
)

func TestChannelValueSetsCostAgainstValue(t *testing.T) {
	lines := channelValue([]dbgen.PayerChannelValueRow{
		{Channel: "utm:yandex", Currency: "RUB", Payers: 4, RevenueMinor: 80000, Months: 8},
		{Channel: "organic", Currency: "RUB", Payers: 10, RevenueMinor: 100000, Months: 10},
	}, map[string]map[string]int64{
		"utm:yandex":      {"RUB": 40000},
		"campaign:spring": {"RUB": 5000},
	})
	byChannel := map[string]ChannelValueLine{}
	for _, line := range lines {
		byChannel[line.Channel] = line
	}
	yandex := byChannel["utm:yandex"]
	if yandex.CacMinor == nil || *yandex.CacMinor != 10000 {
		t.Fatalf("CAC = %v, want 10000", yandex.CacMinor)
	}
	if yandex.LtvToCac == nil || *yandex.LtvToCac != 2 {
		t.Fatalf("LTV:CAC = %v, want 2", yandex.LtvToCac)
	}
	// 80000 over eight payer-months is 10000 a month; a 10000 cost repays in one.
	if yandex.PaybackMonths == nil || *yandex.PaybackMonths != 1 {
		t.Fatalf("payback = %v, want 1", yandex.PaybackMonths)
	}
	if organic := byChannel["organic"]; organic.CacMinor != nil || organic.PaybackMonths != nil {
		t.Fatalf("organic has a cost: %+v", organic)
	}
	// Money spent on a channel that brought nobody is still listed.
	if spring, ok := byChannel["campaign:spring"]; !ok || spring.SpendMinor != 5000 || spring.Payers != 0 {
		t.Fatalf("unproductive spend = %+v", spring)
	}
}

func TestPerGibibyteIgnoresNegligibleTraffic(t *testing.T) {
	costs := []Money{{Currency: "EUR", AmountMinor: 1000}}
	if got := perGibibyte(costs, gibibyte/20); len(got) != 0 {
		t.Fatalf("cost over a twentieth of a GiB = %v, want nothing", got)
	}
	if got := perGibibyte(costs, 10*gibibyte); len(got) != 1 || got[0].AmountMinor != 100 {
		t.Fatalf("cost over 10 GiB = %v, want 100 a GiB", got)
	}
}

func TestCensusFallsBackToTheOnlineMoment(t *testing.T) {
	online := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	unlimited, limited := 0, 3
	params := censusParams([]remnawave.User{
		{ID: 1, HWIDDeviceLimit: &limited, Traffic: remnawave.UserTraffic{OnlineAt: &online, LastConnectedNodeUUID: "node"}},
		{ID: 2, HWIDDeviceLimit: &unlimited},
		{ID: 3},
		{ID: 0},
	}, map[int64]int32{1: 2})
	if len(params.RemnawaveUserIds) != 3 {
		t.Fatalf("recorded %d users, want 3 (an ID of zero is not a user)", len(params.RemnawaveUserIds))
	}
	if !params.FirstConnectedAts[0].Valid || !params.FirstConnectedAts[0].Time.Equal(online) {
		t.Fatalf("first connection = %v, want the online moment", params.FirstConnectedAts[0])
	}
	if params.FirstConnectedAts[1].Valid {
		t.Fatal("a user never online has a first connection")
	}
	if params.ActiveDevices[0] != 2 || params.DeviceLimits[0] != 3 || params.DeviceLimits[1] != 0 || params.DeviceLimits[2] != -1 {
		t.Fatalf("devices %v limits %v", params.ActiveDevices, params.DeviceLimits)
	}
}
