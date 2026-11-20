package app

import (
	"context"

	"github.com/omniflow/omniflow/internal/commercepg"
	"github.com/omniflow/omniflow/internal/remnawave"
)

// SquadLister adapts the Remnawave client to the commerce store's pre-charge
// squad check, which reads only the identifiers of the panel's internal
// squads. A nil client gives no lister, and the check is skipped.
func SquadLister(client *remnawave.Client) commercepg.SquadLister {
	if client == nil {
		return nil
	}
	return func(ctx context.Context) ([]string, error) {
		squads, err := client.InternalSquads(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(squads))
		for _, squad := range squads {
			ids = append(ids, squad.UUID)
		}
		return ids, nil
	}
}
