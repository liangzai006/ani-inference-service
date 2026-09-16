package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ReadUseCase exposes PostgreSQL projections without advancing any workflow.
// All lookups are tenant-scoped before they reach the generated query.
type ReadUseCase struct{ pool DBTX }

func NewReadUseCase(pool DBTX) *ReadUseCase { return &ReadUseCase{pool: pool} }

const (
	defaultListPageSize int32 = 50
	maxListPageSize     int32 = 100
)

type listCursor struct {
	TenantID  string `json:"tenant_id"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

// listTokenKey is only used to detect malformed or modified cursors. Tenant
// binding and the database predicate remain the authorization boundary.
func listTokenKey(tenantID string) []byte {
	sum := sha256.Sum256([]byte("ani-inference-list-token-v1:" + tenantID))
	return sum[:]
}

func encodeListToken(c listCursor) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, listTokenKey(c.TenantID))
	mac.Write(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded, nil
}

func decodeListToken(token, tenantID string) (listCursor, error) {
	var zero listCursor
	parts := make([]string, 0, 2)
	for len(parts) < 2 {
		i := -1
		for j, r := range token {
			if r == '.' {
				i = j
				break
			}
		}
		if i < 0 {
			break
		}
		parts = append(parts, token[:i])
		token = token[i+1:]
	}
	if len(parts) != 1 || token == "" {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	parts = append(parts, token)
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	mac := hmac.New(sha256.New, listTokenKey(tenantID))
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	var c listCursor
	if err := json.Unmarshal(payload, &c); err != nil || c.TenantID != tenantID {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	if _, err := uuid.Parse(c.ID); err != nil {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	if _, err := time.Parse(time.RFC3339Nano, c.CreatedAt); err != nil {
		return zero, inferencebiz.ErrInvalidPageToken
	}
	return c, nil
}

var _ inferencebiz.ReadUseCase = (*ReadUseCase)(nil)

func (u *ReadUseCase) GetService(ctx context.Context, tenantID, serviceID string) (*inferencev1.InferenceService, error) {
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, err
	}
	service, err := workUUID("service_id", serviceID)
	if err != nil {
		return nil, err
	}
	if u == nil || u.pool == nil {
		return nil, errors.New("nil postgres read use case")
	}
	q := New(u.pool)
	row, err := q.GetService(ctx, GetServiceParams{TenantID: tenant, ID: service})
	if err != nil {
		return nil, err
	}
	result := &inferencev1.InferenceService{Id: row.ID.String(), Name: row.Name, DesiredState: row.DesiredState, Generation: row.DesiredGeneration}
	result.AppliedGeneration = row.AppliedGeneration
	spec, e := q.GetSpec(ctx, GetSpecParams{TenantID: tenant, ServiceID: service, Generation: row.DesiredGeneration})
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e == nil {
		if err := applySpecProjection(result, spec.ModelVersionID.String(), spec.ArtifactProvider, spec.ArtifactRef, spec.ArtifactSha256, spec.ImageRef, spec.ServedModelName, spec.EngineRuntime, spec.CommandArgv); err != nil {
			return nil, err
		}
		result.Replicas = spec.Replicas
		result.Runtime = &inferencev1.RuntimeSpec{Mode: runtimeModeEnum(spec.RuntimeMode), WorkerReplicas: spec.WorkerReplicas}
		result.Runtime.Endpoint = endpointProto(spec.EndpointContainerPort, spec.EndpointServicePort, spec.EndpointTargetPort, spec.EndpointProtocol)
		var resource struct {
			Requests map[string]string `json:"requests"`
			Limits   map[string]string `json:"limits"`
		}
		if len(spec.Resources) > 0 {
			if err := json.Unmarshal(spec.Resources, &resource); err != nil {
				return nil, err
			}
		}
		result.Resource = &inferencev1.ResourceSpec{Requests: resource.Requests, Limits: resource.Limits}
	}
	runtime, e := q.GetRuntime(ctx, GetRuntimeParams{TenantID: tenant, ServiceID: service})
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return nil, e
	}
	if e == nil {
		result.RuntimePhase = runtime.RuntimePhase
		result.PublicationPhase = runtime.PublicationPhase
		result.InvocationHealth = runtime.InvocationHealth
		if runtime.ObservedAt.Valid {
			result.ObservedAt = timestamppb.New(runtime.ObservedAt.Time)
		}
	}
	return result, nil
}

func (u *ReadUseCase) ListServices(ctx context.Context, tenantID string, pageSize int32, pageToken string) ([]*inferencev1.InferenceService, string, error) {
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, "", err
	}
	if u == nil || u.pool == nil {
		return nil, "", errors.New("nil postgres read use case")
	}
	if pageSize <= 0 {
		pageSize = defaultListPageSize
	}
	if pageSize > maxListPageSize {
		return nil, "", fmt.Errorf("page_size must be between 1 and %d", maxListPageSize)
	}
	arg := ListServicesParams{TenantID: tenant, PageSize: pageSize + 1}
	if pageToken != "" {
		cursor, e := decodeListToken(pageToken, tenantID)
		if e != nil {
			return nil, "", e
		}
		created, e := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
		if e != nil {
			return nil, "", inferencebiz.ErrInvalidPageToken
		}
		cursorID, e := uuid.Parse(cursor.ID)
		if e != nil {
			return nil, "", inferencebiz.ErrInvalidPageToken
		}
		arg.CursorCreatedAt = pgtype.Timestamptz{Time: created, Valid: true}
		arg.CursorID = pgtype.UUID{Bytes: cursorID, Valid: true}
	}
	rows, err := New(u.pool).ListServices(ctx, arg)
	if err != nil {
		return nil, "", err
	}
	more := len(rows) > int(pageSize)
	if more {
		rows = rows[:pageSize]
	}
	result := make([]*inferencev1.InferenceService, 0, len(rows))
	for _, row := range rows {
		item := &inferencev1.InferenceService{
			Id: row.ID.String(), Name: row.Name, DesiredState: row.DesiredState,
			Generation: row.DesiredGeneration, AppliedGeneration: row.AppliedGeneration, ModelVersionId: row.ModelVersionID.String(),
			Replicas: row.Replicas, RuntimePhase: row.RuntimePhase,
			PublicationPhase: row.PublicationPhase, InvocationHealth: row.InvocationHealth,
			Runtime: &inferencev1.RuntimeSpec{Mode: runtimeModeEnum(row.RuntimeMode), WorkerReplicas: row.WorkerReplicas},
		}
		item.Runtime.Endpoint = endpointProto(row.EndpointContainerPort, row.EndpointServicePort, row.EndpointTargetPort, row.EndpointProtocol)
		if err := applySpecProjection(item, row.ModelVersionID.String(), row.ArtifactProvider, row.ArtifactRef, row.ArtifactSha256, row.ImageRef, row.ServedModelName, row.EngineRuntime, row.CommandArgv); err != nil {
			return nil, "", err
		}
		var resource struct {
			Requests map[string]string `json:"requests"`
			Limits   map[string]string `json:"limits"`
		}
		if len(row.Resources) != 0 {
			if err := json.Unmarshal(row.Resources, &resource); err != nil {
				return nil, "", err
			}
		}
		item.Resource = &inferencev1.ResourceSpec{Requests: resource.Requests, Limits: resource.Limits}
		if row.ObservedAt.Valid {
			item.ObservedAt = timestamppb.New(row.ObservedAt.Time)
		}
		result = append(result, item)
	}
	var next string
	if more && len(rows) > 0 {
		last := rows[len(rows)-1]
		next, err = encodeListToken(listCursor{TenantID: tenantID, CreatedAt: last.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ID: last.ID.String()})
		if err != nil {
			return nil, "", err
		}
	}
	return result, next, nil
}

func endpointProto(containerPort, servicePort pgtype.Int4, targetPort, protocol pgtype.Text) *inferencev1.EndpointSpec {
	if !containerPort.Valid && !servicePort.Valid && !targetPort.Valid && !protocol.Valid {
		return nil
	}
	return &inferencev1.EndpointSpec{ContainerPort: containerPort.Int32, ServicePort: servicePort.Int32, TargetPort: targetPort.String, Protocol: protocol.String}
}

func applySpecProjection(result *inferencev1.InferenceService, modelVersionID, provider, reference, sha256, image, servedModel, engine string, commandJSON []byte) error {
	result.ModelVersionId = modelVersionID
	result.ServedModelName = servedModel
	result.ModelArtifact = &inferencev1.ModelArtifact{Provider: provider, Reference: reference, Sha256: sha256}
	command := []string{}
	if len(commandJSON) != 0 {
		if err := json.Unmarshal(commandJSON, &command); err != nil {
			return fmt.Errorf("decode command argv: %w", err)
		}
	}
	result.Engine = &inferencev1.EngineSpec{Type: engine, Image: image, Command: command}
	return nil
}

func (u *ReadUseCase) GetOperation(ctx context.Context, tenantID, operationID string) (*inferencev1.Operation, error) {
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, err
	}
	op, err := workUUID("operation_id", operationID)
	if err != nil {
		return nil, err
	}
	if u == nil || u.pool == nil {
		return nil, errors.New("nil postgres read use case")
	}
	row, err := New(u.pool).GetOperation(ctx, GetOperationParams{TenantID: tenant, ID: op})
	if err != nil {
		return nil, err
	}
	return operationProto(row), nil
}

func operationProto(row InferenceOperation) *inferencev1.Operation {
	result := &inferencev1.Operation{Id: row.ID.String(), ServiceId: row.ServiceID.String(), Kind: row.Kind, Phase: row.Phase, Step: row.Step, TargetGeneration: row.TargetGeneration, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage}
	if row.CreatedAt.Valid {
		result.CreatedAt = timestamppb.New(row.CreatedAt.Time)
	}
	if row.CompletedAt.Valid {
		result.CompletedAt = timestamppb.New(row.CompletedAt.Time)
	}
	return result
}

func (u *ReadUseCase) ListOperations(ctx context.Context, tenantID, serviceID string, pageSize int32, pageToken string) ([]*inferencev1.Operation, string, error) {
	tenant, err := tenantUUID(tenantID)
	if err != nil {
		return nil, "", err
	}
	if u == nil || u.pool == nil {
		return nil, "", errors.New("nil postgres read use case")
	}
	if pageSize <= 0 {
		pageSize = defaultListPageSize
	}
	if pageSize > maxListPageSize {
		return nil, "", fmt.Errorf("page_size must be between 1 and %d", maxListPageSize)
	}
	arg := ListOperationsParams{TenantID: tenant, PageSize: pageSize + 1}
	if serviceID != "" {
		id, parseErr := workUUID("resource_id", serviceID)
		if parseErr != nil {
			return nil, "", parseErr
		}
		arg.ServiceID = id
	}
	if pageToken != "" {
		cursor, parseErr := decodeListToken(pageToken, tenantID)
		if parseErr != nil {
			return nil, "", parseErr
		}
		created, parseErr := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
		if parseErr != nil {
			return nil, "", inferencebiz.ErrInvalidPageToken
		}
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			return nil, "", inferencebiz.ErrInvalidPageToken
		}
		arg.CursorCreatedAt = pgtype.Timestamptz{Time: created, Valid: true}
		arg.CursorID = pgtype.UUID{Bytes: cursorID, Valid: true}
	}
	rows, err := New(u.pool).ListOperations(ctx, arg)
	if err != nil {
		return nil, "", err
	}
	more := len(rows) > int(pageSize)
	if more {
		rows = rows[:pageSize]
	}
	result := make([]*inferencev1.Operation, 0, len(rows))
	for _, row := range rows {
		result = append(result, operationProto(row))
	}
	var next string
	if more {
		last := rows[len(rows)-1]
		next, err = encodeListToken(listCursor{TenantID: tenantID, CreatedAt: last.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ID: last.ID.String()})
		if err != nil {
			return nil, "", err
		}
	}
	return result, next, nil
}
