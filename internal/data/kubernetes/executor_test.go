package kubernetes

import (
	"context"
	"errors"
	"strconv"
	"testing"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	bizinference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/quota"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func executorScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

type fakeRuntimeSource struct{ desired DesiredRuntime }

func (f fakeRuntimeSource) CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error) {
	return f.desired, nil
}

// The API server assigns UID/resourceVersion on a successful apply. The
// controller-runtime fake client intentionally does not, so this tiny wrapper
// makes the fake response carry the same metadata needed by observation.
type fakeMetadataClient struct {
	client.Client
	uid, resourceVersion string
}

// applyRecordingClient models an API server's successful apply without
// requiring managedFields from a real server. It deliberately applies the
// submitted spec so the fencing regression can detect an unsafe write.
type applyRecordingClient struct{ client.Client }

func (c applyRecordingClient) Patch(ctx context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	current := obj.DeepCopyObject().(client.Object)
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
		return err
	}
	current.SetLabels(obj.GetLabels())
	if source, ok := obj.(*crdv1.InferenceService); ok {
		target := current.(*crdv1.InferenceService)
		target.Spec = source.Spec
	}
	return c.Client.Update(ctx, current)
}

type captureApplyClient struct {
	client.Client
	patchedResourceVersion string
	patchedUID             types.UID
}

func (c *captureApplyClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	c.patchedResourceVersion = obj.GetResourceVersion()
	c.patchedUID = obj.GetUID()
	return nil
}

type noDeleteClient struct{ client.Client }

type recordingBindingStore struct{ records []BindingRecord }

func (s *recordingBindingStore) UpsertRuntimeBinding(_ context.Context, record BindingRecord) error {
	s.records = append(s.records, record)
	return nil
}

type failingBindingStore struct{ err error }

func (s failingBindingStore) UpsertRuntimeBinding(context.Context, BindingRecord) error { return s.err }

func (c noDeleteClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	return nil
}

func (c *fakeMetadataClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.Client.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	// The fake client assigns its own resourceVersion during Patch. Reload the
	// stored object before adding the synthetic UID so the test does not create
	// an artificial optimistic-concurrency conflict.
	stored := obj.DeepCopyObject().(client.Object)
	if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), stored); err != nil {
		return err
	}
	stored.SetUID(types.UID(c.uid))
	if err := c.Client.Update(ctx, stored); err != nil {
		return err
	}
	obj.SetUID(stored.GetUID())
	obj.SetResourceVersion(stored.GetResourceVersion())
	return nil
}

func TestRuntimeExecutorAppliesTypedDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	executor := &RuntimeExecutor{Client: &fakeMetadataClient{Client: fc, uid: "apply-deployment", resourceVersion: "1"}, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "demo", Namespace: "ns", Image: "example/inference:v1", Generation: 1, Replicas: 1, RuntimeMode: "deployment", Resources: resources.Normalized{Requests: map[string]string{"cpu": "1"}}},
		DesiredState: "running", QuotaReserved: true,
	}}}
	if _, err := executor.Ensure(context.Background(), bizreconcile.Desired{TenantID: "tenant-a", ServiceID: "service-a", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	var got appsv1.Deployment
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "demo"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "example/inference:v1" || got.Labels[tenantIDLabel] != "tenant-a" {
		t.Fatalf("unexpected applied deployment: %#v", got)
	}
}

func TestRuntimeExecutorApplyCarriesFreshResourceVersion(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "rv-fence", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = types.UID("runtime-uid"), "41"
	dep.Status.ObservedGeneration = 1
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	capture := &captureApplyClient{Client: base}
	e := &RuntimeExecutor{Client: capture, APIReader: base, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Kind: "Deployment", Role: "runtime", Namespace: "ns", Name: spec.Name, UID: "runtime-uid", ResourceVersion: "41", Generation: 1}},
	}}}
	if _, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	if capture.patchedResourceVersion != "41" {
		t.Fatalf("SSA patch omitted fresh resourceVersion: got %q", capture.patchedResourceVersion)
	}
}

func TestRuntimeExecutorPersistsBindingImmediatelyAfterApply(t *testing.T) {
	scheme := executorScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	bindings := &recordingBindingStore{}
	executor := &RuntimeExecutor{Client: &fakeMetadataClient{Client: fc, uid: "apply-binding", resourceVersion: "1"}, Bindings: bindings, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "bound", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1},
		DesiredState: "running", QuotaReserved: true,
	}}}
	op := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 1, LeaseToken: "lease-1", Reservation: quota.Reservation{State: "confirmed"}}
	if err := executor.ApplyRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(bindings.records) != 1 || bindings.records[0].UID == "" || bindings.records[0].ResourceVersion == "" {
		t.Fatalf("binding was not persisted at apply boundary: %+v", bindings.records)
	}
}

func TestRuntimeExecutorAppliesExplicitEndpointService(t *testing.T) {
	scheme := executorScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	bindings := &recordingBindingStore{}
	desired := DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: "endpointed", Namespace: "ns", Image: "engine:v1",
		Generation: 1, Replicas: 1, RuntimeMode: "deployment",
		Endpoint: &EndpointSpec{ContainerPort: 9000, ServicePort: 80, TargetPort: intstr.FromInt(9000), Protocol: corev1.ProtocolTCP},
	}, DesiredState: "running", QuotaReserved: true}
	executor := &RuntimeExecutor{Client: &fakeMetadataClient{Client: fc, uid: "endpoint-uid", resourceVersion: "1"}, Bindings: bindings, Source: fakeRuntimeSource{desired: desired}}
	if _, err := executor.Ensure(context.Background(), bizreconcile.Desired{TenantID: "tenant-a", ServiceID: "service-a", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	var svc corev1.Service
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "endpointed-endpoint"}, &svc); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.Ports[0].Port != 80 || svc.Spec.Ports[0].TargetPort.IntValue() != 9000 {
		t.Fatalf("unexpected endpoint service: %+v", svc.Spec.Ports)
	}
	if len(bindings.records) != 2 || bindings.records[1].Kind != "Service" || bindings.records[1].Role != "endpoint" {
		t.Fatalf("endpoint binding was not persisted: %+v", bindings.records)
	}
}

func TestRuntimeExecutorPersistsCRBindingImmediatelyAfterApply(t *testing.T) {
	scheme := executorScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	bindings := &recordingBindingStore{}
	client := &fakeMetadataClient{Client: fc, uid: "cr-binding", resourceVersion: "1"}
	executor := &RuntimeExecutor{Client: client, CRClient: client, APIReader: client, Bindings: bindings, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "cr-bound", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1},
		DesiredState: "running", QuotaReserved: true,
	}}}
	op := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 1, LeaseToken: "lease-1", Reservation: quota.Reservation{State: "confirmed"}}
	if err := executor.ApplyCR(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if len(bindings.records) != 1 || bindings.records[0].Kind != "InferenceService" || bindings.records[0].Role != "control" || bindings.records[0].UID == "" || bindings.records[0].ResourceVersion == "" {
		t.Fatalf("CR binding was not persisted at apply boundary: %+v", bindings.records)
	}
}

func TestRuntimeExecutorDoesNotApplyCRWhenControlBindingUIDChanged(t *testing.T) {
	scheme := executorScheme(t)
	replacement := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "cr-fence", Namespace: "ns", UID: types.UID("replacement-uid"),
		Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a"},
	}, Spec: crdv1.InferenceServiceSpec{Generation: 1, DesiredState: "running"}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement).Build()
	cl := applyRecordingClient{Client: base}
	e := &RuntimeExecutor{Client: cl, CRClient: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "cr-fence", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1},
		DesiredState: "running", Bindings: []RuntimeBinding{{Kind: "InferenceService", Role: "control", Namespace: "ns", Name: "cr-fence", UID: "old-uid", ResourceVersion: "1", Generation: 1}},
	}}}
	err := e.ApplyCR(context.Background(), bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 2, LeaseToken: "lease", Reservation: quota.Reservation{State: "confirmed"}})
	if !errors.Is(err, bizreconcile.ErrStaleGeneration) {
		t.Fatalf("ApplyCR error = %v, want stale generation", err)
	}
	got := &crdv1.InferenceService{}
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "cr-fence"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Generation != 1 {
		t.Fatalf("stale worker modified replacement CR: %+v", got.Spec)
	}
}

func TestRuntimeExecutorDeleteCRUsesFreshResourceVersionAfterStatusWrite(t *testing.T) {
	scheme := executorScheme(t)
	obj := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "cr-delete", Namespace: "ns", UID: types.UID("delete-uid"), ResourceVersion: "2",
		Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a"},
	}, Spec: crdv1.InferenceServiceSpec{Generation: 1, DesiredState: "running"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()
	e := &RuntimeExecutor{Client: cl, CRClient: cl, APIReader: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "cr-delete", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1},
		DesiredState: "deleted", Bindings: []RuntimeBinding{{Kind: "InferenceService", Role: "control", Namespace: "ns", Name: "cr-delete", UID: "delete-uid", ResourceVersion: "1", Generation: 1}},
	}}}
	if err := e.DeleteCR(context.Background(), bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 1, LeaseToken: "lease"}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "cr-delete"}, &crdv1.InferenceService{}); err == nil {
		t.Fatal("DeleteCR did not delete object with status-advanced resourceVersion")
	}
}

func TestRuntimeExecutorFailsWhenBindingPersistenceFails(t *testing.T) {
	scheme := executorScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	want := errors.New("postgres unavailable")
	executor := &RuntimeExecutor{Client: &fakeMetadataClient{Client: fc, uid: "binding-failure", resourceVersion: "1"}, Bindings: failingBindingStore{err: want}, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "binding-failure", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1},
		DesiredState: "running", QuotaReserved: true,
	}}}
	op := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 1, LeaseToken: "lease-1", Reservation: quota.Reservation{State: "confirmed"}}
	if err := executor.ApplyRuntime(context.Background(), op); !errors.Is(err, want) {
		t.Fatalf("ApplyRuntime() error = %v, want binding persistence error", err)
	}
	var got appsv1.Deployment
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "binding-failure"}, &got); err != nil {
		t.Fatalf("runtime apply should have reached Kubernetes before persistence failure: %v", err)
	}
}

func TestRuntimeExecutorProjectsTypedInferenceCR(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "demo", Namespace: "ns", Image: "engine:v1", ModelVersionID: "model-v1", Generation: 1, Replicas: 1, RuntimeMode: "deployment", Endpoint: &EndpointSpec{ContainerPort: 9000, ServicePort: 80, TargetPort: intstr.FromInt(9000), Protocol: corev1.ProtocolTCP}}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = types.UID("runtime-uid"), "1"
	fc := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: fc, CRClient: fc, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true, Bindings: []RuntimeBinding{{Kind: "Deployment", Namespace: "ns", Name: "demo", UID: "runtime-uid", ResourceVersion: "1", Role: "runtime"}}}}}
	if _, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation}); err != nil {
		t.Fatal(err)
	}
	var got crdv1.InferenceService
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "demo"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Generation != 1 || got.Spec.ModelVersionID != "model-v1" || got.Labels[tenantIDLabel] != "tenant-a" {
		t.Fatalf("unexpected CR projection: %+v", got.Spec)
	}
	if got.Spec.Endpoint == nil || got.Spec.Endpoint.ContainerPort != 9000 || got.Spec.Endpoint.ServicePort != 80 || got.Spec.Endpoint.TargetPort != "9000" || got.Spec.Endpoint.Protocol != string(corev1.ProtocolTCP) {
		t.Fatalf("endpoint was not projected to CR: %+v", got.Spec.Endpoint)
	}
}

func TestRuntimeExecutorRejectsUnboundExistingRuntime(t *testing.T) {
	fc := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	e := &RuntimeExecutor{Client: fc}
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "fenced", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1}
	if _, err := e.Apply(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(context.Background(), spec); err == nil {
		t.Fatal("Apply() adopted an existing runtime without a persisted binding")
	}
}

func TestRuntimeExecutorAppliesTypedLeaderWorkerSet(t *testing.T) {
	scheme := executorScheme(t)
	fc := fake.NewClientBuilder().WithScheme(scheme).Build()
	e := &RuntimeExecutor{Client: &fakeMetadataClient{Client: fc, uid: "apply-lws", resourceVersion: "1"}, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec:  RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "distributed", Namespace: "ns", Image: "example/inference:v1", Generation: 1, Replicas: 2, WorkerReplicas: 3, RuntimeMode: "leader_worker_set"},
		DesiredState: "running", QuotaReserved: true,
	}}}
	if _, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: "tenant-a", ServiceID: "service-a", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	var got lwsv1.LeaderWorkerSet
	if err := fc.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "distributed"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 2 || got.Spec.LeaderWorkerTemplate.Size == nil || *got.Spec.LeaderWorkerTemplate.Size != 4 {
		t.Fatalf("unexpected LWS shape: replicas=%v size=%v", got.Spec.Replicas, got.Spec.LeaderWorkerTemplate.Size)
	}
}

func TestRuntimeExecutorApplyRejectsNewerRuntimeGeneration(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v2", Generation: 5, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = types.UID("current-runtime"), "9"
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: cl}
	spec.Generation, spec.Image = 4, "engine:v1"
	if _, err := e.Apply(context.Background(), spec); err != bizreconcile.ErrStaleGeneration {
		t.Fatalf("Apply() error = %v, want stale generation", err)
	}
	got := &appsv1.Deployment{}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(dep), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Template.Spec.Containers[0].Image != "engine:v2" {
		t.Fatal("stale apply replaced the newer runtime")
	}
}

func TestRuntimeExecutorEnsureRequiresQuotaAndPublicationFacts(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	target := bizreconcile.Desired{TenantID: "tenant-a", ServiceID: "service-a", Generation: 3}
	spec := RuntimeSpec{TenantID: target.TenantID, ServiceID: target.ServiceID, Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: target.Generation, Replicas: 1}
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running"}}}
	if _, err := e.Ensure(context.Background(), target); err == nil {
		t.Fatal("Ensure() applied runtime without a confirmed quota reservation")
	}
	for _, state := range []string{"stopped", "deleted"} {
		e.Source = fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: state}}
		if _, err := e.Ensure(context.Background(), target); err == nil {
			t.Fatalf("Ensure() accepted %s before publication withdrawal was confirmed", state)
		}
		e.Source = fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: state, PublicationWithdrawn: true}}
		if _, err := e.Ensure(context.Background(), target); err == nil {
			t.Fatalf("Ensure() accepted %s without a persisted runtime binding", state)
		}
	}
}

func TestRuntimeExecutorDeleteRequiresFencing(t *testing.T) {
	executor := &RuntimeExecutor{}
	if err := executor.Delete(context.Background(), RuntimeBinding{Kind: "Deployment", Namespace: "ns", Name: "demo"}); err == nil {
		t.Fatal("Delete accepted an unfenced binding")
	}
}

func TestRuntimeExecutorObservesDeploymentReadiness(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = types.UID("uid-1"), "7"
	dep.Status.ReadyReplicas = 1
	dep.Status.UpdatedReplicas = 1
	dep.Status.AvailableReplicas = 1
	dep.Generation = 2
	dep.Status.ObservedGeneration = 1
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Kind: "Deployment", Namespace: "ns", Name: "svc", UID: "uid-1", ResourceVersion: "7", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "pending" || observation.ReadyReplicas != 1 || observation.RuntimeMode != "deployment" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestRuntimeExecutorObservesDeploymentReadyOnlyAfterControllerGenerationAndCounts(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion, dep.Generation = types.UID("uid-2"), "8", 2
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, AvailableReplicas: 1, ReadyReplicas: 1}
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Generation: 2, Kind: "Deployment", Namespace: "ns", Name: "svc", UID: "uid-2", ResourceVersion: "8", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "ready" || len(observation.Objects) != 1 || observation.Objects[0].UID != "uid-2" {
		t.Fatalf("unexpected ready observation: %+v", observation)
	}
}

func TestRuntimeExecutorMarksReadyRuntimeDegradedAfterStatusRegression(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "svc", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion, dep.Generation = types.UID("uid-3"), "9", 2
	dep.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, UpdatedReplicas: 1, AvailableReplicas: 0, ReadyReplicas: 0}
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Generation: 2, Kind: "Deployment", Namespace: "ns", Name: "svc", UID: "uid-3", ResourceVersion: "9", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "degraded" {
		t.Fatalf("runtime regression was reported as %q", observation.RuntimePhase)
	}
}

func TestRuntimeExecutorObservesLeaderWorkerSetReadiness(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "lws", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 2, WorkerReplicas: 3, RuntimeMode: "leader_worker_set"}
	lws, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	lws.UID, lws.ResourceVersion, lws.Generation = types.UID("lws-1"), "10", 3
	lws.Status.ReadyReplicas, lws.Status.UpdatedReplicas, lws.Status.ObservedGeneration = 2, 2, 3
	objects := []client.Object{lws}
	for i := 0; i < 6; i++ {
		objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: "lws-worker-" + strconv.Itoa(i), Namespace: spec.Namespace,
			Labels: map[string]string{tenantIDLabel: spec.TenantID, serviceIDLabel: spec.ServiceID, generationLabel: "3", lwsv1.SetNameLabelKey: spec.Name, lwsv1.GroupIndexLabelKey: strconv.Itoa(i / 3), lwsv1.WorkerIndexLabelKey: strconv.Itoa(i%3 + 1)},
		}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}})
	}
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Generation: 3, Kind: "LeaderWorkerSet", Namespace: "ns", Name: "lws", UID: "lws-1", ResourceVersion: "10", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "ready" || observation.ReadyGroups != 2 || observation.ReadyWorkers != 6 || len(observation.Objects) != 1 {
		t.Fatalf("unexpected LWS observation: %+v", observation)
	}
}

func TestRuntimeExecutorDoesNotTreatReadyLWSGroupsAsReadyWorkers(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "lws-workers-missing", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 1, WorkerReplicas: 2, RuntimeMode: "leader_worker_set"}
	lws, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	lws.UID, lws.ResourceVersion, lws.Generation = types.UID("lws-missing-workers"), "10", 3
	lws.Status.ReadyReplicas, lws.Status.UpdatedReplicas, lws.Status.ObservedGeneration = 1, 1, 3
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(lws).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
		Bindings: []RuntimeBinding{{Generation: 3, Kind: "LeaderWorkerSet", Namespace: "ns", Name: spec.Name, UID: string(lws.UID), ResourceVersion: lws.ResourceVersion, Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase == "ready" || observation.ReadyGroups != 1 || observation.ReadyWorkers != 0 {
		t.Fatalf("LWS group readiness was treated as worker readiness: %+v", observation)
	}
}

func TestRuntimeExecutorDeleteUsesOldBindingUntilObjectIsGone(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "new-name", Namespace: "ns", Image: "engine:v1", Generation: 4, Replicas: 2, WorkerReplicas: 3, RuntimeMode: "leader_worker_set"}
	old, err := Deployment(RuntimeSpec{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Name: "old-name", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	old.UID, old.ResourceVersion = types.UID("old-uid"), "11"
	cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(old).Build()
	e := &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "deleted", PublicationWithdrawn: true,
		Bindings: []RuntimeBinding{{Generation: 3, Kind: "Deployment", Namespace: "ns", Name: "old-name", UID: "old-uid", ResourceVersion: "11", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "stopped" || len(observation.Objects) != 1 || !observation.Objects[0].Missing || observation.Objects[0].BindingGeneration != 3 {
		t.Fatalf("old runtime deletion was not confirmed: %+v", observation)
	}
}

func TestRuntimeExecutorDoesNotReportStoppedWhileOldBindingObjectRemains(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "new-name", Namespace: "ns", Image: "engine:v1", Generation: 4, Replicas: 1}
	old, err := Deployment(RuntimeSpec{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Name: "old-name", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	old.UID, old.ResourceVersion = types.UID("old-uid"), "12"
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(old).Build()
	e := &RuntimeExecutor{Client: noDeleteClient{Client: base}, Source: fakeRuntimeSource{desired: DesiredRuntime{
		RuntimeSpec: spec, DesiredState: "stopped", PublicationWithdrawn: true,
		Bindings: []RuntimeBinding{{Generation: 3, Kind: "Deployment", Namespace: "ns", Name: "old-name", UID: "old-uid", ResourceVersion: "12", Role: "runtime"}},
	}}}
	observation, err := e.Ensure(context.Background(), bizreconcile.Desired{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if observation.RuntimePhase != "degraded" || len(observation.Objects) != 1 || observation.Objects[0].Missing {
		t.Fatalf("remaining old runtime was reported as stopped: %+v", observation)
	}
}

func TestRuntimeExecutorOperationPortKeepsRuntimeFactsSeparate(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "op-port", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1, RuntimeMode: "deployment"}
	scheme := executorScheme(t)
	base := fake.NewClientBuilder().WithScheme(scheme).Build()
	crClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	e := &RuntimeExecutor{Client: &fakeMetadataClient{Client: base, uid: "runtime-uid", resourceVersion: "1"}, CRClient: crClient, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running"}}}
	op := bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, LeaseToken: "lease", TargetGeneration: spec.Generation, Reservation: quota.Reservation{State: "confirmed"}}
	if err := e.ApplyCR(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	var cr crdv1.InferenceService
	if err := crClient.Get(context.Background(), client.ObjectKey{Namespace: spec.Namespace, Name: spec.Name}, &cr); err != nil {
		t.Fatal(err)
	}
	if cr.Spec.Generation != spec.Generation || cr.Spec.DesiredState != "running" {
		t.Fatalf("unexpected operation CR projection: %+v", cr.Spec)
	}
	if err := e.ApplyRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	observation, err := e.ObserveRuntime(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Ready {
		t.Fatal("runtime should not be ready before controller status is observed")
	}
	if observation.ModelReady {
		t.Fatalf("unverified model fact was reported healthy: %+v", observation)
	}
}

func TestRuntimeExecutorOperationPortDeletesAndConfirmsAbsence(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "op-delete", Namespace: "ns", Image: "engine:v1", Generation: 2, Replicas: 1, RuntimeMode: "deployment"}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = types.UID("runtime-old"), "9"
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	e := &RuntimeExecutor{Client: base, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "stopped", Bindings: []RuntimeBinding{{Kind: "Deployment", Namespace: spec.Namespace, Name: spec.Name, UID: "runtime-old", ResourceVersion: "9", Role: "runtime", Generation: spec.Generation}}}}}
	op := bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, LeaseToken: "lease", TargetGeneration: spec.Generation, Publication: publication.Publication{State: "withdrawn"}}
	if err := e.DeleteRuntime(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	observation, err := e.ObserveAbsence(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Absent || observation.Ready || observation.ModelReady {
		t.Fatalf("unexpected absence observation: %+v", observation)
	}
}

func TestRuntimeExecutorOperationPortRequiresLeaseAndPublicationFacts(t *testing.T) {
	e := &RuntimeExecutor{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).Build(), Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: RuntimeSpec{Generation: 1}, DesiredState: "stopped"}}}
	withoutLease := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", TargetGeneration: 1}
	if err := e.ApplyRuntime(context.Background(), withoutLease); !errors.Is(err, bizreconcile.ErrStaleGeneration) {
		t.Fatalf("ApplyRuntime without lease error=%v, want stale generation", err)
	}
	withoutWithdrawal := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: "service-a", LeaseToken: "lease", TargetGeneration: 1}
	if err := e.DeleteRuntime(context.Background(), withoutWithdrawal); err == nil {
		t.Fatal("DeleteRuntime without publication withdrawal succeeded")
	}
}
