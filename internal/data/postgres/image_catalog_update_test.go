package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	biz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	imagedata "github.com/liangzai006/ani-inference-service/internal/data/image"
	"github.com/liangzai006/ani-inference-service/internal/service"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// Only Resource's external resolution is substituted. Transport command
// validation, digest checks, tenant binding, PG acceptance and replay are real.
type updateCatalogBoundary struct {
	imagev1.ImageRuntimeServiceClient
	tenant  string
	digests map[string]string
	calls   int
	err     error
}

func (b *updateCatalogBoundary) ResolveImageForWorkload(_ context.Context, req *imagev1.ResolveImageForWorkloadRequest, _ ...grpc.CallOption) (*imagev1.ResolveImageForWorkloadResponse, error) {
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	if req.GetTenantId() != b.tenant {
		return nil, status.Error(codes.PermissionDenied, "foreign tenant")
	}
	digest, ok := b.digests[req.GetImageId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown image")
	}
	return &imagev1.ResolveImageForWorkloadResponse{TenantId: req.GetTenantId(), ImageId: req.GetImageId(), Scope: req.GetScope(), ResolvedReference: "private.registry/engine@" + digest, Digest: digest, SelectedPlatform: req.GetTargetPlatform()}, nil
}

func TestCatalogImagePostgresUpdateFreezesNewGeneration(t *testing.T) {
	for _, gpuResource := range []bool{false, true} {
		name := "cpu"
		if gpuResource {
			name = "gpu"
		}
		t.Run(name, func(t *testing.T) {
			ctx, pool, tenant, resource := lifecycleDatabase(t)
			ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
			create := managedCreateFixture(t, tenant, resource)
			if !gpuResource {
				create.Resource.Gpu = nil
				create.ClusterId = ""
				create.GpuExecution = nil
				create.GpuDisplay = nil
			}
			firstID, nextID := "img_"+strings.ReplaceAll(uuid.NewString(), "-", ""), "img_"+strings.ReplaceAll(uuid.NewString(), "-", "")
			firstDigest, nextDigest := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
			create.ContractVersion = 2
			create.Engine.Image = ""
			create.Engine.ImageId = firstID
			create.Engine.ImageScope = "platform"
			refreshManagedFixtureDigests(t, create)
			boundary := &updateCatalogBoundary{tenant: tenant.String(), digests: map[string]string{firstID: firstDigest, nextID: nextDigest}}
			newServer := func() *service.InferenceServer {
				repo := NewRepository(pool)
				durableCreate := NewCreateUseCase(repo)
				durableUpdate := NewUpdateUseCase(repo)
				client := &imagedata.Client{Runtime: boundary, Platform: &imagev1.ImagePlatform{Os: "linux", Architecture: "amd64"}, Namespace: "runtime", RequireCatalog: true}
				server := service.NewInferenceServerWithAll(&imagedata.CreateUseCase{Client: client, Next: durableCreate, Preflight: durableCreate}, NewReadUseCase(pool), NewCommandUseCase(repo), &imagedata.UpdateUseCase{Client: client, Next: durableUpdate, Preflight: durableUpdate})
				server.SetManagedGPUClusterID(create.ClusterId)
				return server
			}
			if _, err := newServer().CreateInferenceService(ctx, create); err != nil {
				t.Fatal(err)
			}
			readSpec := func(generation int64) (InferenceSpec, *biz.ImageSelection) {
				t.Helper()
				row, err := New(pool).GetSpec(ctx, GetSpecParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(resource), Generation: generation})
				if err != nil {
					t.Fatal(err)
				}
				var saved struct {
					Image *biz.ImageSelection `json:"image_selection"`
				}
				if err := json.Unmarshal(row.SpecJson, &saved); err != nil || saved.Image == nil {
					t.Fatalf("generation %d has no accepted image: %v", generation, err)
				}
				return row, saved.Image
			}
			first, firstSelection := readSpec(1)
			if first.ImageRef != "private.registry/engine@"+firstDigest || firstSelection.ImageID != firstID || boundary.calls != 1 {
				t.Fatal("catalog create not frozen")
			}
			// This fixture represents the external workload's completion only;
			// it does not claim a live Pod or inference response was observed.
			complete := func(operation string) {
				t.Helper()
				if _, err := pool.Exec(ctx, "UPDATE inference_operations SET phase='succeeded',step='complete',completed_at=now() WHERE tenant_id=$1 AND id=$2", tenant, uuid.MustParse(operation)); err != nil {
					t.Fatal(err)
				}
			}
			complete(create.Context.OperationId)
			update := &inferencev1.UpdateInferenceServiceRequest{ContractVersion: 2, RequestId: uuid.NewString(), ResourceId: resource.String(), ExpectedGeneration: 1,
				Engine: &inferencev1.EngineSpec{Type: "vllm", Command: "exec engine serve /models --updated", ImageId: nextID, ImageScope: "tenant"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"engine"}},
				Context: &inferencev1.CommandContext{TenantId: tenant.String(), ResourceId: resource.String(), OperationId: uuid.NewString()}}
			sign := func(req *inferencev1.UpdateInferenceServiceRequest) {
				t.Helper()
				digest, err := contractv1.ManagedUpdateCommandHash(req, tenant.String(), "user", "42")
				if err != nil {
					t.Fatal(err)
				}
				req.Context.RequestDigest = digest
			}
			sign(update)
			accepted, err := newServer().UpdateInferenceService(ctx, update)
			if err != nil || !accepted.GetDurableOwnerAck().GetAccepted() || accepted.GetOperation().GetTargetGeneration() != 2 {
				t.Fatalf("catalog update acceptance: %v %v", accepted, err)
			}
			second, secondSelection := readSpec(2)
			if boundary.calls != 2 || second.ImageRef != "private.registry/engine@"+nextDigest || secondSelection.ImageID != nextID || secondSelection.Scope != "tenant" || secondSelection.PullSecretRef != biz.PullSecretName(tenant.String(), "runtime", "private.registry") {
				t.Fatal("updated image not frozen into generation 2")
			}
			command, err := unmarshalEngineSpec(second.CommandArgv)
			if err != nil || command != update.Engine.Command {
				t.Fatal("complete updated engine command was lost", err)
			}
			if again, selection := readSpec(1); again.ImageRef != first.ImageRef || selection.ImageID != firstSelection.ImageID {
				t.Fatal("image update changed historical generation")
			}
			boundary.err = status.Error(codes.Unavailable, "Resource offline")
			replayRequest := proto.Clone(update).(*inferencev1.UpdateInferenceServiceRequest)
			replayRequest.RequestId = uuid.NewString()
			replay, err := newServer().UpdateInferenceService(ctx, replayRequest)
			if err != nil || !proto.Equal(accepted, replay) || boundary.calls != 2 {
				t.Fatalf("accepted update re-resolved after service reconstruction: %v", err)
			}
			changed := proto.Clone(update).(*inferencev1.UpdateInferenceServiceRequest)
			changed.Engine.ImageId = "img_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			sign(changed)
			if _, err := newServer().UpdateInferenceService(ctx, changed); status.Code(err) != codes.AlreadyExists || boundary.calls != 2 {
				t.Fatalf("same operation changed selection without conflict: %v", err)
			}
			foreign := service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(context.Background()), uuid.NewString()), "user:42")
			if _, err := newServer().UpdateInferenceService(foreign, update); status.Code(err) != codes.PermissionDenied || boundary.calls != 2 {
				t.Fatalf("cross-tenant update reached Resource: %v", err)
			}
			complete(update.Context.OperationId)
			rename := &inferencev1.UpdateInferenceServiceRequest{ContractVersion: 2, RequestId: uuid.NewString(), ResourceId: resource.String(), ExpectedGeneration: 2, ServedModelName: "renamed-model", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"served_model_name"}}, Context: &inferencev1.CommandContext{TenantId: tenant.String(), ResourceId: resource.String(), OperationId: uuid.NewString()}}
			sign(rename)
			if response, err := newServer().UpdateInferenceService(ctx, rename); err != nil || response.GetOperation().GetTargetGeneration() != 3 {
				t.Fatalf("non-image update failed with Resource offline: %v", err)
			}
			third, thirdSelection := readSpec(3)
			if boundary.calls != 2 || third.ImageRef != second.ImageRef || *thirdSelection != *secondSelection || third.ServedModelName != "renamed-model" || string(third.CommandArgv) != string(second.CommandArgv) {
				t.Fatal("non-image update changed accepted image or engine")
			}
		})
	}
}
