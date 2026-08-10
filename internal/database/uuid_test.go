package database

import "testing"

func TestUUIDListRoundTrip(t *testing.T) {
	t.Parallel()

	values := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"7f3c2ea6-3c1d-4d57-b459-20cc5c8191a2",
	}
	parsed, err := ParseUUIDs(values)
	if err != nil {
		t.Fatal(err)
	}
	result := UUIDStrings(parsed)
	for index := range values {
		if result[index] != values[index] {
			t.Fatalf("UUID %d = %q, want %q", index, result[index], values[index])
		}
	}
}

func TestParseUUIDsRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	if _, err := ParseUUIDs([]string{"not-a-uuid"}); err == nil {
		t.Fatal("expected invalid UUID error")
	}
}
