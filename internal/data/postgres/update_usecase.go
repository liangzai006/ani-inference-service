package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	gpubiz "github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type UpdateUseCase struct{ repo *Repository }

func endpointValues(e *inferencebiz.EndpointSpec) (pgtype.Int4, pgtype.Int4, pgtype.Text, pgtype.Text) {
	if e == nil {
		return pgtype.Int4{}, pgtype.Int4{}, pgtype.Text{}, pgtype.Text{}
	}
	return pgtype.Int4{Int32: e.ContainerPort, Valid: true}, pgtype.Int4{Int32: e.ServicePort, Valid: true}, pgtype.Text{String: e.TargetPort, Valid: true}, pgtype.Text{String: e.Protocol, Valid: true}
}

func NewUpdateUseCase(repo *Repository) *UpdateUseCase { return &UpdateUseCase{repo: repo} }

func (u *UpdateUseCase) Update(ctx context.Context, in inferencebiz.UpdateInput) (*inferencev1.OperationResponse, error) {
	if u == nil || u.repo == nil || u.repo.pool == nil {
		return nil, errors.New("nil postgres update repository")
	}
	tenant, err := parseUUID("tenant_id", in.TenantID, false)
	if err != nil {
		return nil, err
	}
	gpuPlanJSON, err := json.Marshal(in.GPUSnapshot)
	if in.GPUSnapshot == nil {
		gpuPlanJSON = nil
	}
	if err != nil {
		return nil, err
	}
	gpuSnapshotDigest := in.GPUSnapshotDigest
	if gpuSnapshotDigest == "" && in.GPUSnapshot != nil {
		gpuSnapshotDigest = in.GPUSnapshot.ExecutionDigest
	}
	var engineJSON []byte
	if in.EngineCommand != "" {
		engineJSON, err = marshalEngineSpec(in.EngineCommand)
		if err != nil {
			return nil, err
		}
	}
	serviceID, err := parseUUID("service_id", in.ServiceID, false)
	if err != nil {
		return nil, err
	}
	modelVersionID, err := parseUUID("model_version_id", in.ModelVersionID, true)
	if err != nil {
		return nil, err
	}
	if in.ExpectedGeneration < 1 {
		return nil, inferencebiz.ErrGenerationConflict
	}
	opID := pgUUID(uuid.New())
	if in.ManagedOperationID != "" {
		opID, err = parseUUID("managed_gpu_operation_id", in.ManagedOperationID, false)
		if err != nil {
			return nil, err
		}
	}
	tx, err := u.repo.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := New(tx)
	method := "InferenceService/update"
	if in.ManagedOperationID != "" {
		if err := q.LockManagedGPUKey(ctx, in.TenantID+":command:"+in.ManagedOperationID); err != nil {
			return nil, err
		}
		if existing, e := q.GetOperation(ctx, GetOperationParams{TenantID: tenant, ID: opID}); e == nil {
			if existing.RequestHash != in.RequestHash {
				return nil, inferencebiz.ErrIdempotencyConflict
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return managedUpdateResponseFromOperation(existing), nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	if in.RequestID != "" {
		if err := q.LockIdempotency(ctx, LockIdempotencyParams{TenantID: in.TenantID, Method: method, IdempotencyKey: in.RequestID}); err != nil {
			return nil, err
		}
		existing, e := q.GetIdempotency(ctx, GetIdempotencyParams{TenantID: tenant, Method: method, IdempotencyKey: in.RequestID})
		if e == nil {
			if existing.PayloadHash != in.RequestHash {
				return nil, inferencebiz.ErrIdempotencyConflict
			}
			op, e := q.GetOperation(ctx, GetOperationParams{TenantID: tenant, ID: existing.OperationID})
			if e != nil {
				return nil, e
			}
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			response := updateResponseFromOperation(op)
			if in.ManagedOperationID != "" {
				response.DurableOwnerAck = &commandv1.DurableOwnerAck{OperationId: op.ID.String(), ResourceId: op.ServiceID.String(), Accepted: true}
			}
			return response, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
	}
	if err := q.LockManagedGPUKey(ctx, in.TenantID+":"+in.ServiceID); err != nil {
		return nil, err
	}
	current, err := q.GetServiceForUpdate(ctx, GetServiceForUpdateParams{TenantID: tenant, ID: serviceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, inferencebiz.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.DesiredGeneration != in.ExpectedGeneration || current.DeletedAt.Valid {
		return nil, inferencebiz.ErrGenerationConflict
	}
	// A stopped service has no runtime operation to replace. Refusing this
	// mixed command keeps update from accidentally reserving quota and
	// publishing a service whose desired state is stopped; callers can update
	// after Start, or use a future stopped-spec-only operation explicitly.
	if current.DesiredState != "running" {
		return nil, inferencebiz.ErrInvalidState
	}
	managedResource, managedErr := q.GetManagedGPUResource(ctx, GetManagedGPUResourceParams{TenantID: tenant, ResourceID: serviceID})
	managed := managedErr == nil
	if managedErr != nil && !errors.Is(managedErr, pgx.ErrNoRows) {
		return nil, managedErr
	}
	if in.ManagedOperationID != "" && !managed {
		return nil, fmt.Errorf("%w: managed GPU resource is missing", inferencebiz.ErrInvalidState)
	}
	if managed && managedResource.Closing {
		return nil, fmt.Errorf("%w: managed resource has a closing intent", inferencebiz.ErrInvalidState)
	}
	var frozen *InferenceSpec
	if len(in.UpdateMask) > 0 {
		selected := map[string]bool{}
		for _, p := range in.UpdateMask {
			if selected[p] || (p != "model_version_id" && p != "served_model_name" && p != "engine") {
				return nil, inferencebiz.ErrInvalidState
			}
			selected[p] = true
		}
		if selected["model_version_id"] && in.ModelVersionID == "" || selected["served_model_name"] && in.ServedModelName == "" || selected["engine"] && (in.EngineCommand == "" || in.ImageRef == "" || in.EngineRuntime == "") {
			return nil, inferencebiz.ErrInvalidState
		}
		if !selected["model_version_id"] {
			in.ModelVersionID = ""
			modelVersionID = pgtype.UUID{}
			in.ArtifactProvider = ""
			in.ArtifactRef = ""
			in.ArtifactSHA256 = ""
			in.ModelCapabilities = nil
		}
		if !selected["served_model_name"] {
			in.ServedModelName = ""
		}
		if !selected["engine"] {
			in.ImageRef = ""
			in.EngineRuntime = ""
			in.EngineCommand = ""
			engineJSON = nil
		}
		spec, e := q.GetSpec(ctx, GetSpecParams{TenantID: tenant, ServiceID: serviceID, Generation: current.DesiredGeneration})
		if e != nil {
			return nil, e
		}
		frozen = &spec
		in.Replicas, in.WorkerReplicas, in.RuntimeMode, in.RuntimeProvider = spec.Replicas, spec.WorkerReplicas, spec.RuntimeMode, spec.RuntimeProvider
		in.Endpoint = endpointInputFromSpec(spec)
		var res struct {
			Requests map[string]string `json:"requests"`
			Limits   map[string]string `json:"limits"`
			GPU      *gpubiz.Request   `json:"gpu"`
		}
		if e := json.Unmarshal(spec.Resources, &res); e != nil {
			return nil, e
		}
		in.Resources.Requests, in.Resources.Limits, in.Resources.GPU = res.Requests, res.Limits, res.GPU
		gpuPlanJSON, gpuSnapshotDigest = spec.GpuPlan, spec.GpuPlanDigest.String
	} else if managed {
		// Managed resources are only changed through the explicit partial contract.
		return nil, fmt.Errorf("%w: managed update_mask is required", inferencebiz.ErrInvalidState)
	}
	resourcesJSON, err := json.Marshal(struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
		GPU      *gpubiz.Request   `json:"gpu,omitempty"`
	}{in.Resources.Requests, in.Resources.Limits, in.Resources.GPU})
	if err != nil {
		return nil, err
	}
	gpuRequestJSON, err := json.Marshal(in.Resources.GPU)
	if in.Resources.GPU == nil {
		gpuRequestJSON = nil
	}
	if err != nil {
		return nil, err
	}
	if frozen != nil {
		resourcesJSON = frozen.Resources
		gpuRequestJSON = frozen.GpuRequest
	}
	// Partial updates inherit the entire frozen resource and runtime snapshot.
	target := current.DesiredGeneration + 1
	workers := in.WorkerReplicas
	if workers < 1 {
		workers = 1
	}
	mode := in.RuntimeMode
	if mode == "" {
		mode = "deployment"
	}
	provider := in.RuntimeProvider
	ec, es, et, ep := endpointValues(in.Endpoint)
	var modelCapabilitiesJSON []byte
	specOverlay := map[string]any{}
	if in.ModelVersionID != "" {
		specOverlay["model_capabilities"] = in.ModelCapabilities
	}
	if in.ImageSelection != nil {
		specOverlay["image_selection"] = in.ImageSelection
	} else if in.ImageRef != "" {
		specOverlay["image_selection"] = nil
	}
	if len(specOverlay) != 0 {
		modelCapabilitiesJSON, err = json.Marshal(specOverlay)
		if err != nil {
			return nil, err
		}
	}
	rows, err := q.CloneSpecWithRuntimeUpdate(ctx, CloneSpecWithRuntimeUpdateParams{ID: pgUUID(uuid.New()), TargetGeneration: target, ModelVersionID: modelVersionID, ModelCapabilities: modelCapabilitiesJSON, Resources: resourcesJSON, Replicas: in.Replicas, RuntimeMode: mode, WorkerReplicas: workers, RuntimeProvider: provider, ArtifactProvider: in.ArtifactProvider, ArtifactRef: in.ArtifactRef, ArtifactSha256: in.ArtifactSHA256, ImageRef: in.ImageRef, ServedModelName: in.ServedModelName, EngineRuntime: in.EngineRuntime, CommandArgv: engineJSON, EndpointContainerPort: ec, EndpointServicePort: es, EndpointTargetPort: et, EndpointProtocol: ep, GpuRequest: gpuRequestJSON, GpuPlan: gpuPlanJSON, GpuPlanDigest: pgtype.Text{String: gpuSnapshotDigest, Valid: gpuSnapshotDigest != ""}, TenantID: tenant, ServiceID: serviceID, SourceGeneration: current.DesiredGeneration})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, fmt.Errorf("%w: missing current specification", inferencebiz.ErrInvalidState)
	}
	rows, err = q.TransitionService(ctx, TransitionServiceParams{DesiredState: current.DesiredState, TargetGeneration: target, OperationID: opID, TenantID: tenant, ServiceID: serviceID, ExpectedGeneration: current.DesiredGeneration})
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, inferencebiz.ErrGenerationConflict
	}
	if err := q.InsertOperation(ctx, InsertOperationParams{TenantID: tenant, ID: opID, ServiceID: serviceID, Kind: "update", Phase: "pending", Step: "withdraw_publication", TargetGeneration: target, RequestHash: in.RequestHash}); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, inferencebiz.ErrOperationActive
		}
		return nil, err
	}
	if !managed {
		if err := q.InsertQuotaReservation(ctx, InsertQuotaReservationParams{TenantID: tenant, ServiceID: serviceID, OperationID: opID, Generation: target, ReservationID: "pending-" + opID.String(), RequestedResources: resourcesJSON}); err != nil {
			return nil, err
		}
	}
	if err := q.UpsertResourceWork(ctx, UpsertResourceWorkParams{TenantID: tenant, ServiceID: serviceID, DirtyVersion: target}); err != nil {
		return nil, err
	}
	if in.RequestID != "" {
		if _, err := q.PutIdempotency(ctx, PutIdempotencyParams{TenantID: tenant, Method: method, IdempotencyKey: in.RequestID, PayloadHash: in.RequestHash, OperationID: opID}); err != nil {
			return nil, err
		}
	}
	payload, _ := json.Marshal(map[string]any{"kind": "update", "desired_state": current.DesiredState})
	if err := q.AppendAuditEvent(ctx, AppendAuditEventParams{TenantID: tenant, EventID: pgUUID(uuid.New()), ServiceID: serviceID, OperationID: opID, Generation: target, EventType: "inference.update.accepted", Actor: in.Actor, RequestID: in.RequestID, Payload: payload}); err != nil {
		return nil, err
	}
	response := managedUpdateResponse(serviceID.String(), opID.String(), target, in.ManagedOperationID != "")
	snapshot, err := protojson.Marshal(response)
	if err != nil {
		return nil, err
	}
	if rows, err := q.SaveOperationAcceptanceResponse(ctx, SaveOperationAcceptanceResponseParams{TenantID: tenant, OperationID: opID, Response: snapshot}); err != nil {
		return nil, err
	} else if rows != 1 {
		return nil, inferencebiz.ErrGenerationConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return response, nil
}

func managedUpdateResponse(serviceID, operationID string, generation int64, managed bool) *inferencev1.OperationResponse {
	response := commandResponse(serviceID, operationID, generation, "update", "withdraw_publication")
	if managed {
		response.DurableOwnerAck = &commandv1.DurableOwnerAck{OperationId: operationID, ResourceId: serviceID, Accepted: true}
	}
	return response
}

func managedUpdateResponseFromOperation(op InferenceOperation) *inferencev1.OperationResponse {
	response := updateResponseFromOperation(op)
	response.DurableOwnerAck = &commandv1.DurableOwnerAck{OperationId: op.ID.String(), ResourceId: op.ServiceID.String(), Accepted: true}
	return response
}

func endpointInputFromSpec(s InferenceSpec) *inferencebiz.EndpointSpec {
	if !s.EndpointContainerPort.Valid && !s.EndpointServicePort.Valid && !s.EndpointTargetPort.Valid && !s.EndpointProtocol.Valid {
		return nil
	}
	return &inferencebiz.EndpointSpec{ContainerPort: s.EndpointContainerPort.Int32, ServicePort: s.EndpointServicePort.Int32, TargetPort: s.EndpointTargetPort.String, Protocol: s.EndpointProtocol.String}
}

func (u *UpdateUseCase) PrepareUpdate(ctx context.Context, in inferencebiz.UpdateInput) (*inferencev1.OperationResponse, bool, error) {
	if u == nil || u.repo == nil || u.repo.pool == nil {
		return nil, false, errors.New("nil postgres update repository")
	}
	tenant, e := parseUUID("tenant_id", in.TenantID, false)
	if e != nil {
		return nil, false, e
	}
	q := New(u.repo.pool)
	if in.ManagedOperationID != "" {
		id, e := parseUUID("operation_id", in.ManagedOperationID, false)
		if e != nil {
			return nil, false, e
		}
		row, e := q.GetOperation(ctx, GetOperationParams{TenantID: tenant, ID: id})
		if e == nil {
			if row.RequestHash != in.RequestHash {
				return nil, false, inferencebiz.ErrIdempotencyConflict
			}
			return managedUpdateResponseFromOperation(row), true, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, false, e
		}
	} else if in.RequestID != "" {
		row, e := q.GetIdempotency(ctx, GetIdempotencyParams{TenantID: tenant, Method: "InferenceService/update", IdempotencyKey: in.RequestID})
		if e == nil {
			if row.PayloadHash != in.RequestHash {
				return nil, false, inferencebiz.ErrIdempotencyConflict
			}
			op, e := q.GetOperation(ctx, GetOperationParams{TenantID: tenant, ID: row.OperationID})
			if e != nil {
				return nil, false, e
			}
			return updateResponseFromOperation(op), true, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, false, e
		}
	}
	return nil, false, nil
}

func updateResponseFromOperation(op InferenceOperation) *inferencev1.OperationResponse {
	if len(op.ResultSnapshot) > 0 {
		response := new(inferencev1.OperationResponse)
		if protojson.Unmarshal(op.ResultSnapshot, response) == nil && response.GetOperation().GetId() == op.ID.String() && response.GetResource().GetId() == op.ServiceID.String() {
			return response
		}
	}
	return commandResponseFromRow(op)
}
