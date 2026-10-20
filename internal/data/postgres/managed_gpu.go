package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

var _ gpu.RefundContextStore = (*Repository)(nil)

type managedIntake struct {
	closing bool
	replay  []byte
}

// prepareManagedCommand serializes CREATE/DELETE on the tenant/resource key and
// validates the original immutable context before any business write. The
// caller uses this same transaction for service/spec/work and the durable ACK.
func prepareManagedCommand(ctx context.Context, tx pgx.Tx, command *inferencebiz.ManagedCommand, kind, operationID string) (managedIntake, error) {
	if command == nil {
		return managedIntake{}, fmt.Errorf("managed command is required")
	}
	original := command.Context
	tenant, err := parseUUID("tenant_id", original.TenantID, false)
	if err != nil {
		return managedIntake{}, err
	}
	resourceID, err := parseUUID("resource_id", original.ResourceID, false)
	if err != nil {
		return managedIntake{}, err
	}
	operation, err := parseUUID("operation_id", operationID, false)
	if err != nil {
		return managedIntake{}, err
	}
	createID, err := parseUUID("create_operation_id", original.OriginalCreateOperationID, false)
	if err != nil {
		return managedIntake{}, err
	}
	q := New(tx)
	if err := q.LockManagedGPUKey(ctx, original.TenantID+":command:"+operationID); err != nil {
		return managedIntake{}, err
	}
	if err := q.LockManagedGPUKey(ctx, original.TenantID+":"+original.ResourceID); err != nil {
		return managedIntake{}, err
	}
	existing, err := q.GetManagedGPUCommand(ctx, GetManagedGPUCommandParams{TenantID: tenant, OperationID: operation})
	if err == nil {
		if existing.PayloadHash != RequestHash(command.Payload) {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		return managedIntake{replay: existing.ResponseSnapshot}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return managedIntake{}, err
	}
	savedResource, err := q.GetManagedGPUResourceForUpdate(ctx, GetManagedGPUResourceForUpdateParams{TenantID: tenant, ResourceID: resourceID})
	if err == nil {
		var saved gpu.RefundContext
		if err := json.Unmarshal(savedResource.OriginalContext, &saved); err != nil {
			return managedIntake{}, err
		}
		if savedResource.CreateOperationID != createID {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		if original.ResourceClass != "" && savedResource.ResourceClass != original.ResourceClass {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		if original.CreateCommandDigest != "" && savedResource.CreateCommandDigest != "" && original.CreateCommandDigest != savedResource.CreateCommandDigest {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		if original.ClusterID != "" && saved.ClusterID != original.ClusterID {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		if kind == "create" && savedResource.DeleteOperationID.Valid {
			// DELETE-first closes the immutable resource association. A later
			// CREATE is acknowledged as blocked_by_delete after the command is
			// durably recorded; it must not be allowed to mutate the snapshot.
			return managedIntake{closing: true}, nil
		}
		// DELETE commands intentionally carry only tenant/resource/create/delete
		// identity. Restore the immutable CREATE snapshot from PostgreSQL rather
		// than asking callers to resend (or forge) cluster/execution data.
		if kind == "delete" {
			requestedDelete := original.DeleteOperationID
			original.DeleteOperationID = ""
			if original.OwnerService != "" && original.OwnerService != saved.OwnerService || original.ClusterID != "" && original.ClusterID != saved.ClusterID || original.Snapshot != nil && !reflect.DeepEqual(original.Snapshot, saved.Snapshot) {
				return managedIntake{}, inferencebiz.ErrIdempotencyConflict
			}
			original.OwnerService, original.ClusterID, original.Snapshot = saved.OwnerService, saved.ClusterID, saved.Snapshot
			original.ResourceClass, original.CreateCommandDigest = saved.ResourceClass, saved.CreateCommandDigest
			original.DeleteOperationID = requestedDelete
			command.Context = original
		} else {
			saved.DeleteOperationID, original.DeleteOperationID = "", ""
			if !reflect.DeepEqual(saved, original) {
				return managedIntake{}, inferencebiz.ErrIdempotencyConflict
			}
		}
		if kind == "delete" && savedResource.DeleteOperationID.Valid && savedResource.DeleteOperationID != operation {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		if kind == "create" && savedResource.BusinessPayloadDigest != "" && savedResource.BusinessPayloadDigest != command.BusinessDigest {
			return managedIntake{}, inferencebiz.ErrIdempotencyConflict
		}
		return managedIntake{closing: savedResource.DeleteOperationID.Valid}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return managedIntake{}, err
	}
	// A managed command may not adopt an old direct record or fabricate charges
	// for it. DELETE-first rows deliberately have no inference_services FK.
	directExists, err := q.ManagedGPUServiceExists(ctx, ManagedGPUServiceExistsParams{TenantID: tenant, ID: resourceID})
	if err != nil {
		return managedIntake{}, err
	}
	if directExists {
		return managedIntake{}, fmt.Errorf("%w: existing direct resource is not Governance managed", inferencebiz.ErrInvalidState)
	}
	original.DeleteOperationID = ""
	payload, err := json.Marshal(original)
	if err != nil {
		return managedIntake{}, err
	}
	// The column predates the execution snapshot and its CHECK remains the
	// compatibility marker. Execution details are stored in original_context;
	// this value does not represent or synthesize a charge vector.
	err = q.InsertManagedGPUResource(ctx, InsertManagedGPUResourceParams{TenantID: tenant, ResourceID: resourceID, CreateOperationID: createID, OwnerService: original.OwnerService, ResourceClass: managedClass(command), CreateCommandDigest: original.CreateCommandDigest, OriginalContext: payload})
	return managedIntake{}, err
}

func persistManagedCommand(ctx context.Context, tx pgx.Tx, command *inferencebiz.ManagedCommand, kind, operationID, intent string, response []byte) error {
	original := command.Context
	tenant, err := parseUUID("tenant_id", original.TenantID, false)
	if err != nil {
		return err
	}
	resourceID, err := parseUUID("resource_id", original.ResourceID, false)
	if err != nil {
		return err
	}
	operation, err := parseUUID("operation_id", operationID, false)
	if err != nil {
		return err
	}
	createID, err := parseUUID("create_operation_id", original.OriginalCreateOperationID, false)
	if err != nil {
		return err
	}
	q := New(tx)
	if kind == "delete" {
		err := q.SetManagedGPUClosing(ctx, SetManagedGPUClosingParams{TenantID: tenant, ResourceID: resourceID, DeleteOperationID: operation})
		if err != nil {
			return err
		}
	} else if intent != "blocked_by_delete" {
		err := q.SaveManagedGPUCreate(ctx, SaveManagedGPUCreateParams{TenantID: tenant, ResourceID: resourceID, BusinessPayload: command.Business, BusinessPayloadDigest: command.BusinessDigest, CreateRequestHash: command.RequestHash, CreatePayload: command.Payload})
		if err != nil {
			return err
		}
	}
	ack, err := json.Marshal(map[string]any{"operation_id": operationID, "resource_id": original.ResourceID, "accepted": true})
	if err != nil {
		return err
	}
	err = q.InsertManagedGPUCommand(ctx, InsertManagedGPUCommandParams{TenantID: tenant, OperationID: operation, ResourceID: resourceID, CreateOperationID: createID, Kind: kind, PayloadHash: RequestHash(command.Payload), RequestHash: command.RequestHash, CommandPayload: command.Payload, ResponseSnapshot: response, DurableAck: ack, ExecutionIntent: intent})
	return err
}

func decodeManagedResponse(raw []byte) (*inferencev1.OperationResponse, error) {
	out := new(inferencev1.OperationResponse)
	if err := protojson.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return out, nil
}

func managedIntentResponse(original gpu.RefundContext, operationID, kind, intent string) *inferencev1.OperationResponse {
	return &inferencev1.OperationResponse{
		Resource:        &inferencev1.InferenceService{Id: original.ResourceID, DesiredState: "deleted"},
		Operation:       &inferencev1.Operation{Id: operationID, ServiceId: original.ResourceID, Kind: kind, Phase: "pending", Step: intent},
		DurableOwnerAck: &commandv1.DurableOwnerAck{OperationId: operationID, ResourceId: original.ResourceID, Accepted: true},
	}
}

func (u *CommandUseCase) acceptManagedDelete(ctx context.Context, in inferencebiz.CommandInput) (*inferencev1.OperationResponse, error) {
	original := in.Managed.Context
	if in.Kind != "delete" || original.TenantID != in.TenantID || original.ResourceID != in.ServiceID || original.DeleteOperationID == "" {
		return nil, inferencebiz.ErrInvalidState
	}
	tx, err := u.repo.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	accepted, err := prepareManagedCommand(ctx, tx, in.Managed, "delete", original.DeleteOperationID)
	if err != nil {
		return nil, err
	}
	if len(accepted.replay) != 0 {
		return decodeManagedResponse(accepted.replay)
	}
	q := New(tx)
	service, serviceErr := q.GetServiceForUpdate(ctx, GetServiceForUpdateParams{TenantID: pgUUIDMust(original.TenantID), ID: pgUUIDMust(original.ResourceID)})
	if serviceErr != nil && !errors.Is(serviceErr, pgx.ErrNoRows) {
		return nil, serviceErr
	}
	if errors.Is(serviceErr, pgx.ErrNoRows) {
		// The resource lock also fences the final CREATE transaction. A missing
		// service alone is insufficient: retain unknown prior acceptance facts.
		unknown, err := q.ManagedExecutionFacts(ctx, ManagedExecutionFactsParams{TenantID: pgUUIDMust(original.TenantID), ResourceID: pgUUIDMust(original.ResourceID)})
		if err != nil {
			return nil, err
		}
		state, reason := "pending", ""
		if !unknown.Valid || unknown.Bool {
			state, reason = "unknown_execution", "prior accepted execution cannot be excluded"
		} else if managedClass(in.Managed) == "cpu" {
			state = "complete"
		}
		response := managedIntentResponse(original, original.DeleteOperationID, "delete", "closing_intent")
		snapshot, err := protojson.Marshal(response)
		if err != nil {
			return nil, err
		}
		if err := persistManagedCommand(ctx, tx, in.Managed, "delete", original.DeleteOperationID, "closing_intent", snapshot); err != nil {
			return nil, err
		}
		if err := q.SetManagedCloseState(ctx, SetManagedCloseStateParams{TenantID: pgUUIDMust(original.TenantID), OperationID: pgUUIDMust(original.DeleteOperationID), Actor: in.Actor, CloseState: state, CloseError: reason}); err != nil {
			return nil, err
		}
		if unknown.Valid && !unknown.Bool && managedClass(in.Managed) == "gpu" {
			scope, err := encodeManagedGPUReleaseScope(time.Now().UTC().Format(time.RFC3339Nano), nil)
			if err != nil {
				return nil, err
			}
			if err := q.UpsertManagedGPUReleaseScope(ctx, UpsertManagedGPUReleaseScopeParams{TenantID: pgUUIDMust(original.TenantID), ServiceID: pgUUIDMust(original.ResourceID), OperationID: pgUUIDMust(original.DeleteOperationID), Pods: scope}); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return response, nil
	}
	if service.DeletedAt.Valid || service.DesiredState == "deleted" {
		return nil, fmt.Errorf("%w: managed resource is already deleted", inferencebiz.ErrInvalidState)
	}
	targetGeneration := service.DesiredGeneration + 1
	cancelledActive, err := q.CancelActiveOperation(ctx, CancelActiveOperationParams{
		TenantID:  service.TenantID,
		ServiceID: service.ID,
	})
	if err != nil {
		return nil, err
	}
	if rows, err := q.TransitionService(ctx, TransitionServiceParams{
		DesiredState: "deleted", TargetGeneration: targetGeneration, OperationID: pgUUIDMust(original.DeleteOperationID),
		TenantID: service.TenantID, ServiceID: service.ID, ExpectedGeneration: service.DesiredGeneration,
	}); err != nil {
		return nil, err
	} else if rows != 1 {
		return nil, inferencebiz.ErrGenerationConflict
	}
	if err := q.InsertOperation(ctx, InsertOperationParams{TenantID: service.TenantID, ID: pgUUIDMust(original.DeleteOperationID), ServiceID: service.ID, Kind: "delete", Phase: "pending", Step: "withdraw_publication", TargetGeneration: targetGeneration, RequestHash: in.RequestHash}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, inferencebiz.ErrOperationActive
		}
		return nil, err
	}
	if cancelledActive > 0 {
		if _, err := q.ReleaseResourceWorkLease(ctx, ReleaseResourceWorkLeaseParams{
			TenantID:  service.TenantID,
			ServiceID: service.ID,
		}); err != nil {
			return nil, err
		}
	}
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: service.TenantID, ServiceID: service.ID, DirtyVersion: targetGeneration}); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"kind": "delete", "desired_state": "deleted"})
	if err := q.AppendAuditEvent(ctx, AppendAuditEventParams{TenantID: service.TenantID, EventID: pgUUID(uuid.New()), ServiceID: service.ID, OperationID: pgUUIDMust(original.DeleteOperationID), Generation: targetGeneration, EventType: "inference.delete.accepted", Actor: in.Actor, RequestID: in.RequestID, Payload: payload}); err != nil {
		return nil, err
	}
	response := commandResponse(service.ID.String(), original.DeleteOperationID, targetGeneration, "delete", "withdraw_publication")
	response.DurableOwnerAck = &commandv1.DurableOwnerAck{OperationId: original.DeleteOperationID, ResourceId: original.ResourceID, Accepted: true}
	snapshot, err := protojson.Marshal(response)
	if err != nil {
		return nil, err
	}
	if err := persistManagedCommand(ctx, tx, in.Managed, "delete", original.DeleteOperationID, "closing_intent", snapshot); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func pgUUIDMust(value string) pgtype.UUID {
	u, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgUUID(u)
}

// LoadRefundContext uses the original CREATE, never the current generation or
// most recent local operation. It requires the separately persisted DELETE.
func (r *Repository) LoadRefundContext(ctx context.Context, tenantID, resourceID, originalCreateID string) (gpu.RefundContext, error) {
	if r == nil || r.pool == nil {
		return gpu.RefundContext{}, errors.New("nil managed GPU repository")
	}
	tenant, err := parseUUID("tenant_id", tenantID, false)
	if err != nil {
		return gpu.RefundContext{}, err
	}
	resource, err := parseUUID("resource_id", resourceID, false)
	if err != nil {
		return gpu.RefundContext{}, err
	}
	createID, err := parseUUID("create_operation_id", originalCreateID, false)
	if err != nil {
		return gpu.RefundContext{}, err
	}
	row, err := New(r.pool).GetManagedGPURefundContext(ctx, GetManagedGPURefundContextParams{TenantID: tenant, ResourceID: resource, CreateOperationID: createID})
	if err != nil {
		return gpu.RefundContext{}, err
	}
	var original gpu.RefundContext
	if err := json.Unmarshal(row.OriginalContext, &original); err != nil {
		return gpu.RefundContext{}, err
	}
	if row.DeleteOperationID.Valid {
		original.DeleteOperationID = row.DeleteOperationID.String()
	}
	return original, nil
}

func managedClass(command *inferencebiz.ManagedCommand) string {
	if command.ResourceClass != "" {
		return command.ResourceClass
	}
	if command.Context.ResourceClass != "" {
		return command.Context.ResourceClass
	}
	if command.Context.Snapshot != nil {
		return "gpu"
	}
	return "cpu"
}

// PrepareCreate returns replay/blocked acceptance before Model metadata reads.
// Unaccepted CREATEs leave no row; final CreateService repeats the locked check.
func (r *Repository) PrepareCreate(ctx context.Context, in inferencebiz.CreateInput) (*inferencev1.OperationResponse, bool, error) {
	if in.Managed == nil {
		return nil, false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	accepted, err := prepareManagedCommand(ctx, tx, in.Managed, "create", in.Managed.Context.OriginalCreateOperationID)
	if err != nil {
		return nil, false, err
	}
	if len(accepted.replay) > 0 {
		out, err := decodeManagedResponse(accepted.replay)
		return out, true, err
	}
	if !accepted.closing {
		return nil, false, nil
	}
	out := managedIntentResponse(in.Managed.Context, in.Managed.Context.OriginalCreateOperationID, "create", "blocked_by_delete")
	raw, err := protojson.Marshal(out)
	if err != nil {
		return nil, false, err
	}
	if err := persistManagedCommand(ctx, tx, in.Managed, "create", in.Managed.Context.OriginalCreateOperationID, "blocked_by_delete", raw); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return out, true, nil
}
