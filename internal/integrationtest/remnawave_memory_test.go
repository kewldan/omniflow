//go:build integration

package integrationtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/fulfillment"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// memoryRemnawave is a working stand-in for the panel: users are created,
// updated, enabled, and read back, so a test can follow what fulfillment
// actually pushed. onCall, when set, runs before every request, which is where
// a test observes what the worker is holding while Remnawave is being asked.
type memoryRemnawave struct {
	mu     sync.Mutex
	nextID int64
	users  map[int64]remnawave.User
	calls  []string
	onCall func(call string)
}

func newMemoryRemnawave() *memoryRemnawave {
	return &memoryRemnawave{nextID: 100, users: map[int64]remnawave.User{}}
}

func (panel *memoryRemnawave) record(call string) {
	panel.mu.Lock()
	panel.calls = append(panel.calls, call)
	hook := panel.onCall
	panel.mu.Unlock()
	if hook != nil {
		hook(call)
	}
}

func (panel *memoryRemnawave) User(_ context.Context, id int64) (remnawave.User, error) {
	panel.record("User")
	panel.mu.Lock()
	defer panel.mu.Unlock()
	user, ok := panel.users[id]
	if !ok {
		return remnawave.User{}, remnawave.ErrNotFound
	}
	return user, nil
}

func (panel *memoryRemnawave) UserByUsername(_ context.Context, username string) (remnawave.User, error) {
	panel.record("UserByUsername")
	panel.mu.Lock()
	defer panel.mu.Unlock()
	for _, user := range panel.users {
		if user.Username == username {
			return user, nil
		}
	}
	return remnawave.User{}, remnawave.ErrNotFound
}

func (panel *memoryRemnawave) apply(user remnawave.User, desired remnawave.ProvisionUser) remnawave.User {
	user.ExpireAt = desired.ExpireAt
	user.TrafficLimitBytes = 0
	if desired.TrafficLimitBytes != nil {
		user.TrafficLimitBytes = *desired.TrafficLimitBytes
	}
	user.HWIDDeviceLimit = desired.HWIDDeviceLimit
	user.ActiveInternalSquads = nil
	for _, squad := range desired.InternalSquadIDs {
		user.ActiveInternalSquads = append(user.ActiveInternalSquads, remnawave.InternalSquad{UUID: squad})
	}
	return user
}

func (panel *memoryRemnawave) CreateUser(_ context.Context, desired remnawave.ProvisionUser) (remnawave.User, error) {
	panel.record("CreateUser")
	panel.mu.Lock()
	defer panel.mu.Unlock()
	panel.nextID++
	user := panel.apply(remnawave.User{ID: panel.nextID, Username: desired.Username, Status: "ACTIVE"}, desired)
	panel.users[user.ID] = user
	return user, nil
}

func (panel *memoryRemnawave) UpdateUser(_ context.Context, id int64, desired remnawave.ProvisionUser) (remnawave.User, error) {
	panel.record("UpdateUser")
	panel.mu.Lock()
	defer panel.mu.Unlock()
	user, ok := panel.users[id]
	if !ok {
		return remnawave.User{}, remnawave.ErrNotFound
	}
	user = panel.apply(user, desired)
	panel.users[id] = user
	return user, nil
}

func (panel *memoryRemnawave) setStatus(id int64, status string) error {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	user, ok := panel.users[id]
	if !ok {
		return remnawave.ErrNotFound
	}
	user.Status = status
	panel.users[id] = user
	return nil
}

func (panel *memoryRemnawave) EnableUser(_ context.Context, id int64) error {
	panel.record("EnableUser")
	return panel.setStatus(id, "ACTIVE")
}

func (panel *memoryRemnawave) DisableUser(_ context.Context, id int64) error {
	panel.record("DisableUser")
	return panel.setStatus(id, "DISABLED")
}

func (panel *memoryRemnawave) ResetUserTraffic(context.Context, int64) error {
	panel.record("ResetUserTraffic")
	return nil
}

// userCount reports how many Remnawave users exist.
func (panel *memoryRemnawave) userCount() int {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	return len(panel.users)
}

// callCount reports how many requests the panel has received.
func (panel *memoryRemnawave) callCount() int {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	return len(panel.calls)
}

// expiryOf reads the expiry a user carries.
func (panel *memoryRemnawave) expiryOf(id int64) time.Time {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	return panel.users[id].ExpireAt
}

// runFulfillment works one operation the way River would, once.
func runFulfillment(ctx context.Context, worker *fulfillment.Worker, operationID string) error {
	return worker.Work(ctx, &river.Job[fulfillment.JobArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1}, Args: fulfillment.JobArgs{OperationID: operationID},
	})
}

// fulfillAll runs every operation that is still waiting, oldest first, until
// none is left. It is what the queue would do over the next few seconds.
func (harness *harness) fulfillAll(ctx context.Context, t *testing.T, worker *fulfillment.Worker) {
	t.Helper()
	for range 20 {
		rows, err := harness.pool.Query(ctx, `SELECT id::text FROM fulfillment_operations
			WHERE status IN ('pending', 'retrying') ORDER BY created_at`)
		if err != nil {
			t.Fatalf("list operations: %v", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("scan operations: %v", err)
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if err := runFulfillment(ctx, worker, id); err != nil {
				t.Fatalf("fulfil %s: %v", id, err)
			}
		}
	}
	t.Fatal("fulfillment did not settle within twenty passes")
}
