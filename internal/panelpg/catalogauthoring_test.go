package panelpg

import (
	"errors"
	"testing"
)

func validVersion() PlanVersionInput {
	return PlanVersionInput{
		BillingPeriod: "month", DurationSeconds: 30 * 86_400,
		SquadSelection: "automatic", UpgradePolicy: "extend",
		DowngradePolicy: "at_expiry", CancellationPolicy: "at_expiry",
		TrialEligibility: "new_customer", Prices: map[string]int64{"RUB": 19900},
	}
}

func offered(ids ...string) []OfferedSquad {
	squads := make([]OfferedSquad, 0, len(ids))
	for _, id := range ids {
		squads = append(squads, OfferedSquad{SquadID: id, LabelRU: "Германия", LabelEN: "Germany"})
	}
	return squads
}

// The editor once sent `customer_choice` and `fixed`, which the database
// refuses, and refused the `optional` and `required` it does accept. These are
// the values that must now publish, and the ones that must not.
func TestPlanVersionValidationAcceptsTheDatabaseVocabulary(t *testing.T) {
	t.Parallel()
	for _, selection := range []string{"automatic", "optional", "required"} {
		input := validVersion()
		input.SquadSelection = selection
		if selection != "automatic" {
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111")
			input.MinSelectableSquads = 1
		}
		if err := validatePlanVersion(input); err != nil {
			t.Fatalf("%s refused: %v", selection, err)
		}
	}
	for _, rule := range []string{"new_customer", "never_trialled", "any"} {
		input := validVersion()
		input.TrialEligibility = rule
		if err := validatePlanVersion(input); err != nil {
			t.Fatalf("trial rule %s refused: %v", rule, err)
		}
	}
	for _, period := range []string{"none", "half_year"} {
		input := validVersion()
		input.BillingPeriod = period
		if err := validatePlanVersion(input); err != nil {
			t.Fatalf("billing period %s refused: %v", period, err)
		}
	}
	for _, stale := range []string{"customer_choice", "fixed"} {
		input := validVersion()
		input.SquadSelection = stale
		input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111")
		if err := validatePlanVersion(input); !errors.Is(err, ErrValidaton) {
			t.Fatalf("%s was accepted", stale)
		}
	}
}

func TestSquadChoiceRefusesSelectionsTheCheckoutCouldNeverSatisfy(t *testing.T) {
	t.Parallel()
	zero := 0
	cases := map[string]func(*PlanVersionInput){
		"choice with nothing offered": func(input *PlanVersionInput) { input.SquadSelection = "optional" },
		"required with no minimum": func(input *PlanVersionInput) {
			input.SquadSelection, input.OfferedSquads = "required", offered("11111111-1111-1111-1111-111111111111")
		},
		"minimum above what is offered": func(input *PlanVersionInput) {
			input.SquadSelection, input.MinSelectableSquads = "required", 2
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111")
		},
		"maximum below minimum": func(input *PlanVersionInput) {
			input.SquadSelection, input.MinSelectableSquads = "optional", 2
			maximum := int32(1)
			input.MaxSelectableSquads = &maximum
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
		},
		"maximum of zero": func(input *PlanVersionInput) {
			input.SquadSelection = "optional"
			maximum := int32(zero)
			input.MaxSelectableSquads = &maximum
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111")
		},
		"the same squad twice": func(input *PlanVersionInput) {
			input.SquadSelection = "optional"
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111", "11111111-1111-1111-1111-111111111111")
		},
		"a blank label": func(input *PlanVersionInput) {
			input.SquadSelection = "optional"
			input.OfferedSquads = []OfferedSquad{{SquadID: "11111111-1111-1111-1111-111111111111", LabelRU: " ", LabelEN: "Germany"}}
		},
		"offered squads on an automatic version": func(input *PlanVersionInput) {
			input.OfferedSquads = offered("11111111-1111-1111-1111-111111111111")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := validVersion()
			mutate(&input)
			if err := validatePlanVersion(input); !errors.Is(err, ErrValidaton) {
				t.Fatalf("accepted: %+v", input)
			}
		})
	}
}
