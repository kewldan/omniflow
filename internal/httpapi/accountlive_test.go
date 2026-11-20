package httpapi

import (
	"maps"
	"testing"
)

func TestLiveVersionsRoundTripThroughTheEventID(t *testing.T) {
	versions := map[string]string{"support": "41.0.1700000000000", "orders": "1.2", "wallet": "9"}
	encoded := encodeLiveVersions(versions)
	if encoded != "orders=1.2;support=41.0.1700000000000;wallet=9" {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	if decoded := decodeLiveVersions(encoded); !maps.Equal(decoded, versions) {
		t.Fatalf("round trip lost data: %v", decoded)
	}
	for _, unreadable := range []string{"", "garbage", "=1", string(make([]byte, 600))} {
		if decodeLiveVersions(unreadable) != nil {
			t.Errorf("%q should yield a fresh baseline", unreadable)
		}
	}
}
