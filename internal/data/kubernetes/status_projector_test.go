package kubernetes

import (
	"context"
	"testing"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type statusProjectionSource struct {
	projection StatusProjection
	tenant     string
	service    string
}

func (s *statusProjectionSource) GetStatusProjection(_ context.Context, tenant, service string) (StatusProjection, error) {
	s.tenant, s.service = tenant, service
	return s.projection, nil
}

func TestStatusProjectorPatchesOnlyStatusFromDurableProjection(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := crdv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &crdv1.InferenceService{
		TypeMeta: metav1.TypeMeta{APIVersion: crdv1.GroupVersion.String(), Kind: "InferenceService"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "tenant-a", Name: "svc-a",
			UID:    types.UID("uid-svc-a"),
			Labels: map[string]string{tenantIDLabel: "tenant-id", serviceIDLabel: "service-id"},
		},
		Spec: crdv1.InferenceServiceSpec{Generation: 7, DesiredState: "running"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
	source := &statusProjectionSource{projection: StatusProjection{
		AppliedGeneration: 7, RuntimePhase: "ready", ReadyReplicas: 1,
		ModelReady: true, PublicationPhase: "published", PublicationEffective: true,
		InvocationHealth: "healthy", Operation: &OperationProjection{ID: "op-1", Kind: "create", Phase: "succeeded", Step: "complete"},
		ControlBinding: statusControlBinding(obj),
	}}
	p := &StatusProjector{Client: cl, APIReader: cl, Source: source}
	if err := p.Project(context.Background(), client.ObjectKey{Namespace: "tenant-a", Name: "svc-a"}); err != nil {
		t.Fatal(err)
	}
	var got crdv1.InferenceService
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "tenant-a", Name: "svc-a"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Generation != 7 || got.Status.ObservedGeneration != 7 || got.Status.RuntimePhase != "ready" || got.Status.PublicationPhase != "published" || got.Status.InvocationHealth != "healthy" {
		t.Fatalf("projection changed unexpected fields: spec=%+v status=%+v", got.Spec, got.Status)
	}
	if got.Status.Operation == nil || got.Status.Operation.ID != "op-1" || !sourceMatches(source, "tenant-id", "service-id") {
		t.Fatalf("status operation/source mismatch: status=%+v source=%+v", got.Status.Operation, source)
	}
}

func statusControlBinding(obj client.Object) *RuntimeBinding {
	return &RuntimeBinding{Kind: "InferenceService", Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID()), Role: "control"}
}

func sourceMatches(source *statusProjectionSource, tenant, service string) bool {
	return source.tenant == tenant && source.service == service
}

func TestStatusProjectorRejectsUnownedCR(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := crdv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "svc-a"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()
	p := &StatusProjector{Client: cl, APIReader: cl, Source: &statusProjectionSource{}}
	if err := p.Project(context.Background(), client.ObjectKey{Namespace: "tenant-a", Name: "svc-a"}); err == nil {
		t.Fatal("Project accepted CR without ownership labels")
	}
}
