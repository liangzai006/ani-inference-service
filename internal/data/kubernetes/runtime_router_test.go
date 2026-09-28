package kubernetes

import (
	"context"
	"testing"

	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
)

type routerRuntimeFake struct {
	name  string
	calls []string
}

func (f *routerRuntimeFake) call(method string) { f.calls = append(f.calls, f.name+":"+method) }
func (f *routerRuntimeFake) ApplyCR(context.Context, inferencebiz.OperationContext) error {
	f.call("apply-cr")
	return nil
}
func (f *routerRuntimeFake) ApplyRuntime(context.Context, inferencebiz.OperationContext) error {
	f.call("apply-runtime")
	return nil
}
func (f *routerRuntimeFake) ObserveRuntime(context.Context, inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	f.call("observe")
	return inferencebiz.RuntimeObservation{Ready: true}, nil
}
func (f *routerRuntimeFake) DeleteRuntime(context.Context, inferencebiz.OperationContext) error {
	f.call("delete")
	return nil
}
func (f *routerRuntimeFake) ObserveAbsence(context.Context, inferencebiz.OperationContext) (inferencebiz.RuntimeObservation, error) {
	f.call("absence")
	return inferencebiz.RuntimeObservation{Absent: true}, nil
}
func (f *routerRuntimeFake) DeleteCR(context.Context, inferencebiz.OperationContext) error {
	f.call("delete-cr")
	return nil
}

type routerReconcileFake struct{ calls int }

func (f *routerReconcileFake) Ensure(context.Context, bizreconcile.Desired) (bizreconcile.Observation, error) {
	f.calls++
	return bizreconcile.Observation{Generation: 1}, nil
}

type routerSourceFake struct{ desired DesiredRuntime }

func (s routerSourceFake) CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error) {
	return s.desired, nil
}

func TestRuntimeRouterSelectsPersistedProvider(t *testing.T) {
	deployment := &routerRuntimeFake{name: "deployment"}
	kserve := &routerRuntimeFake{name: "kserve"}
	router := &RuntimeRouter{
		Source:     routerSourceFake{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{RuntimeProvider: "kserve"}}},
		Deployment: deployment, KServe: kserve,
	}
	op := inferencebiz.OperationContext{TenantID: "tenant", ServiceID: "service", TargetGeneration: 1}
	if err := router.ApplyRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(kserve.calls) != 1 || kserve.calls[0] != "kserve:apply-runtime" || len(deployment.calls) != 0 {
		t.Fatalf("provider dispatch: deployment=%v kserve=%v", deployment.calls, kserve.calls)
	}
}

func TestRuntimeRouterUsesBindingForReplacementDeletion(t *testing.T) {
	deployment := &routerRuntimeFake{name: "deployment"}
	kserve := &routerRuntimeFake{name: "kserve"}
	router := &RuntimeRouter{
		Source:     routerSourceFake{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{RuntimeProvider: "deployment"}, Bindings: []RuntimeBinding{{Kind: KServeInferenceServiceKind, Role: "runtime"}}}},
		Deployment: deployment, KServe: kserve,
	}
	op := inferencebiz.OperationContext{TenantID: "tenant", ServiceID: "service", TargetGeneration: 2}
	if err := router.DeleteRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(kserve.calls) != 1 || len(deployment.calls) != 0 {
		t.Fatalf("binding dispatch: deployment=%v kserve=%v", deployment.calls, kserve.calls)
	}
}
