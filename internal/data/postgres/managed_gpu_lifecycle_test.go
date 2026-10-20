package postgres

import (
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/protobuf/proto"
)

// DELETE keeps the original CREATE identity while carrying a distinct
// operation id. This is the durable association used by release reporting.
func TestPostgresManagedGPUDeletePreservesAcceptedCreateAndRejectsTopologyMutation(t *testing.T) {
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	req := managedCreateFixture(t, tenant, resource)
	deleteReq := managedDeleteFixture(t, req)
	if deleteReq.CreateOperationId != req.Context.OperationId || deleteReq.Context.OperationId == req.Context.OperationId {
		t.Fatalf("delete/create identity association changed: create=%s delete=%s", req.Context.OperationId, deleteReq.Context.OperationId)
	}
	server := service.NewInferenceServerWithCommands(NewCreateUseCase(NewRepository(pool)), nil, NewCommandUseCase(NewRepository(pool)))
	server.SetManagedGPUClusterID(req.ClusterId)
	if _, err := server.DeleteInferenceService(ctx, deleteReq); err != nil {
		t.Fatal(err)
	}
	// A topology mutation cannot be hidden in a new request: GPU replicas and
	// the frozen execution are validated together before persistence.
	bad := proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest)
	bad.Resource.Gpu.Replicas = 2
	if err := gpu.ValidateTopology(&gpu.Request{ClusterID: bad.ClusterId, PoolID: bad.Resource.Gpu.PoolId, ProfileID: bad.Resource.Gpu.ProfileId, ProfileVersion: uint64(bad.Resource.Gpu.ProfileVersion), Replicas: uint32(bad.Resource.Gpu.Replicas), DevicesPerReplica: uint32(bad.Resource.Gpu.DevicesPerReplica), ContainerName: bad.Resource.Gpu.ContainerName}, bad.Replicas, "deployment", 0); err != nil {
		// This is the expected rejection: the frozen ACC request no longer
		// matches the service replica count.
		return
	}
	t.Fatal("topology mutation accepted")
}

func TestPostgresManagedGPUAcceptanceRollbackLeavesNoACK(t *testing.T) {
	ctx, pool, tenant, resource := lifecycleDatabase(t)
	ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
	req := managedCreateFixture(t, tenant, resource)
	req.Context.RequestDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	server := managedFixtureServer(pool, req.ClusterId)
	if _, err := server.CreateInferenceService(ctx, req); err == nil {
		t.Fatal("forged command accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_managed_gpu_commands WHERE tenant_id=$1`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled back command left durable ACK: %d", count)
	}
}
