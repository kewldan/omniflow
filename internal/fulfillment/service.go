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
