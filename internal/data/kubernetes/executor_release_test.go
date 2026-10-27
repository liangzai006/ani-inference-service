package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	bizinference "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"github.com/liangzai006/ani-inference-service/internal/biz/publication"
	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The delete target is generation 3; the running Pods carry generation 2.
func managedGPUDeleteFixture(t *testing.T, pods ...client.Object) (*RuntimeExecutor, bizinference.OperationContext) {
	t.Helper()
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "gpu-delete", Namespace: "ns", Image: "engine:v1", Generation: 3, Replicas: 1, RuntimeMode: "deployment", ManagedGPU: true,
		Resources: resources.Normalized{GPU: &gpu.Request{ContainerName: "engine", ClusterID: "cluster-a"}}}
	fc := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(pods...).Build()
	e := &RuntimeExecutor{Client: fc, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "deleted", Bindings: []RuntimeBinding{{Kind: "Deployment", Namespace: spec.Namespace, Name: spec.Name, UID: "runtime-old", ResourceVersion: "9", Role: "runtime", Generation: 2}}}}}
	op := bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, LeaseToken: "lease", TargetGeneration: spec.Generation, Publication: publication.Publication{State: "withdrawn"}, ManagedGPU: true}
	return e, op
}

func gpuPod(name, uid string, labels map[string]string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(uid), Labels: labels},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "engine"}}}, Status: corev1.PodStatus{Phase: phase}}
}

func ownedLabels(generation string) map[string]string {
	return map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a", generationLabel: generation}
}

func TestManagedGPUAbsenceWaitsForPodsAfterWorkloadIsGone(t *testing.T) {
	terminating := gpuPod("gpu-delete-0", "pod-old", ownedLabels("2"), corev1.PodRunning)
	terminating.Finalizers = []string{"test/hold"}
	e, op := managedGPUDeleteFixture(t, terminating)
	if err := e.Client.Delete(context.Background(), terminating); err != nil {
		t.Fatal(err)
	}
	got, err := e.ObserveAbsence(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	if got.Absent || got.RuntimePhase != "degraded" || !strings.Contains(got.Reason, "ns/gpu-delete-0") {
		t.Fatalf("Terminating GPU Pod was treated as released: %+v", got)
	}
	var pod corev1.Pod
	if err := e.Client.Get(context.Background(), client.ObjectKeyFromObject(terminating), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Finalizers = nil
	if err := e.Client.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	if got, err = e.ObserveAbsence(context.Background(), op); err != nil || !got.Absent {
		t.Fatalf("absence after Pod removal = %+v err=%v", got, err)
	}
}

func TestManagedGPUAbsenceIgnoresTerminalAndDownloadPods(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Generation: 2, ArtifactProvider: "model"}
	download := gpuPod(ModelClaimName(spec)+"-fetch-pod", "download", map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a", generationLabel: "2", "ani.kubercloud.com/component": "model-materializer"}, corev1.PodRunning)
	download.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: ModelClaimName(spec) + "-fetch", UID: "job-uid", Controller: boolPtr(true)}}
	e, op := managedGPUDeleteFixture(t, gpuPod("done", "done", ownedLabels("2"), corev1.PodSucceeded), gpuPod("crashed", "crashed", ownedLabels("1"), corev1.PodFailed), download)
	e.Source = fakeRuntimeSource{desired: func() DesiredRuntime {
		d := e.Source.(fakeRuntimeSource).desired
		d.ArtifactProvider = "model"
		return d
	}()}
	got, err := e.ObserveAbsence(context.Background(), op)
	if err != nil || !got.Absent {
		t.Fatalf("terminal/download Pods blocked absence: %+v err=%v", got, err)
	}
}

func TestManagedGPUAbsenceBlocksOnRecordedUIDWithDriftedLabels(t *testing.T) {
	drifted := gpuPod("gpu-delete-0", "pod-recorded", map[string]string{"app": "relabelled"}, corev1.PodRunning)
	e, op := managedGPUDeleteFixture(t, drifted)
	op.ManagedGPUReleasePods = []gpu.PodRef{{ClusterID: "cluster-a", Namespace: "ns", Name: "gpu-delete-0", UID: "pod-recorded", ContainerName: "engine"}}
	got, err := e.ObserveAbsence(context.Background(), op)
	if err != nil || got.Absent {
		t.Fatalf("recorded GPU Pod UID did not block absence: %+v err=%v", got, err)
	}
	// A replacement Pod reusing the recorded name with a new UID is unrelated.
	op.ManagedGPUReleasePods[0].UID = "pod-previous"
	if got, err = e.ObserveAbsence(context.Background(), op); err != nil || !got.Absent {
		t.Fatalf("unrelated same-name Pod blocked absence: %+v err=%v", got, err)
	}
}

func TestKServeManagedGPUAbsenceWaitsForPredictorPods(t *testing.T) {
	predictor := gpuPod("gpu-delete-predictor-0", "predictor", map[string]string{"serving.kserve.io/inferenceservice": "gpu-delete"}, corev1.PodRunning)
	predictor.Spec.Containers[0].Name = "engine"
	inner, op := managedGPUDeleteFixture(t, predictor)
	d := inner.Source.(fakeRuntimeSource).desired
	d.RuntimeProvider = "kserve"
	d.Bindings = []RuntimeBinding{{Kind: KServeInferenceServiceKind, Namespace: "ns", Name: "gpu-delete", UID: "isvc-uid", ResourceVersion: "7", Role: "runtime"}}
	inner.Source = fakeRuntimeSource{desired: d}
	e := &KServeRuntimeExecutor{RuntimeExecutor: inner}
	got, err := e.ObserveAbsence(context.Background(), op)
	if err != nil || got.Absent || !strings.Contains(got.Reason, "gpu-delete-predictor-0") {
		t.Fatalf("KServe predictor Pod was treated as released: %+v err=%v", got, err)
	}
}

func TestUnmanagedAbsenceDoesNotInspectPods(t *testing.T) {
	e, op := managedGPUDeleteFixture(t, gpuPod("gpu-delete-0", "pod-old", ownedLabels("2"), corev1.PodRunning))
	d := e.Source.(fakeRuntimeSource).desired
	d.ManagedGPU = false
	e.Source = fakeRuntimeSource{desired: d}
	if got, err := e.ObserveAbsence(context.Background(), op); err != nil || !got.Absent {
		t.Fatalf("unmanaged absence changed: %+v err=%v", got, err)
	}
}

// conflictOnDeleteClient models the foreground finalizer bumping
// resourceVersion between the first delete and a retry.
type conflictOnDeleteClient struct {
	client.Client
	policy *metav1.DeletionPropagation
}

func (c *conflictOnDeleteClient) Delete(_ context.Context, obj client.Object, opts ...client.DeleteOption) error {
	options := client.DeleteOptions{}
	for _, opt := range opts {
		opt.ApplyToDelete(&options)
	}
	c.policy = options.PropagationPolicy
	return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, obj.GetName(), nil)
}

func TestRuntimeDeleteUsesForegroundAndToleratesInProgressRetry(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "service-a", Name: "fg", Namespace: "ns", Image: "engine:v1", Generation: 1, Replicas: 1}
	dep, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	dep.UID, dep.ResourceVersion = "runtime-uid", "9"
	dep.Finalizers = []string{metav1.FinalizerDeleteDependents}
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(dep).Build()
	binding := RuntimeBinding{Kind: "Deployment", Namespace: "ns", Name: "fg", UID: "runtime-uid", ResourceVersion: "9", Role: "runtime"}
	cl := &conflictOnDeleteClient{Client: base}
	// Not yet deleting: the conflict is a real stale binding.
	if err := (&RuntimeExecutor{Client: cl}).Delete(context.Background(), binding); !apierrors.IsConflict(err) {
		t.Fatalf("stale binding conflict was swallowed: %v", err)
	}
	if cl.policy == nil || *cl.policy != metav1.DeletePropagationForeground {
		t.Fatalf("delete propagation policy = %v; want foreground", cl.policy)
	}
	if err := base.Delete(context.Background(), dep); err != nil {
		t.Fatal(err)
	}
	var current = dep.DeepCopy()
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(dep), current); err != nil || current.DeletionTimestamp == nil {
		t.Fatalf("fixture not terminating: %v", err)
	}
	if err := (&RuntimeExecutor{Client: cl}).Delete(context.Background(), binding); err != nil {
		t.Fatalf("retry against in-progress foreground delete: %v", err)
	}
}
