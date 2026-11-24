package smartsubpg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/omniflow/omniflow/internal/smartsub"
)

type geocheckPanel struct {
	Panel
	nodes []remnawave.Node
	mu    sync.Mutex
	asked []string
}

func (panel *geocheckPanel) Nodes(context.Context) ([]remnawave.Node, error) { return panel.nodes, nil }

func (panel *geocheckPanel) RequestGeocheck(_ context.Context, node string) (string, error) {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	panel.asked = append(panel.asked, node)
	if node == "broken" {
		return "", errors.New("node unreachable")
	}
	return "job-" + node, nil
}

func (panel *geocheckPanel) Geocheck(_ context.Context, job string) (remnawave.GeocheckJob, error) {
	return remnawave.GeocheckJob{
		Completed: true, Success: true,
		Report: []byte(`{"timestamp":"2026-11-24T07:00:00Z","stash_checks":[{"id":"gemini_access","state":"blocked","region":"RUS"}]}`),
	}, nil
}

type memoryChecks struct{ saved map[string]smartsub.NodeCheck }

func (store *memoryChecks) Load(context.Context) (map[string]smartsub.NodeCheck, error) {
	copied := map[string]smartsub.NodeCheck{}
	for id, check := range store.saved {
		copied[id] = check
	}
	return copied, nil
}

func (store *memoryChecks) Save(_ context.Context, checks map[string]smartsub.NodeCheck) error {
	store.saved = checks
	return nil
}

func TestRefreshChecks(t *testing.T) {
	panel := &geocheckPanel{nodes: []remnawave.Node{
		{UUID: "fresh", Name: "Fresh", IsConnected: true},
		{UUID: "broken", Name: "Broken", IsConnected: true},
		{UUID: "offline", Name: "Offline", IsConnected: false},
	}}
	old := smartsub.NodeCheck{Services: map[string]smartsub.ServiceStatus{"reddit": {State: smartsub.StateYes}}}
	store := &memoryChecks{saved: map[string]smartsub.NodeCheck{"broken": old, "offline": old, "retired": old}}
	service := New(nil, panel, "", nil).WithChecks(store)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.refreshChecks(ctx); err != nil {
		t.Fatal(err)
	}
	// The connected node is checked, the one that failed and the offline one
	// keep their last summary, and a node the panel no longer lists is gone.
	if got := store.saved["fresh"].Services["gemini"]; got.State != smartsub.StateNo || got.Region != "RUS" {
		t.Errorf("fresh = %v", store.saved["fresh"])
	}
	if store.saved["broken"].Services["reddit"].State != smartsub.StateYes || len(store.saved["offline"].Services) != 1 {
		t.Errorf("kept = %v", store.saved)
	}
	if _, ok := store.saved["retired"]; ok || len(store.saved) != 3 {
		t.Errorf("saved = %v", store.saved)
	}
	if len(panel.asked) != 2 {
		t.Errorf("asked = %v, want only the connected nodes", panel.asked)
	}
	if service.NodeChecks(ctx)["fresh"].Services == nil {
		t.Error("the pass did not refresh the cached checks")
	}
}
