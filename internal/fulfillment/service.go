package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	databaseutil "github.com/omniflow/omniflow/internal/database"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/riverqueue/river"
)

type Service struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
	clock  func() time.Time
}

func NewService(pool *pgxpool.Pool, client *river.Client[pgx.Tx]) *Service {
	return &Service{pool: pool, client: client, clock: time.Now}
}

type OperationInput struct {
	Operation             string
	IdempotencyKey        string
	CorrelationID         string
	EffectiveAt           *time.Time
	EndsAt                *time.Time
	TrafficAllowanceBytes *int64
	DeviceLimit           *int32
	SquadIDs              []string
}

func (service *Service) Enqueue(ctx context.Context, entitlementID string, input OperationInput) (dbgen.FulfillmentOperation, error) {
	// Pause and resume are absent on purpose. They are not operations a caller
	// composes out of parameters: each is a Remnawave change *and* a change to
	// the entitlement's own clock, and the two have to commit together. They
	// have their own methods below.
	allowed := map[string]bool{"create": true, "extend": true, "enable": true, "disable": true, "reset_traffic": true, "set_limits": true, "set_squads": true, "reconcile": true}
	if !allowed[input.Operation] || input.IdempotencyKey == "" || input.CorrelationID == "" {
		return dbgen.FulfillmentOperation{}, errors.New("operation, idempotency key, and correlation ID are required")
	}
	if input.TrafficAllowanceBytes != nil && *input.TrafficAllowanceBytes < 0 || input.DeviceLimit != nil && *input.DeviceLimit < 0 {
		return dbgen.FulfillmentOperation{}, errors.New("traffic and device limits cannot be negative")
	}
	id, err := parseUUID(entitlementID)
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	entitlement, err := queries.GetEntitlement(ctx, id)
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	effectiveAt := service.clock().UTC()
	if input.EffectiveAt != nil {
		effectiveAt = input.EffectiveAt.UTC()
	}
	endsAt := entitlement.EndsAt.Time
	if input.EndsAt != nil {
		endsAt = input.EndsAt.UTC()
	}
	if input.Operation == "extend" && (input.EndsAt == nil || !endsAt.After(entitlement.EndsAt.Time)) {
		return dbgen.FulfillmentOperation{}, errors.New("extend requires an endsAt later than the current entitlement")
	}
	traffic := nullableInt8(entitlement.TrafficAllowanceBytes)
	if input.TrafficAllowanceBytes != nil {
		traffic = *input.TrafficAllowanceBytes
	}
	deviceLimit := nullableInt4(entitlement.DeviceLimit)
	if input.DeviceLimit != nil {
		deviceLimit = *input.DeviceLimit
	}
	squads := databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds)
	if input.SquadIDs != nil {
		if _, err = databaseutil.ParseUUIDs(input.SquadIDs); err != nil {
			return dbgen.FulfillmentOperation{}, err
		}
		squads = input.SquadIDs
	}
	desired, err := json.Marshal(map[string]any{"effectiveAt": effectiveAt, "endsAt": endsAt, "trafficAllowanceBytes": traffic, "deviceLimit": deviceLimit, "squadIds": squads})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	operation, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{EntitlementID: id, Operation: input.Operation, IdempotencyKey: input.IdempotencyKey, CorrelationID: input.CorrelationID, DesiredState: desired})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	if operation.EntitlementID != id || operation.Operation != input.Operation {
		return dbgen.FulfillmentOperation{}, errors.New("idempotency key was already used with different fulfillment parameters")
	}
	var persisted desiredState
	if err = json.Unmarshal(operation.DesiredState, &persisted); err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	if input.EffectiveAt != nil && !persisted.EffectiveAt.Equal(input.EffectiveAt.UTC()) || input.EndsAt != nil && !persisted.EndsAt.Equal(input.EndsAt.UTC()) || input.TrafficAllowanceBytes != nil && (persisted.TrafficAllowanceBytes == nil || *persisted.TrafficAllowanceBytes != *input.TrafficAllowanceBytes) || input.DeviceLimit != nil && (persisted.DeviceLimit == nil || *persisted.DeviceLimit != int(*input.DeviceLimit)) || input.SquadIDs != nil && !sameStringSet(persisted.SquadIDs, input.SquadIDs) {
		return dbgen.FulfillmentOperation{}, errors.New("idempotency key was already used with different fulfillment parameters")
	}
	if _, err = service.client.InsertTx(ctx, tx, JobArgs{OperationID: uuidString(operation.ID)}, InsertOpts()); err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	return operation, nil
}

// ErrNotPausable is returned when an entitlement cannot enter or leave a pause.
//
// One sentinel rather than two, because the caller's answer is the same either
// way: the subscription is not in a state where this makes sense, and the panel
// says so. The predicate that decides it lives in the SQL, so two operators
// pressing the button at the same moment produce one pause and one refusal
// rather than a second pause that resets the instant the first recorded.
var ErrNotPausable = errors.New("the subscription is not in a state that can be paused or resumed")

// Pause stops a subscription without spending its remaining days.
//
// It is one transaction because it is one fact recorded in two places: the
// entitlement's clock stops here, and the Remnawave user is disabled by the job
// this enqueues. Committing the first without the second would leave a customer
// paying for time they cannot use; the second without the first would stop the
// clock on a subscription that still connects.
func (service *Service) Pause(
	ctx context.Context, entitlementID, idempotencyKey, correlationID string,
) (dbgen.FulfillmentOperation, error) {
	return service.pauseTransition(ctx, entitlementID, "pause", idempotencyKey, correlationID)
}

// Resume gives back exactly the time the pause took and re-enables access.
//
// The new expiry is read back from the update rather than computed here, so the
// instant the job pushes to Remnawave is the instant the database committed. A
// figure calculated in Go from a clock a moment earlier would drift from the
// row by the length of the transaction, and the drift detector would then
// report the difference forever.
func (service *Service) Resume(
	ctx context.Context, entitlementID, idempotencyKey, correlationID string,
) (dbgen.FulfillmentOperation, error) {
	return service.pauseTransition(ctx, entitlementID, "resume", idempotencyKey, correlationID)
}

func (service *Service) pauseTransition(
	ctx context.Context, entitlementID, operation, idempotencyKey, correlationID string,
) (dbgen.FulfillmentOperation, error) {
	if idempotencyKey == "" || correlationID == "" {
		return dbgen.FulfillmentOperation{}, errors.New("idempotency key and correlation ID are required")
	}
	id, err := parseUUID(entitlementID)
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}

	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)

	var entitlement dbgen.Entitlement
	if operation == "pause" {
		entitlement, err = queries.PauseEntitlement(ctx, id)
	} else {
		entitlement, err = queries.ResumeEntitlement(ctx, id)
	}
	if err != nil {
		// No row means the guard in the predicate refused: the entitlement was
		// not in a state this transition applies to.
		if errors.Is(err, pgx.ErrNoRows) {
			return dbgen.FulfillmentOperation{}, ErrNotPausable
		}
		return dbgen.FulfillmentOperation{}, err
	}

	desired, err := json.Marshal(map[string]any{
		"effectiveAt": service.clock().UTC(),
		// The expiry the job pushes is the one the row now holds, which for a
		// resume is already moved forward by the pause.
		"endsAt":                entitlement.EndsAt.Time,
		"trafficAllowanceBytes": nullableInt8(entitlement.TrafficAllowanceBytes),
		"deviceLimit":           nullableInt4(entitlement.DeviceLimit),
		"squadIds":              databaseutil.UUIDStrings(entitlement.RemnawaveSquadIds),
	})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}

	created, err := queries.CreateFulfillmentOperation(ctx, dbgen.CreateFulfillmentOperationParams{
		EntitlementID: id, Operation: operation,
		IdempotencyKey: idempotencyKey, CorrelationID: correlationID, DesiredState: desired,
	})
	if err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	if _, err = service.client.InsertTx(
		ctx, tx, JobArgs{OperationID: uuidString(created.ID)}, InsertOpts(),
	); err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return dbgen.FulfillmentOperation{}, err
	}
	return created, nil
}
