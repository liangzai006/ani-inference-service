package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Bound live streams independently of transport connection limits.
var runtimeLogStreams = make(chan struct{}, 32)

type DiagnosticsServer struct {
	inferencev1.UnimplementedInferenceDiagnosticsServer
	Read    inferencebiz.ReadUseCase
	Runtime biz.Reader
	probes  biz.ProbeCache
}

func (s *DiagnosticsServer) scope(ctx context.Context, id string) (biz.Scope, *inferencev1.InferenceService, error) {
	tenant, err := RequireTenant(ctx, "")
	if err != nil {
		return biz.Scope{}, nil, status.Error(codes.Unauthenticated, err.Error())
	}
	if strings.TrimSpace(id) == "" {
		return biz.Scope{}, nil, status.Error(codes.InvalidArgument, "resource_id is required")
	}
	if s.Read == nil {
		return biz.Scope{}, nil, status.Error(codes.Unavailable, "inference reader unavailable")
	}
	resource, err := s.Read.GetService(ctx, tenant, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return biz.Scope{}, nil, status.Error(codes.NotFound, "inference service not found")
	}
	if err != nil {
		return biz.Scope{}, nil, status.Error(codes.Internal, "read inference service failed")
	}
	if resource == nil {
		return biz.Scope{}, nil, status.Error(codes.NotFound, "inference service not found")
	}
	return biz.Scope{TenantID: tenant, ServiceID: id, Generation: resource.Generation}, resource, nil
}
func diagnosticsError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "diagnostics canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "diagnostics deadline exceeded")
	case errors.Is(err, biz.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, biz.ErrLimit):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Unavailable, "runtime diagnostics source unavailable")
	}
}
func (s *DiagnosticsServer) GetRuntimeDiagnostics(ctx context.Context, req *inferencev1.GetRuntimeDiagnosticsRequest) (*inferencev1.RuntimeDiagnostics, error) {
	scope, resource, err := s.scope(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	if s.Runtime == nil {
		return nil, diagnosticsError(biz.ErrUnavailable)
	}
	instances, err := s.Runtime.Instances(ctx, scope)
	if err != nil {
		return nil, diagnosticsError(err)
	}
	health, reason := invocationHealthExplanation(resource)
	result := &inferencev1.RuntimeDiagnostics{InvocationHealth: health, HealthReason: reason, RuntimeObservedAt: resource.ObservedAt, ObservedAt: timestamppb.Now()}
	if prober, ok := s.Runtime.(biz.InvocationProber); ok && resource.DesiredState == "running" && resource.PublicationPhase == "published" {
		observed, err := s.probes.Observe(ctx, prober, scope, resource.ServedModelName)
		if err != nil {
			return nil, diagnosticsError(err)
		}
		result.InvocationHealth, result.HealthReason = observed.State, observed.Reason
		if !observed.ObservedAt.IsZero() {
			result.HealthObservedAt = timestamppb.New(observed.ObservedAt)
		}
	}

	for _, instance := range instances {
		result.Instances = append(result.Instances, &inferencev1.RuntimeInstance{Name: instance.Name, Uid: instance.UID, Phase: instance.Phase, Containers: instance.Containers})
	}
	return result, nil
}

// Runtime readiness and observation time are not invocation probe evidence.
func invocationHealthExplanation(resource *inferencev1.InferenceService) (string, string) {
	health := resource.GetInvocationHealth()
	if health == "healthy" || health == "unhealthy" {
		return health, "recorded_health_without_probe_timestamp"
	}
	switch {
	case resource.GetDesiredState() == "stopped":
		return "unknown", "service_stopped"
	case resource.GetDesiredState() == "deleted":
		return "unknown", "service_deleted"
	case resource.GetObservedAt() == nil:
		return "unknown", "runtime_not_observed"
	case resource.GetPublicationPhase() != "published":
		return "unknown", "endpoint_not_published"
	default:
		return "unknown", "invocation_probe_not_configured"
	}
}
func timestampValue(ts *timestamppb.Timestamp) (time.Time, error) {
	if ts == nil {
		return time.Time{}, nil
	}
	if err := ts.CheckValid(); err != nil {
		return time.Time{}, status.Error(codes.InvalidArgument, "invalid timestamp")
	}
	return ts.AsTime(), nil
}
func diagnosticsTimeRange(since, until *timestamppb.Timestamp) (time.Time, time.Time, error) {
	start, err := timestampValue(since)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := timestampValue(until)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !start.IsZero() && !end.IsZero() && !end.After(start) {
		return time.Time{}, time.Time{}, status.Error(codes.InvalidArgument, "until must be after since")
	}
	return start, end, nil
}
func logQuery(req *inferencev1.RuntimeLogsRequest, follow bool) (biz.LogsQuery, error) {
	since, until, err := diagnosticsTimeRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return biz.LogsQuery{}, err
	}
	if req.GetInstance() == "" {
		return biz.LogsQuery{}, status.Error(codes.InvalidArgument, "instance is required")
	}
	tail := req.GetTailLines()
	if tail == 0 {
		tail = 200
	}
	if tail < 1 || tail > 2000 {
		return biz.LogsQuery{}, status.Error(codes.InvalidArgument, "tail_lines must be between 1 and 2000")
	}
	return biz.LogsQuery{Instance: req.GetInstance(), Container: req.GetContainer(), Since: since, Until: until, TailLines: tail, Follow: follow}, nil
}
func protoLog(line biz.LogLine) *inferencev1.RuntimeLogLine {
	result := &inferencev1.RuntimeLogLine{Message: line.Message}
	if !line.Timestamp.IsZero() {
		result.Timestamp = timestamppb.New(line.Timestamp)
	}
	return result
}
func (s *DiagnosticsServer) GetRuntimeLogs(ctx context.Context, req *inferencev1.RuntimeLogsRequest) (*inferencev1.RuntimeLogsResponse, error) {
	scope, _, err := s.scope(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	query, err := logQuery(req, false)
	if err != nil {
		return nil, err
	}
	if s.Runtime == nil {
		return nil, diagnosticsError(biz.ErrUnavailable)
	}
	result := &inferencev1.RuntimeLogsResponse{}
	result.Truncated, err = s.Runtime.Logs(ctx, scope, query, func(line biz.LogLine) error { result.Items = append(result.Items, protoLog(line)); return nil })
	if err != nil {
		return nil, diagnosticsError(err)
	}
	return result, nil
}
func (s *DiagnosticsServer) StreamRuntimeLogs(req *inferencev1.RuntimeLogsRequest, stream grpc.ServerStreamingServer[inferencev1.RuntimeLogLine]) error {
	ctx, cancel := context.WithTimeout(stream.Context(), 5*time.Minute)
	defer cancel()
	scope, _, err := s.scope(ctx, req.GetResourceId())
	if err != nil {
		return err
	}
	query, err := logQuery(req, true)
	if err != nil {
		return err
	}
	if !query.Until.IsZero() {
		var endCancel context.CancelFunc
		ctx, endCancel = context.WithDeadline(ctx, query.Until)
		defer endCancel()
	}
	select {
	case runtimeLogStreams <- struct{}{}:
		defer func() { <-runtimeLogStreams }()
	default:
		return status.Error(codes.ResourceExhausted, "too many runtime log streams")
	}

	if s.Runtime == nil {
		return diagnosticsError(biz.ErrUnavailable)
	}
	truncated, err := s.Runtime.Logs(ctx, scope, query, func(line biz.LogLine) error { return sendRuntimeLog(ctx, stream.Send, protoLog(line)) })
	if err != nil {
		return diagnosticsError(err)
	}
	if truncated {
		return diagnosticsError(biz.ErrLimit)
	}
	return nil
}
func (s *DiagnosticsServer) ListRuntimeEvents(ctx context.Context, req *inferencev1.ListRuntimeEventsRequest) (*inferencev1.ListRuntimeEventsResponse, error) {
	scope, _, err := s.scope(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	since, until, err := diagnosticsTimeRange(req.GetSince(), req.GetUntil())
	if err != nil {
		return nil, err
	}
	page, size := req.GetPage(), req.GetPageSize()
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 20
	}
	if page < 1 || size < 1 || size > 100 || page > 10000 {
		return nil, status.Error(codes.InvalidArgument, "invalid event pagination")
	}
	typ := req.GetType()
	if typ != "" && typ != "Normal" && typ != "Warning" {
		return nil, status.Error(codes.InvalidArgument, "type must be Normal or Warning")
	}
	if s.Runtime == nil {
		return nil, diagnosticsError(biz.ErrUnavailable)
	}
	items, total, err := s.Runtime.Events(ctx, scope, biz.EventsQuery{Since: since, Until: until, Type: typ, Offset: int(page-1) * int(size), Limit: int(size)})
	if err != nil {
		return nil, diagnosticsError(err)
	}
	result := &inferencev1.ListRuntimeEventsResponse{Total: total}
	for _, item := range items {
		event := &inferencev1.RuntimeEvent{Id: item.ID, Type: item.Type, Reason: item.Reason, Message: item.Message, Instance: item.Instance, Count: item.Count}
		if !item.Timestamp.IsZero() {
			event.Timestamp = timestamppb.New(item.Timestamp)
		}
		result.Items = append(result.Items, event)
	}
	return result, nil
}

// A stalled receiver must not outlive the diagnostics deadline. Returning from
// the handler closes the gRPC transport stream and releases its pending Send.
func sendRuntimeLog(ctx context.Context, send func(*inferencev1.RuntimeLogLine) error, line *inferencev1.RuntimeLogLine) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- send(line) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
