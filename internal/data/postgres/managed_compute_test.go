package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"github.com/liangzai006/ani-inference-service/internal/data/model"
	"github.com/liangzai006/ani-inference-service/internal/service"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Only Model/ACC/Gov RPC boundaries are doubles. Intake, locks, persistence,
// query projection, release validation and restarted scans use production code.
type unavailableModel struct {
	modelv1.ModelServiceClient
	calls int
}

func (m *unavailableModel) GetModelVersion(context.Context, *modelv1.GetModelVersionRequest, ...grpc.CallOption) (*modelv1.GetModelVersionResponse, error) {
	m.calls++
	return nil, status.Error(codes.Unavailable, "isolated Model boundary unavailable")
}

func (m *unavailableModel) AbortTrainedArtifact(context.Context, *modelv1.AbortTrainedArtifactRequest, ...grpc.CallOption) (*modelv1.AbortTrainedArtifactResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by this fixture")
}

type closeObserver struct {
	finding gpu.ReleaseFinding
	calls   []gpu.ReleaseObservationInput
}

func (o *closeObserver) ObserveRelease(_ context.Context, in gpu.ReleaseObservationInput) (gpu.ReleaseObservation, error) {
	o.calls = append(o.calls, in)
	return gpu.ReleaseObservation{Finding: o.finding, EvidenceID: "isolated-evidence", EvidenceDigest: "isolated-digest"}, nil
}

type closeReporter struct {
	calls       []gpu.ReleaseNotification
	unknownOnce bool
}

func (r *closeReporter) ReportResourceReleased(_ context.Context, in gpu.ReleaseNotification) error {
	r.calls = append(r.calls, in)
	if r.unknownOnce {
		r.unknownOnce = false
		return errors.New("Gov applied but owner did not receive completion")
	}
	return nil
}

func TestManagedComputeDeleteFirstPersistentRecovery(t *testing.T) {
	for _, gpuResource := range []bool{false, true} {
		for _, late := range []bool{false, true} {
			name := "cpu"
			if gpuResource {
				name = "gpu"
			}
			if late {
				name += "-late"
			} else {
				name += "-never"
			}
			t.Run(name, func(t *testing.T) {
				ctx, pool, tenant, resource := lifecycleDatabase(t)
				ctx = service.WithActor(service.WithTenantID(gpu.WithGovernanceIdentity(ctx), tenant.String()), "user:42")
				req := managedCreateFixture(t, tenant, resource)
				if !gpuResource {
					req.Resource.Gpu = nil
					req.ClusterId = ""
					req.GpuExecution = nil
					req.GpuDisplay = nil
					refreshManagedFixtureDigests(t, req)
				}
				boundary := &unavailableModel{}
				repo := NewRepository(pool)
				create := model.NewCreateUseCase(model.NewClient(boundary), NewCreateUseCase(repo))
				server := service.NewInferenceServerWithAll(create, NewReadUseCase(pool), NewCommandUseCase(repo), NewUpdateUseCase(repo))
				server.SetManagedGPUClusterID(req.ClusterId)
				if out, err := server.CreateInferenceService(ctx, req); status.Code(err) != codes.Unavailable || out != nil {
					t.Fatalf("initial Model failure out=%v err=%v", out, err)
				}
				var accepted int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM inference_managed_gpu_commands WHERE tenant_id=$1", tenant).Scan(&accepted); err != nil || accepted != 0 {
					t.Fatalf("failed Model fabricated acceptance=%d err=%v", accepted, err)
				}
				del := managedDeleteFixture(t, req)
				ack, err := server.DeleteInferenceService(ctx, del)
				if err != nil || !ack.GetDurableOwnerAck().GetAccepted() {
					t.Fatalf("deletefirst ack=%v err=%v", ack, err)
				}
				get := func(id string) *inferencev1.Operation {
					t.Helper()
					op, err := NewReadUseCase(pool).GetOperation(ctx, tenant.String(), id)
					if err != nil {
						t.Fatal(err)
					}
					return op
				}
				if late {
					// A new Model wrapper and repository model a restart; the durable closing
					// check must run before Model even though the first CREATE was rejected.
					restarted := service.NewInferenceServer(model.NewCreateUseCase(model.NewClient(boundary), NewCreateUseCase(NewRepository(pool))))
					restarted.SetManagedGPUClusterID(req.ClusterId)
					replay, err := restarted.CreateInferenceService(ctx, proto.Clone(req).(*inferencev1.CreateInferenceServiceRequest))
					if err != nil || !replay.GetDurableOwnerAck().GetAccepted() {
						t.Fatalf("late create=%v err=%v", replay, err)
					}
					blocked := get(req.Context.OperationId)
					if blocked.Phase != "failed" || blocked.Step != "cancelled_by_delete" || blocked.TargetGeneration != 0 || blocked.CompletedAt == nil {
						t.Fatalf("blocked create query=%v", blocked)
					}
				} else if _, err := NewReadUseCase(pool).GetOperation(ctx, tenant.String(), req.Context.OperationId); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("never accepted create fabricated query: %v", err)
				}
				if boundary.calls != 1 {
					t.Fatalf("closing consulted Model again: %d", boundary.calls)
				}
				if gpuResource {
					observer := &closeObserver{finding: gpu.ReleaseFindingUnknown}
					reporter := &closeReporter{unknownOnce: true}
					coordinator := func(r *Repository) *service.ManagedGPUReleaseCoordinator {
						return &service.ManagedGPUReleaseCoordinator{Observer: observer, Contexts: r, Reporter: reporter}
					}
					if err := NewRepository(pool).RunManagedCloses(ctx, coordinator(NewRepository(pool))); err == nil {
						t.Fatal("Unknown completed close")
					}
					if len(reporter.calls) != 0 || (get(del.Context.OperationId).Phase != "pending" || get(del.Context.OperationId).Step != "observe_release") {
						t.Fatal("Unknown reported release or completed delete")
					}
					observer.finding = gpu.ReleaseFindingReleased
					ready := func() {
						t.Helper()
						if _, err := pool.Exec(ctx, "UPDATE inference_managed_gpu_commands SET retry_at=now() WHERE tenant_id=$1 AND operation_id=$2", tenant, uuid.MustParse(del.Context.OperationId)); err != nil {
							t.Fatal(err)
						}
					}
					ready()
					if err := NewRepository(pool).RunManagedCloses(ctx, coordinator(NewRepository(pool))); err == nil {
						t.Fatal("lost completion was not retained for retry")
					}
					if pending := get(del.Context.OperationId); pending.Phase != "pending" || pending.Step != "report_resource_released" {
						t.Fatalf("durable release/report pending query=%v", pending)
					}
					ready()
					if err := NewRepository(pool).RunManagedCloses(ctx, coordinator(NewRepository(pool))); err != nil {
						t.Fatal(err)
					}
					if len(observer.calls) != 2 || len(reporter.calls) != 2 || reporter.calls[0] != reporter.calls[1] {
						t.Fatalf("unstable report/observation: observes=%d reports=%v", len(observer.calls), reporter.calls)
					}
					if observer.calls[0].SealedAt == "" || observer.calls[0].SealedAt != observer.calls[1].SealedAt {
						t.Fatal("seal changed across restarted scans")
					}
				}
				terminal := get(del.Context.OperationId)
				if terminal.Phase != "succeeded" || terminal.Step != "complete" || terminal.TargetGeneration != 0 || terminal.CompletedAt == nil {
					t.Fatalf("delete projection=%v", terminal)
				}
				items, total, err := NewReadUseCase(pool).ListOperationsPage(ctx, tenant.String(), resource.String(), inferencebiz.ListOptions{Limit: 10})
				want := int64(1)
				if late {
					want = 2
				}
				if err != nil || total != want || int64(len(items)) != want {
					t.Fatalf("list/count items=%v total=%d err=%v", items, total, err)
				}
				var services, reservations, work int
				for _, check := range []struct {
					sql string
					dst *int
				}{{"SELECT count(*) FROM inference_services WHERE tenant_id=$1", &services}, {"SELECT count(*) FROM inference_quota_reservations WHERE tenant_id=$1", &reservations}, {"SELECT count(*) FROM inference_resource_work WHERE tenant_id=$1", &work}} {
					if err := pool.QueryRow(ctx, check.sql, tenant).Scan(check.dst); err != nil {
						t.Fatal(err)
					}
				}
				if services != 0 || reservations != 0 || work != 0 {
					t.Fatalf("deletefirst fabricated service/reservation/work %d/%d/%d", services, reservations, work)
				}
			})
		}
	}
}
