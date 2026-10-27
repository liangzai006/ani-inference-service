package service

import (
	"context"
	"errors"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

type releaseContextStoreFake struct{ original gpu.RefundContext }

func (s *releaseContextStoreFake) LoadRefundContext(context.Context, string, string, string) (gpu.RefundContext, error) {
	return s.original, nil
}

// releaseReportStoreFake records the persisted proof but never replays it, so
// every call recomputes evidence from the operation context.
type releaseReportStoreFake struct {
	releaseContextStoreFake
	evidenceIDs, digests []string
}

func (s *releaseReportStoreFake) LoadManagedReleaseReport(context.Context, gpu.RefundContext) (gpu.ReleaseNotification, bool, error) {
	return gpu.ReleaseNotification{}, false, nil
}

func (s *releaseReportStoreFake) SaveManagedReleaseReport(_ context.Context, _ gpu.RefundContext, evidenceID, digest string, _ gpu.ReleaseNotification, _ string) error {
	s.evidenceIDs, s.digests = append(s.evidenceIDs, evidenceID), append(s.digests, digest)
	return nil
}

type releaseReporterFake struct {
	calls []gpu.ReleaseNotification
}

func (f *releaseReporterFake) ReportResourceReleased(_ context.Context, notification gpu.ReleaseNotification) error {
	f.calls = append(f.calls, notification)
	return nil
}

func releaseFixture() (gpu.RefundContext, inferencebiz.OperationContext) {
	original := gpu.RefundContext{
		TenantID: "10000000-0000-4000-8000-000000000001", ResourceID: "10000000-0000-4000-8000-000000000002",
		OriginalCreateOperationID: "10000000-0000-4000-8000-000000000003", DeleteOperationID: "10000000-0000-4000-8000-000000000004",
		OwnerService: "ani-inference", ClusterID: "10000000-0000-4000-8000-000000000006",
	}
	op := inferencebiz.OperationContext{
		TenantID: original.TenantID, ServiceID: original.ResourceID, ID: original.DeleteOperationID, Kind: "delete", ManagedGPU: true,
		ManagedGPUCreateOperationID: original.OriginalCreateOperationID, Actor: "ani-inference:owner",
		ManagedGPUReleaseSealedAt: "2026-10-18T00:00:00.000000000Z",
		ManagedGPUReleasePods: []gpu.PodRef{
			{ClusterID: original.ClusterID, Namespace: "ani-system", Name: "model-0", UID: "10000000-0000-4000-8000-000000000007", ContainerName: "kserve-container"},
			{ClusterID: original.ClusterID, Namespace: "ani-system", Name: "model-1", UID: "10000000-0000-4000-8000-000000000008", ContainerName: "kserve-container"},
		},
	}
	return original, op
}

func TestManagedGPUReleaseReportsGovernanceAfterLocalAbsence(t *testing.T) {
	original, op := releaseFixture()
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{Contexts: &releaseContextStoreFake{original: original}, Reporter: reporter}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(reporter.calls) != 1 {
		t.Fatalf("reports=%v", reporter.calls)
	}
	n := reporter.calls[0]
	if n.ReleaseEventID == "" || n.CreateOperationID != op.ManagedGPUCreateOperationID || n.ResourceID != op.ServiceID || n.Reason != gpu.ResourceReleased {
		t.Fatalf("notification=%+v", n)
	}
}

func TestManagedGPUReleaseRequiresAbsenceSeal(t *testing.T) {
	original, op := releaseFixture()
	op.ManagedGPUReleaseSealedAt = ""
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{Contexts: &releaseContextStoreFake{original: original}, Reporter: reporter}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); !errors.Is(err, ErrManagedGPUReleaseNotProven) {
		t.Fatalf("err=%v, want not proven", err)
	}
	if len(reporter.calls) != 0 {
		t.Fatalf("unsealed delete reported release: %v", reporter.calls)
	}
}

func TestManagedGPUReleaseRejectsAmbiguousPodScope(t *testing.T) {
	original, op := releaseFixture()
	op.ManagedGPUReleasePods[1].UID = op.ManagedGPUReleasePods[0].UID
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{Contexts: &releaseContextStoreFake{original: original}, Reporter: reporter}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); !errors.Is(err, ErrManagedGPUReleaseNotProven) || len(reporter.calls) != 0 {
		t.Fatalf("err=%v reports=%v", err, reporter.calls)
	}
}

func TestManagedGPUReleaseProofIsStableAcrossRetries(t *testing.T) {
	original, op := releaseFixture()
	store := &releaseReportStoreFake{releaseContextStoreFake: releaseContextStoreFake{original: original}}
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{Contexts: store, Reporter: reporter}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	// A restarted worker may load the same scope in a different order.
	op.ManagedGPUReleasePods[0], op.ManagedGPUReleasePods[1] = op.ManagedGPUReleasePods[1], op.ManagedGPUReleasePods[0]
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(store.digests) != 2 || store.evidenceIDs[0] == "" || store.evidenceIDs[0] != store.evidenceIDs[1] || store.digests[0] != store.digests[1] {
		t.Fatalf("unstable proof ids=%v digests=%v", store.evidenceIDs, store.digests)
	}
	if len(reporter.calls) != 2 || reporter.calls[0] != reporter.calls[1] {
		t.Fatalf("unstable report: %v", reporter.calls)
	}
}

func TestManagedGPUReleaseAllowsEmptyPodScope(t *testing.T) {
	original, op := releaseFixture()
	op.ManagedGPUReleasePods = nil
	reporter := &releaseReporterFake{}
	coordinator := &ManagedGPUReleaseCoordinator{Contexts: &releaseContextStoreFake{original: original}, Reporter: reporter}
	if err := coordinator.ReleaseManagedGPU(context.Background(), op); err != nil || len(reporter.calls) != 1 {
		t.Fatalf("err=%v reports=%v", err, reporter.calls)
	}
}
