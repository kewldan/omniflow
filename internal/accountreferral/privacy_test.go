package accountreferral

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func instant(t *testing.T, value string) pgtype.Timestamptz {
	t.Helper()
	if value == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: mustTime(t, value), Valid: true}
}

// Whether a deletion request is open is derived from three append-only records
// rather than a flag, because none of the three may be edited. The ordering rule
// is the whole safety property: a withdrawn request must not still be pending,
// and a fresh request after a withdrawal must be.
func TestDeletionIsPendingOnlyWhileItIsTheMostRecentDecision(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		cancelled string
		completed string
		pending   bool
	}{
		{name: "never asked", pending: false},
		{name: "asked and waiting", requested: "2026-08-12T10:00:00Z", pending: true},
		{
			name:      "asked and then withdrawn",
			requested: "2026-08-12T10:00:00Z", cancelled: "2026-08-12T11:00:00Z", pending: false,
		},
		{
			name:      "withdrawn once and asked again",
			requested: "2026-08-12T12:00:00Z", cancelled: "2026-08-12T11:00:00Z", pending: true,
		},
		{
			name:      "already carried out",
			requested: "2026-08-12T10:00:00Z", completed: "2026-08-13T10:00:00Z", pending: false,
		},
		{
			name:      "a withdrawal at the same instant wins",
			requested: "2026-08-12T10:00:00Z", cancelled: "2026-08-12T10:00:00Z", pending: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			pending := deletionPending(
				instant(t, testCase.requested),
				instant(t, testCase.cancelled),
				instant(t, testCase.completed),
			)
			if pending != testCase.pending {
				t.Fatalf("pending = %v, want %v", pending, testCase.pending)
			}
		})
	}
}

// The executor is stated rather than implied. The difference between "requested"
// and "done" is what this whole route is built around, and a screen that does
// not say who acts invites a customer to believe their data is already gone.
func TestDeletionAlwaysNamesWhatWouldCarryItOut(t *testing.T) {
	if DeletionExecutor == "" {
		t.Fatal("a deletion request must name what executes it")
	}
	if DeletionExecutor == "account_web" {
		t.Fatal("the customer panel must never be the executor of a deletion")
	}
}

// Contact normalization is not cosmetic: uniqueness is enforced on a MAC of the
// value, so two spellings of one address would otherwise be two channels.
func TestContactNormalizationCollapsesSpellingsAndRefusesNonsense(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		value  string
		want   string
		refuse bool
	}{
		{name: "email case", kind: "email", value: "  Person@Example.COM ", want: "person@example.com"},
		{name: "phone spacing", kind: "phone", value: "+7 (900) 000-00-00", want: "+79000000000"},
		{name: "telegram handle", kind: "telegram", value: "@Somebody", want: "somebody"},
		{name: "kind case", kind: "EMAIL", value: "person@example.com", want: "person@example.com"},
		{name: "empty", kind: "email", value: "   ", refuse: true},
		{name: "not an address", kind: "email", value: "person.example.com", refuse: true},
		{name: "two at signs", kind: "email", value: "a@b@example.com", refuse: true},
		{name: "letters in a phone", kind: "phone", value: "+7900OOOOOOO", refuse: true},
		{name: "handle too short", kind: "telegram", value: "@abc", refuse: true},
		{name: "unknown kind", kind: "fax", value: "12345", refuse: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			kind, value, err := NormalizeContact(testCase.kind, testCase.value)
			if testCase.refuse {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("expected a refusal, got %q/%q and %v", kind, value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if value != testCase.want {
				t.Fatalf("value = %q, want %q", value, testCase.want)
			}
			if kind != "email" && kind != "phone" && kind != "telegram" {
				t.Fatalf("kind = %q, want one the schema admits", kind)
			}
		})
	}
}

// A too-long value is refused before it is sealed, so an oversized write never
// reaches the database and never reaches a log line either.
func TestContactValueIsBounded(t *testing.T) {
	long := "a"
	for range 9 {
		long += long
	}
	if _, _, err := NormalizeContact("email", long+"@example.com"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected an oversized address to be refused, got %v", err)
	}
}

// The clock is injected so a caller can reproduce a document's timestamp. A
// service that read the wall clock directly could not be tested for it.
func TestServiceNormalizesItsClockToUTC(t *testing.T) {
	moscow := time.FixedZone("MSK", 3*60*60)
	service := &Service{clock: func() time.Time {
		return time.Date(2026, 8, 12, 13, 0, 0, 0, moscow)
	}}
	if zone := service.now().Location(); zone != time.UTC {
		t.Fatalf("now() is in %s, want UTC", zone)
	}
	if hour := service.now().Hour(); hour != 10 {
		t.Fatalf("now() = %d o'clock, want the UTC instant", hour)
	}
}
