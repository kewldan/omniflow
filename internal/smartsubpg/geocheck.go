package smartsubpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/omniflow/omniflow/internal/smartsub"
	"github.com/valkey-io/valkey-go"
)

// Geocheck timing. A node takes up to a minute to answer; the summaries are
// kept for a few runs, so a node that fails one check keeps its last result
// and a check that stops running entirely ages out instead of lingering.
const (
	// GeocheckTaskName names the worker's periodic pass, GeocheckInterval is
	// how often it runs, and GeocheckTimeout bounds one pass over every node.
	GeocheckTaskName = "smart_subscription_geocheck"
	GeocheckInterval = 6 * time.Hour
	GeocheckTimeout  = 10 * time.Minute

	geocheckWait      = 2 * time.Minute
	geocheckPoll      = 5 * time.Second
	geocheckTTL       = 3 * 24 * time.Hour
	geocheckParallel  = 4
	geocheckNodeLimit = 200
	geocheckStoreKey  = "omniflow:smartsub:geochecks"
	checksTTL         = time.Minute
)

// CheckStore keeps the latest geocheck summary of each node. It is a cache of
// what Remnawave's nodes reported, not a record: losing it costs a few hours
// of empty service badges until the next pass, and Omniflow keeps no node
// table of its own (decision 0004).
type CheckStore interface {
	Load(ctx context.Context) (map[string]smartsub.NodeCheck, error)
	Save(ctx context.Context, checks map[string]smartsub.NodeCheck) error
}

// ValkeyChecks keeps the summaries in one Valkey key with a TTL.
type ValkeyChecks struct{ Client valkey.Client }

// Load reads the summaries, or none when there are none yet.
func (store ValkeyChecks) Load(ctx context.Context) (map[string]smartsub.NodeCheck, error) {
	raw, err := store.Client.Do(ctx, store.Client.B().Get().Key(geocheckStoreKey).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return map[string]smartsub.NodeCheck{}, nil
	}
	if err != nil {
		return nil, err
	}
	checks := map[string]smartsub.NodeCheck{}
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		return map[string]smartsub.NodeCheck{}, nil
	}
	return checks, nil
}

// Save replaces the summaries.
func (store ValkeyChecks) Save(ctx context.Context, checks map[string]smartsub.NodeCheck) error {
	encoded, err := json.Marshal(checks)
	if err != nil {
		return err
	}
	return store.Client.Do(ctx, store.Client.B().Set().Key(geocheckStoreKey).Value(string(encoded)).
		Px(geocheckTTL).Build()).Error()
}

// WithChecks gives the service the store the node checks are kept in.
func (service *Service) WithChecks(store CheckStore) *Service {
	service.checkStore = store
	return service
}

// NodeChecks reads the latest summaries through a one-minute cache. Without a
// store, or when it does not answer, there are none: the servers screen then
// shows no service badges rather than failing.
func (service *Service) NodeChecks(ctx context.Context) map[string]smartsub.NodeCheck {
	if service.checkStore == nil {
		return nil
	}
	service.mu.Lock()
	cached, at := service.checks, service.checksAt
	service.mu.Unlock()
	if !at.IsZero() && service.clock().Sub(at) < checksTTL {
		return cached
	}
	checks, err := service.checkStore.Load(ctx)
	if err != nil {
		service.logger.Warn("smart subscription could not read node checks", "error", err)
		checks = cached
	}
	service.mu.Lock()
	service.checks, service.checksAt = checks, service.clock()
	service.mu.Unlock()
	return checks
}

// RunGeochecks asks Remnawave to run its geocheck on every connected node and
// keeps a summary of each report. It does nothing while the module is off.
// A node whose check fails keeps its previous summary; a node the panel no
// longer lists is dropped.
func (service *Service) RunGeochecks(ctx context.Context) error {
	if service.checkStore == nil || !service.Settings(ctx).Enabled {
		return nil
	}
	return service.refreshChecks(ctx)
}

// refreshChecks is the pass itself, with the module's switch left to the
// caller.
func (service *Service) refreshChecks(ctx context.Context) error {
	nodes, err := service.panel.Nodes(ctx)
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}
	if len(nodes) > geocheckNodeLimit {
		nodes = nodes[:geocheckNodeLimit]
	}
	previous, err := service.checkStore.Load(ctx)
	if err != nil {
		return fmt.Errorf("read node checks: %w", err)
	}

	var (
		mu       sync.Mutex
		fresh    = map[string]smartsub.NodeCheck{}
		failures []string
		wait     sync.WaitGroup
		slots    = make(chan struct{}, geocheckParallel)
	)
	listed := map[string]bool{}
	for _, node := range nodes {
		id := strings.ToLower(node.UUID)
		listed[id] = true
		if node.IsDisabled || !node.IsConnected {
			continue
		}
		wait.Add(1)
		go func(id, name string) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			check, err := service.geocheck(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, name+": "+err.Error())
				return
			}
			fresh[id] = check
		}(id, node.Name)
	}
	wait.Wait()

	kept := map[string]smartsub.NodeCheck{}
	for id, check := range previous {
		if listed[id] {
			kept[id] = check
		}
	}
	for id, check := range fresh {
		kept[id] = check
	}
	if err := service.checkStore.Save(ctx, kept); err != nil {
		return fmt.Errorf("save node checks: %w", err)
	}
	service.mu.Lock()
	service.checks, service.checksAt = kept, service.clock()
	service.mu.Unlock()
	if len(failures) > 0 {
		service.logger.Warn("smart subscription geocheck failed on some nodes", "nodes", failures)
		if len(fresh) == 0 {
			return errors.New("geocheck failed on every node")
		}
	}
	return nil
}

// geocheck runs one node's check and waits for it.
func (service *Service) geocheck(ctx context.Context, nodeUUID string) (smartsub.NodeCheck, error) {
	job, err := service.panel.RequestGeocheck(ctx, nodeUUID)
	if err != nil {
		return smartsub.NodeCheck{}, err
	}
	if job == "" {
		return smartsub.NodeCheck{}, errors.New("panel returned no job")
	}
	deadline := time.NewTimer(geocheckWait)
	defer deadline.Stop()
	ticker := time.NewTicker(geocheckPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return smartsub.NodeCheck{}, ctx.Err()
		case <-deadline.C:
			return smartsub.NodeCheck{}, errors.New("node did not answer in time")
		case <-ticker.C:
		}
		state, err := service.panel.Geocheck(ctx, job)
		if err != nil {
			return smartsub.NodeCheck{}, err
		}
		switch {
		case state.Failed || state.Completed && !state.Success:
			if state.Message != "" {
				return smartsub.NodeCheck{}, errors.New(state.Message)
			}
			return smartsub.NodeCheck{}, errors.New("check failed")
		case state.Completed:
			if len(state.Report) == 0 {
				return smartsub.NodeCheck{}, errors.New("check returned no report")
			}
			return smartsub.SummarizeGeocheck(state.Report, service.clock())
		}
	}
}
