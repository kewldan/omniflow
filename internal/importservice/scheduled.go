package importservice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// maxScheduledPages bounds one scheduled run: 500 pages of 500 users is a
// quarter of a million panel users, far past any installation this serves, and
// a bound means a panel that never reports its end cannot hold the pass.
const maxScheduledPages = 500

// RunAll previews every Remnawave user and applies the clean records, end to
// end, as the operator would from the panel. It is what the scheduled import
// runs, so a user created directly in the panel becomes a customer without
// anybody pressing a button.
//
// It never resolves a conflict: a user whose Telegram ID already belongs to a
// customer, or who is already mapped, is staged as a conflict and left for an
// operator, exactly as in a manual import.
func (service *Service) RunAll(ctx context.Context) (dbgen.CustomerImport, error) {
	run, err := service.Preview(ctx, "", 500)
	if err != nil {
		return run, err
	}
	id := uuidText(run.ID)
	for page := 0; run.Status == "previewing" && page < maxScheduledPages; page++ {
		if run, err = service.Preview(ctx, id, 500); err != nil {
			return run, err
		}
	}
	switch run.Status {
	case "failed":
		return run, errors.New("the Remnawave user list could not be read")
	case "ready":
	default:
		return run, nil
	}
	if run.ValidCount == 0 {
		return run, nil
	}
	for page := 0; (run.Status == "ready" || run.Status == "applying") && page < maxScheduledPages; page++ {
		if run, err = service.Apply(ctx, id, 500); err != nil {
			return run, err
		}
	}
	return run, nil
}

func uuidText(id pgtype.UUID) string {
	value, err := id.Value()
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
