package installsettings

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestDocumentReadersFallBackWhenUnset(t *testing.T) {
	document := Document{
		"interval": float64(20), "zero": float64(0), "text": "  hello ", "blank": " ",
		"on": true, "list": []any{"a", " b ", 3}, "csv": "x, y,,z", "numeric": "7",
	}
	if got := document.Int("interval", 15); got != 20 {
		t.Fatalf("Int = %d", got)
	}
	if got := document.Int("numeric", 0); got != 7 {
		t.Fatalf("Int from text = %d", got)
	}
	if got := document.Positive("zero", 15); got != 15 {
		t.Fatalf("a saved zero must fall back, got %d", got)
	}
	if got := document.Int("missing", 15); got != 15 {
		t.Fatalf("missing Int = %d", got)
	}
	if got := document.String("text", "x"); got != "hello" {
		t.Fatalf("String = %q", got)
	}
	if got := document.String("blank", "fallback"); got != "fallback" {
		t.Fatalf("blank String = %q", got)
	}
	if !document.Bool("on", false) || document.Bool("missing", false) || !document.Bool("missing", true) {
		t.Fatal("Bool must honour a saved value and fall back otherwise")
	}
	if got := document.Strings("list", nil); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Strings = %v", got)
	}
	if got := document.Strings("csv", nil); !slices.Equal(got, []string{"x", "y", "z"}) {
		t.Fatalf("Strings from text = %v", got)
	}
	if got := document.Duration("interval", time.Minute, time.Hour); got != 20*time.Minute {
		t.Fatalf("Duration = %v", got)
	}
	if got := document.Duration("zero", time.Minute, time.Hour); got != time.Hour {
		t.Fatalf("zero Duration = %v", got)
	}
	if document.Has("blank") || !document.Has("on") {
		t.Fatal("Has must ignore blank text")
	}
}

func TestQuietWindowWrapsMidnight(t *testing.T) {
	at := func(hour, minute int) time.Time { return time.Date(2026, 11, 20, hour, minute, 0, 0, time.UTC) }
	cases := []struct {
		start, end string
		now        time.Time
		want       bool
	}{
		{"23:00", "07:00", at(23, 30), true},
		{"23:00", "07:00", at(6, 59), true},
		{"23:00", "07:00", at(7, 0), false},
		{"09:00", "18:00", at(12, 0), true},
		{"09:00", "18:00", at(18, 0), false},
		{"", "07:00", at(1, 0), false},
		{"25:00", "07:00", at(1, 0), false},
		{"08:00", "08:00", at(8, 0), false},
	}
	for _, testCase := range cases {
		if got := WithinWindow(testCase.now, testCase.start, testCase.end); got != testCase.want {
			t.Errorf("WithinWindow(%s, %s–%s) = %v", testCase.now.Format("15:04"), testCase.start, testCase.end, got)
		}
	}
}

func TestStaticReaderServesDocuments(t *testing.T) {
	reader := Static(map[string]Document{"branding": {"timezone": "Europe/Moscow"}})
	if got := reader.Section(context.Background(), "branding").String("timezone", ""); got != "Europe/Moscow" {
		t.Fatalf("timezone = %q", got)
	}
	if reader.Location(context.Background()).String() != "Europe/Moscow" {
		t.Fatal("Location must load the saved zone")
	}
	if len(reader.Section(context.Background(), "unknown")) != 0 {
		t.Fatal("an unknown section is empty")
	}
	var missing *Reader
	if len(missing.Section(context.Background(), "branding")) != 0 {
		t.Fatal("a nil reader answers empty documents")
	}
}
