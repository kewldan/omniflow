package commercepg

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrSquadUnavailable refuses an order whose squads the Remnawave panel no
// longer has. Charging for it would provision a user that cannot connect, or
// fail provisioning after the money moved.
var ErrSquadUnavailable = errors.New("squad_unavailable")

// SquadLister reads the identifiers of the internal squads Remnawave has now.
type SquadLister func(context.Context) ([]string, error)

// squadCacheTTL is how long a read of the panel's squads is trusted. A squad
// deleted in the panel is refused within a minute; a checkout does not ask
// the panel on every re-price.
const squadCacheTTL = time.Minute

// squadCheck is the cached squad list behind the pre-charge check.
type squadCheck struct {
	mu      sync.Mutex
	list    SquadLister
	known   map[string]bool
	readAt  time.Time
	timeout time.Duration
}

// SetSquadLister turns on the check that every squad an order would grant
// still exists in Remnawave before any money moves, as Bedolaga's checkout
// squad validation does. Processes that take orders set it from their
// Remnawave client; without it the check is skipped.
func (store *Store) SetSquadLister(list SquadLister) {
	if list == nil {
		store.squads = nil
		return
	}
	store.squads = &squadCheck{list: list, timeout: 5 * time.Second}
}

// assertSquadsExist refuses squads the panel does not have. A panel that does
// not answer does not block a sale: the check exists to catch a squad an
// operator deleted, and an outage is what fulfillment's retries are for.
func (store *Store) assertSquadsExist(ctx context.Context, squadIDs []string) error {
	if store.squads == nil || len(squadIDs) == 0 {
		return nil
	}
	known, ok := store.squads.current(ctx, store.clock())
	if !ok {
		return nil
	}
	for _, id := range squadIDs {
		if !known[strings.ToLower(id)] {
			return ErrSquadUnavailable
		}
	}
	return nil
}

// pricedSquads is every squad the priced add-on lines would grant.
func pricedSquads(priced []pricedAddon) []string {
	squads := make([]string, 0, len(priced))
	for _, line := range priced {
		for _, id := range line.row.RemnawaveSquadIds {
			squads = append(squads, uuidString(id))
		}
	}
	return squads
}

// current returns the cached squad set, reading the panel when it is stale.
// It reports false when the panel could not be read.
func (check *squadCheck) current(ctx context.Context, now time.Time) (map[string]bool, bool) {
	check.mu.Lock()
	defer check.mu.Unlock()
	if check.known != nil && now.Sub(check.readAt) < squadCacheTTL {
		return check.known, true
	}
	readCtx, cancel := context.WithTimeout(ctx, check.timeout)
	defer cancel()
	ids, err := check.list(readCtx)
	if err != nil {
		return nil, false
	}
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[strings.ToLower(id)] = true
	}
	check.known, check.readAt = known, now
	return known, true
}
