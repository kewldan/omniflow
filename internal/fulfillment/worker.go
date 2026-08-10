package fulfillment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omniflow/omniflow/internal/database/dbgen"
	"github.com/omniflow/omniflow/internal/remnawave"
	"github.com/riverqueue/river"
)

type Worker struct {
	river.WorkerDefaults[JobArgs]
	pool        *pgxpool.Pool
	provisioner remnawave.Provisioner
	clock       func() time.Time
}

func NewWorker(pool *pgxpool.Pool, provisioner remnawave.Provisioner) *Worker {
	return &Worker{pool: pool, provisioner: provisioner, clock: time.Now}
}

type desiredState struct {
	EffectiveAt           time.Time `json:"effectiveAt"`
	EndsAt                time.Time `json:"endsAt"`
	TrafficAllowanceBytes *int64    `json:"trafficAllowanceBytes"`
	DeviceLimit           *int      `json:"deviceLimit"`
	SquadIDs              []string  `json:"squadIds"`
}

func (worker *Worker) Work(ctx context.Context, job *river.Job[JobArgs]) error {
	operationID, err := parseUUID(job.Args.OperationID)
	if err != nil {
		return river.JobCancel(err)
	}
	tx, err := worker.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbgen.New(tx)
	operation, err := queries.LockFulfillmentOperation(ctx, operationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return river.JobCancel(err)
		}
		return err
	}
	if operation.Status == "succeeded" || operation.Status == "cancelled" {
		return tx.Commit(ctx)
	}
	entitlement, err := queries.GetEntitlement(ctx, operation.EntitlementID)
	if err != nil {
		return err
	}
	var desired desiredState
	if err := json.Unmarshal(operation.DesiredState, &desired); err != nil {
		return worker.failPermanent(ctx, tx, queries, operation, "invalid_desired_state")
	}
	if !desired.EffectiveAt.IsZero() && worker.clock().Before(desired.EffectiveAt) {
		return river.JobSnooze(desired.EffectiveAt.Sub(worker.clock()))
	}
	operation, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "running", AttemptCount: operation.AttemptCount + 1, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}})
	if err != nil {
		return err
	}
	if operation.Operation == "reconcile" {
		if err := worker.detectDrift(ctx, queries, entitlement, desired); err != nil {
			code := classifyError(err)
			_, _ = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "retrying", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: code, Valid: true}})
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return commitErr
			}
			return err
		}
	}
	remote, callErr := worker.apply(ctx, queries, operation.Operation, entitlement, desired)
	if callErr != nil {
		code := classifyError(callErr)
		nextAttempt := worker.clock().Add(backoff(operation.AttemptCount))
		_, _ = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "retrying", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: nextAttempt, Valid: true}, LastErrorCode: pgtype.Text{String: code, Valid: true}})
		_, _ = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "retrying", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(`{}`), ErrorCode: pgtype.Text{String: code, Valid: true}})
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return commitErr
		}
		return callErr
	}
	observed, _ := json.Marshal(map[string]any{"status": remote.Status, "expireAt": remote.ExpireAt, "trafficLimitBytes": remote.TrafficLimitBytes, "deviceLimit": remote.HWIDDeviceLimit})
	if _, err = queries.UpsertRemnawaveMapping(ctx, dbgen.UpsertRemnawaveMappingParams{UserID: entitlement.UserID, RemnawaveID: remote.ID, ObservedState: observed}); err != nil {
		return err
	}
	if _, err = queries.UpdateEntitlementObservedState(ctx, dbgen.UpdateEntitlementObservedStateParams{EntitlementID: entitlement.ID, Status: observedEntitlementStatus(remote), RemnawaveUserID: pgtype.Int8{Int64: remote.ID, Valid: true}, ObservedState: observed}); err != nil {
		return err
	}
	if err = queries.ResolveEntitlementDrifts(ctx, entitlement.ID); err != nil {
		return err
	}
	if operation.Operation == "create" {
		if err = queries.SupersedePreviousEntitlements(ctx, dbgen.SupersedePreviousEntitlementsParams{UserID: entitlement.UserID, CurrentEntitlementID: entitlement.ID}); err != nil {
			return err
		}
	}
	if _, err = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "succeeded", AttemptCount: operation.AttemptCount, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}}); err != nil {
		return err
	}
	if _, err = queries.InsertFulfillmentHistory(ctx, dbgen.InsertFulfillmentHistoryParams{OperationID: operation.ID, Status: "succeeded", CorrelationID: operation.CorrelationID, RequestSummary: safeDesiredSummary(desired), ResponseSummary: []byte(fmt.Sprintf(`{"remnawaveUserId":%d,"status":%q}`, remote.ID, remote.Status))}); err != nil {
		return err
	}
	if operation.Operation == "create" {
		if _, err = queries.SetOrderState(ctx, dbgen.SetOrderStateParams{ID: entitlement.OrderID, State: "fulfilled"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (worker *Worker) detectDrift(ctx context.Context, queries *dbgen.Queries, entitlement dbgen.Entitlement, desired desiredState) error {
	mapping, err := queries.GetRemnawaveMappingByCustomer(ctx, entitlement.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: "missing_remote", Expected: []byte(`{"exists":true}`), Observed: []byte(`{"exists":false}`)})
		return nil
	}
	if err != nil {
		return nil
	}
	remote, err := worker.provisioner.User(ctx, mapping.RemnawaveID)
	if errors.Is(err, remnawave.ErrNotFound) {
		_, _ = queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: "missing_remote", Expected: []byte(`{"exists":true}`), Observed: []byte(`{"exists":false}`)})
		return nil
	}
	if err != nil {
		return err
	}
	type mismatch struct {
		kind               string
		expected, observed any
	}
	mismatches := make([]mismatch, 0, 4)
	if !remote.ExpireAt.Equal(desired.EndsAt) {
		mismatches = append(mismatches, mismatch{"expiry", desired.EndsAt, remote.ExpireAt})
	}
	if desired.TrafficAllowanceBytes != nil && remote.TrafficLimitBytes != *desired.TrafficAllowanceBytes {
		mismatches = append(mismatches, mismatch{"traffic", *desired.TrafficAllowanceBytes, remote.TrafficLimitBytes})
	}
	if desired.DeviceLimit != nil && (remote.HWIDDeviceLimit == nil || *remote.HWIDDeviceLimit != *desired.DeviceLimit) {
		mismatches = append(mismatches, mismatch{"device_limit", desired.DeviceLimit, remote.HWIDDeviceLimit})
	}
	remoteSquads := make([]string, 0, len(remote.ActiveInternalSquads))
	for _, squad := range remote.ActiveInternalSquads {
		remoteSquads = append(remoteSquads, squad.UUID)
	}
	if !sameStringSet(desired.SquadIDs, remoteSquads) {
		mismatches = append(mismatches, mismatch{"squads", desired.SquadIDs, remoteSquads})
	}
	if observedEntitlementStatus(remote) != entitlement.Status {
		mismatches = append(mismatches, mismatch{"status", entitlement.Status, observedEntitlementStatus(remote)})
	}
	for _, item := range mismatches {
		expected, _ := json.Marshal(map[string]any{"value": item.expected})
		observed, _ := json.Marshal(map[string]any{"value": item.observed})
		if _, err := queries.InsertEntitlementDrift(ctx, dbgen.InsertEntitlementDriftParams{EntitlementID: entitlement.ID, Kind: item.kind, Expected: expected, Observed: observed}); err != nil {
			return err
		}
	}
	return nil
}

func (worker *Worker) apply(ctx context.Context, queries *dbgen.Queries, operation string, entitlement dbgen.Entitlement, desired desiredState) (remnawave.User, error) {
	username := "omniflow_" + strings.ReplaceAll(uuidString(entitlement.UserID), "-", "")[:16]
	provision := remnawave.ProvisionUser{Username: username, ExpireAt: desired.EndsAt, TrafficLimitBytes: desired.TrafficAllowanceBytes, HWIDDeviceLimit: desired.DeviceLimit, InternalSquadIDs: desired.SquadIDs}
	mapping, mappingErr := queries.GetRemnawaveMappingByCustomer(ctx, entitlement.UserID)
	remoteID := int64(0)
	if mappingErr == nil {
		remoteID = mapping.RemnawaveID
	} else if !errors.Is(mappingErr, pgx.ErrNoRows) {
		return remnawave.User{}, mappingErr
	}
	createOrRecover := func() (remnawave.User, error) {
		if existing, err := worker.provisioner.UserByUsername(ctx, username); err == nil {
			return worker.provisioner.UpdateUser(ctx, existing.ID, provision)
		} else if !errors.Is(err, remnawave.ErrNotFound) {
			return remnawave.User{}, err
		}
		return worker.provisioner.CreateUser(ctx, provision)
	}
	if (operation == "create" || operation == "reconcile") && remoteID == 0 {
		return createOrRecover()
	}
	if operation == "create" || operation == "reconcile" {
		updated, updateErr := worker.provisioner.UpdateUser(ctx, remoteID, provision)
		if errors.Is(updateErr, remnawave.ErrNotFound) {
			return createOrRecover()
		}
		return updated, updateErr
	}
	if remoteID == 0 {
		return remnawave.User{}, remnawave.ErrNotFound
	}
	switch operation {
	case "extend", "set_limits", "set_squads":
		return worker.provisioner.UpdateUser(ctx, remoteID, provision)
	case "enable":
		if err := worker.provisioner.EnableUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	case "disable":
		if err := worker.provisioner.DisableUser(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	case "reset_traffic":
		if err := worker.provisioner.ResetUserTraffic(ctx, remoteID); err != nil {
			return remnawave.User{}, err
		}
	default:
		return remnawave.User{}, river.JobCancel(errors.New("unsupported fulfillment operation"))
	}
	return worker.provisioner.User(ctx, remoteID)
}

func (worker *Worker) failPermanent(ctx context.Context, tx pgx.Tx, queries *dbgen.Queries, operation dbgen.FulfillmentOperation, code string) error {
	_, _ = queries.UpdateFulfillmentOperation(ctx, dbgen.UpdateFulfillmentOperationParams{OperationID: operation.ID, Status: "failed", AttemptCount: operation.AttemptCount + 1, NextAttemptAt: pgtype.Timestamptz{Time: worker.clock(), Valid: true}, LastErrorCode: pgtype.Text{String: code, Valid: true}})
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return river.JobCancel(errors.New(code))
}

func safeDesiredSummary(desired desiredState) []byte {
	value, _ := json.Marshal(map[string]any{"endsAt": desired.EndsAt, "trafficLimitConfigured": desired.TrafficAllowanceBytes != nil, "deviceLimitConfigured": desired.DeviceLimit != nil, "squadCount": len(desired.SquadIDs)})
	return value
}

func classifyError(err error) string {
	if errors.Is(err, remnawave.ErrNotFound) {
		return "remote_user_not_found"
	}
	var apiError *remnawave.APIError
	if errors.As(err, &apiError) {
		return fmt.Sprintf("remnawave_http_%d", apiError.StatusCode)
	}
	return "remnawave_unavailable"
}

func backoff(attempt int32) time.Duration {
	seconds := int64(5)
	for range min(attempt, 8) {
		seconds *= 2
	}
	return time.Duration(seconds) * time.Second
}

func observedEntitlementStatus(user remnawave.User) string {
	switch strings.ToUpper(user.Status) {
	case "ACTIVE":
		return "active"
	case "LIMITED":
		return "limited"
	case "DISABLED":
		return "disabled"
	case "EXPIRED":
		return "expired"
	default:
		return "failed"
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func parseUUID(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil || !id.Valid {
		return pgtype.UUID{}, errors.New("invalid operation ID")
	}
	return id, nil
}

func uuidString(value pgtype.UUID) string {
	b := value.Bytes
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
