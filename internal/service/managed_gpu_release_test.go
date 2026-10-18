package service

import (
	"context"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

type releaseContextStoreFake struct{ original gpu.RefundContext }

func (s *releaseContextStoreFake) LoadRefundContext(context.Context, string, string, string) (gpu.RefundContext, error) {
	return s.original, nil
}

type releaseObserverFake struct {
	input gpu.ReleaseObservationInput
}

func (f *releaseObserverFake) ObserveRelease(_ context.Context, input gpu.ReleaseObservationInput) (gpu.ReleaseObservation, error) {
	f.input = input
	return gpu.ReleaseObservation{EvidenceID: "10000000-0000-4000-8000-000000000014", EvidenceDigest: "digest", Finding: gpu.ReleaseFindingReleased}, nil
}

type releaseReporterFake struct {
	notification gpu.ReleaseNotification
}

func (f *releaseReporterFake) ReportResourceReleased(_ context.Context, notification gpu.ReleaseNotification) error {
	f.notification = notification
	return nil
}

func TestManagedGPUReleaseRequiresAcceleratorEvidenceBeforeGovernance(t *testing.T) {
	observer := &releaseObserverFake{}
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{
		Observer: observer,
		Contexts: &releaseContextStoreFake{original: gpu.RefundContext{
			TenantID: "10000000-0000-4000-8000-000000000001", ResourceID: "10000000-0000-4000-8000-000000000002",
			OriginalCreateOperationID: "10000000-0000-4000-8000-000000000003", DeleteOperationID: "10000000-0000-4000-8000-000000000004",
			OwnerService: "ani-inference", ClusterID: "10000000-0000-4000-8000-000000000006",
		}},
		Reporter: reporter,
	}
	op := inferencebiz.OperationContext{
		TenantID: "10000000-0000-4000-8000-000000000001", ServiceID: "10000000-0000-4000-8000-000000000002", ID: "10000000-0000-4000-8000-000000000004", Kind: "delete", ManagedGPU: true,
		ManagedGPUCreateOperationID: "10000000-0000-4000-8000-000000000003", Actor: "ani-inference:owner",
		GPURequest:            &gpu.Request{ClusterID: "10000000-0000-4000-8000-000000000006"},
		ManagedGPUReleasePods: []gpu.PodRef{{ClusterID: "10000000-0000-4000-8000-000000000006", Namespace: "ani-system", Name: "model-0", UID: "10000000-0000-4000-8000-000000000007", ContainerName: "kserve-container"}},
	}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if observer.input.DeleteOperationID != op.ID || len(observer.input.Pods) != 1 || reporter.notification.ReleaseEventID == "" {
		t.Fatalf("observer=%+v notification=%+v", observer.input, reporter.notification)
	}
	if reporter.notification.CreateOperationID != op.ManagedGPUCreateOperationID || reporter.notification.ResourceID != op.ServiceID || reporter.notification.Reason != gpu.ResourceReleased {
		t.Fatalf("notification=%+v", reporter.notification)
	}
}
