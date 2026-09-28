package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/gpu"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

type fakeCreateRepository struct{ in CreateAggregateInput }

func (f *fakeCreateRepository) CreateService(_ context.Context, in CreateAggregateInput) (CreateAggregateResult, error) {
	f.in = in
	return CreateAggregateResult{ServiceID: in.ServiceID, OperationID: in.OperationID}, nil
}

func TestCreateUseCasePersistsGpuRequestSnapshot(t *testing.T) {
	repo := &fakeCreateRepository{}
	request := &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000001", PoolID: "10000000-0000-4000-8000-000000000002", ProfileID: "10000000-0000-4000-8000-000000000003", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: "kserve-container"}
	if _, err := NewCreateUseCase(repo).Create(context.Background(), inference.CreateInput{TenantID: "tenant", RequestID: "gpu-req", Name: "gpu", ModelVersionID: "version", Replicas: 1, Resources: resources.Normalized{GPU: request}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.in.GPURequest) == 0 || !strings.Contains(string(repo.in.Resources), `"gpu"`) {
		t.Fatalf("GPU snapshot missing: request=%s resources=%s", repo.in.GPURequest, repo.in.Resources)
	}
}

func TestCreateUseCaseMapsDurableCommand(t *testing.T) {
	repo := &fakeCreateRepository{}
	uc := NewCreateUseCase(repo)
	out, err := uc.Create(context.Background(), inference.CreateInput{
		TenantID: "tenant", RequestID: "req-1", Name: "svc", ModelVersionID: "model-version", ServedModelName: "resnet", Replicas: 1, RequestHash: "hash",
		Resources: resources.Normalized{Requests: map[string]string{"cpu": "2"}, Limits: map[string]string{"cpu": "4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetOperation().GetPhase() != "pending" || repo.in.TenantID != "tenant" || repo.in.IdempotencyKey != "req-1" || repo.in.ServedModelName != "resnet" {
		t.Fatalf("unexpected mapping: %+v", repo.in)
	}
	if string(repo.in.Resources) != `{"requests":{"cpu":"2"},"limits":{"cpu":"4"}}` {
		t.Fatalf("resources json=%s", repo.in.Resources)
	}
}
