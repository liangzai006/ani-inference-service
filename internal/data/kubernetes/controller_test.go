package kubernetes

import (
	"context"
	"errors"
	"testing"
	"time"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type recordingNotifier struct {
	tenant, service string
	generation      int64
	err             error
}

func (n *recordingNotifier) Notify(_ context.Context, tenant, service string, generation int64) error {
	n.tenant, n.service, n.generation = tenant, service, generation
	return n.err
}

func TestControllerReconcileNotifiesDurableWork(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cr := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns", Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a"}}, Spec: crdv1.InferenceServiceSpec{Generation: 4}}
	n := &recordingNotifier{}
	c := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build(), Work: n, PollInterval: time.Minute}
	got, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "ns", Name: "demo"}})
	if err != nil || got.RequeueAfter != time.Minute || n.tenant != "tenant-a" || n.service != "service-a" || n.generation != 4 {
		t.Fatalf("reconcile result=%+v err=%v notification=%+v", got, err, n)
	}
}

func TestControllerReconcilePropagatesNotifierError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cr := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns", Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a"}}, Spec: crdv1.InferenceServiceSpec{Generation: 1}}
	want := errors.New("database unavailable")
	c := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build(), Work: &recordingNotifier{err: want}}
	if _, err := c.Reconcile(t.Context(), reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "ns", Name: "demo"}}); !errors.Is(err, want) {
		t.Fatalf("reconcile error=%v, want %v", err, want)
	}
}

func TestIdentityFromObjectRequiresStableOwnershipLabels(t *testing.T) {
	obj := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "svc", Namespace: "tenant-ns", Labels: map[string]string{
			tenantIDLabel: "tenant-a", serviceIDLabel: "service-a",
		},
	}}
	tenant, service, err := identityFromObject(obj)
	if err != nil || tenant != "tenant-a" || service != "service-a" {
		t.Fatalf("identityFromObject() = %q, %q, %v", tenant, service, err)
	}

	labels := obj.GetLabels()
	delete(labels, tenantIDLabel)
	obj.SetLabels(labels)
	if _, _, err := identityFromObject(obj); err == nil {
		t.Fatal("identityFromObject() accepted an object without tenant ownership label")
	}
}

func TestAddToSchemeRegistersInferenceServiceTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []struct{ Group, Version, Kind string }{
		{InferenceServiceGVK.Group, InferenceServiceGVK.Version, InferenceServiceGVK.Kind},
		{InferenceServiceGVK.Group, InferenceServiceGVK.Version, "InferenceServiceList"},
	} {
		// The scheme's ObjectKinds method confirms registration without needing
		// an API server or controller-runtime manager.
		var obj runtime.Object
		if gvk.Kind == "InferenceService" {
			obj = &crdv1.InferenceService{}
		} else {
			obj = &crdv1.InferenceServiceList{}
		}
		obj.GetObjectKind().SetGroupVersionKind(schemaGVK(gvk.Group, gvk.Version, gvk.Kind))
		if kinds, _, err := scheme.ObjectKinds(obj); err != nil || len(kinds) == 0 {
			t.Fatalf("ObjectKinds(%v) = %v, err=%v", gvk, kinds, err)
		}
	}
	if _, _, err := scheme.ObjectKinds(&appsv1.Deployment{}); err != nil {
		t.Fatalf("Deployment scheme registration failed: %v", err)
	}
}

func TestMapRuntimeObjectUsesServiceLabelsForGeneratedPodNames(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cr := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "demo", Namespace: "tenant-ns", Labels: map[string]string{
			tenantIDLabel: "tenant-a", serviceIDLabel: "service-a",
		},
	}, Spec: crdv1.InferenceServiceSpec{Generation: 1}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "demo-7d8c9", Namespace: "tenant-ns", Labels: map[string]string{
			tenantIDLabel: "tenant-a", serviceIDLabel: "service-a", generationLabel: "1",
		},
	}}
	c := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()}
	requests := c.mapRuntimeObject(t.Context(), pod)
	if len(requests) != 1 || requests[0].Name != "demo" || requests[0].Namespace != "tenant-ns" {
		t.Fatalf("mapRuntimeObject() = %#v, want demo/tenant-ns", requests)
	}
}

func TestMapRuntimeObjectMapsJobEventsOnlyByStableLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cr := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "demo", Namespace: "tenant-ns", Labels: map[string]string{
			tenantIDLabel: "tenant-a", serviceIDLabel: "service-a",
		},
	}, Spec: crdv1.InferenceServiceSpec{Generation: 1}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: "model-materialize-abc", Namespace: "tenant-ns", Labels: map[string]string{
			tenantIDLabel: "tenant-a", serviceIDLabel: "service-a", generationLabel: "1",
		},
	}}
	c := &Controller{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).Build()}
	requests := c.mapRuntimeObject(t.Context(), job)
	if len(requests) != 1 || requests[0].Name != "demo" || requests[0].Namespace != "tenant-ns" {
		t.Fatalf("mapRuntimeObject(Job) = %#v, want demo/tenant-ns", requests)
	}
}

// schemaGVK keeps the test table readable without importing schema in every
// entry; it also ensures the same GVK values used by the adapter are tested.
func schemaGVK(group, version, kind string) schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: group, Version: version, Kind: kind}
}
