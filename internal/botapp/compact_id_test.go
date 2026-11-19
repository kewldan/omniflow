package botapp

import "testing"

func TestCompactIDRoundTrips(t *testing.T) {
	id := "6a0f3c1e-2b4d-4e8f-9a1b-3c5d7e9f1a2b"
	compact := compactID(id)
	if len(compact) != 22 {
		t.Fatalf("compact id %q is %d characters, want 22", compact, len(compact))
	}
	if back, ok := expandCompactID(compact); !ok || back != id {
		t.Fatalf("expand(%q) = %q %v", compact, back, ok)
	}
	if _, ok := expandCompactID("not-an-id"); ok {
		t.Fatal("a malformed compact id was accepted")
	}
	if len("act:opms:"+compact+":cloudpayments") > 64 {
		t.Fatal("the save-card callback exceeds Telegram's 64 bytes")
	}
}
