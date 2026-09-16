package kubernetes

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	bizinference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/quota"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

type projectionSourceFunc func(context.Context, string, string) (StatusProjection, error)

type runtimeRaceApplyClient struct {
	client.Client
	bumped bool
}

func (c *runtimeRaceApplyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if !c.bumped {
		current := obj.DeepCopyObject().(client.Object)
		if err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
			return err
		}
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations["ani.kubercloud.com/race"] = "bumped"
		current.SetAnnotations(annotations)
		if err := c.Client.Update(ctx, current); err != nil {
			return err
		}
		c.bumped = true
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func (f projectionSourceFunc) GetStatusProjection(ctx context.Context, tenant, service string) (StatusProjection, error) {
	return f(ctx, tenant, service)
}

func TestRuntimeExecutorCRFencingEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run CR fencing API test")
	}
	useExisting := false
	crdPaths := []string{"../../../config/crd/bases"}
	if lwsPath := os.Getenv("LWS_CRD_PATH"); lwsPath != "" {
		crdPaths = append(crdPaths, lwsPath)
	}
	env := &envtest.Environment{UseExistingCluster: &useExisting, CRDDirectoryPaths: crdPaths}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cr-fencing-test"}}
	if err := kube.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	makeSpec := func(name string, generation int64) DesiredRuntime {
		return DesiredRuntime{RuntimeSpec: RuntimeSpec{
			TenantID: "tenant-a", ServiceID: name, Name: name, Namespace: ns.Name,
			Image: "engine:v1", EngineRuntime: "engine", ModelVersionID: "model-v1",
			Generation: generation, Replicas: 1,
		}, DesiredState: "running"}
	}
	t.Run("endpoint deletion and observation", func(t *testing.T) {
		testEndpointDeletionAPI(t, ctx, kube, ns.Name)
	})

	t.Run("status RV advances before delete", func(t *testing.T) {
		name := "delete-fenced"
		bindings := &recordingBindingStore{}
		e := &RuntimeExecutor{Client: kube, CRClient: kube, APIReader: kube, Bindings: bindings, Source: fakeRuntimeSource{desired: makeSpec(name, 1)}}
		op := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: name, TargetGeneration: 1, LeaseToken: "lease", Reservation: quota.Reservation{State: "confirmed"}}
		if err := e.ApplyCR(ctx, op); err != nil {
			t.Fatal(err)
		}
		if len(bindings.records) != 1 {
			t.Fatalf("missing control binding: %+v", bindings.records)
		}
		current := &crdv1.InferenceService{}
		key := client.ObjectKey{Namespace: ns.Name, Name: name}
		if err := kube.Get(ctx, key, current); err != nil {
			t.Fatal(err)
		}
		before := current.ResourceVersion
		current.Status.RuntimePhase = "pending"
		if err := kube.Status().Update(ctx, current); err != nil {
			t.Fatal(err)
		}
		if current.ResourceVersion == before {
			t.Fatal("status update did not advance resourceVersion")
		}
		spec := makeSpec(name, 1)
		spec.DesiredState = "deleted"
		record := bindings.records[0]
		spec.Bindings = []RuntimeBinding{{Kind: record.Kind, Role: record.Role, Namespace: record.Namespace, Name: record.Name, UID: record.UID, ResourceVersion: record.ResourceVersion, Generation: record.Generation}}
		e.Source = fakeRuntimeSource{desired: spec}
		if err := e.DeleteCR(ctx, op); err != nil {
			t.Fatalf("DeleteCR rejected current RV after status update: %v", err)
		}
	})

	t.Run("replacement UID is fenced before SSA", func(t *testing.T) {
		name := "apply-fenced"
		old := makeSpec(name, 1)
		e := &RuntimeExecutor{Client: kube, CRClient: kube, APIReader: kube, Bindings: &recordingBindingStore{}, Source: fakeRuntimeSource{desired: old}}
		op := bizinference.OperationContext{TenantID: "tenant-a", ServiceID: name, TargetGeneration: 1, LeaseToken: "lease", Reservation: quota.Reservation{State: "confirmed"}}
		if err := e.ApplyCR(ctx, op); err != nil {
			t.Fatal(err)
		}
		key := client.ObjectKey{Namespace: ns.Name, Name: name}
		created := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, created); err != nil {
			t.Fatal(err)
		}
		binding := statusControlBinding(created)
		if err := kube.Delete(ctx, created); err != nil {
			t.Fatal(err)
		}
		if err := waitForObjectGone(ctx, kube, key); err != nil {
			t.Fatal(err)
		}
		replacement := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name,
			Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: name}}, Spec: crdv1.InferenceServiceSpec{Generation: 1, DesiredState: "running", ModelVersionID: "replacement", Replicas: 1}}
		if err := kube.Create(ctx, replacement); err != nil {
			t.Fatal(err)
		}
		next := old
		next.Generation = 2
		next.Bindings = []RuntimeBinding{*binding}
		e.Source = fakeRuntimeSource{desired: next}
		err = e.ApplyCR(ctx, bizinference.OperationContext{TenantID: "tenant-a", ServiceID: name, TargetGeneration: 2, LeaseToken: "lease", Reservation: quota.Reservation{State: "confirmed"}})
		if !errors.Is(err, bizreconcile.ErrStaleGeneration) {
			t.Fatalf("ApplyCR error = %v, want stale generation", err)
		}
		got := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.Generation != 1 {
			t.Fatalf("replacement was modified before fencing: %+v", got.Spec)
		}
	})

	t.Run("runtime SSA rejects a concurrent API update", func(t *testing.T) {
		spec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "runtime-race", Name: "runtime-race", Namespace: ns.Name, Image: "engine:v1", Generation: 1, Replicas: 1}
		deployment, err := Deployment(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := kube.Create(ctx, deployment); err != nil {
			t.Fatal(err)
		}
		current := &appsv1.Deployment{}
		key := client.ObjectKeyFromObject(deployment)
		if err := kube.Get(ctx, key, current); err != nil {
			t.Fatal(err)
		}
		rv := current.ResourceVersion
		tracing := &runtimeRaceApplyClient{Client: kube}
		e := &RuntimeExecutor{Client: tracing, APIReader: kube, Source: fakeRuntimeSource{desired: DesiredRuntime{
			RuntimeSpec: spec, DesiredState: "running", QuotaReserved: true,
			Bindings: []RuntimeBinding{{Kind: "Deployment", Role: "runtime", Namespace: ns.Name, Name: spec.Name, UID: string(current.UID), ResourceVersion: rv, Generation: 1}},
		}}}
		err = e.ApplyRuntime(ctx, bizinference.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: 1, LeaseToken: "lease", Reservation: quota.Reservation{State: "confirmed"}})
		if !apierrors.IsConflict(err) {
			t.Fatalf("ApplyRuntime error = %v, want API conflict", err)
		}
		got := &appsv1.Deployment{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.Annotations["ani.kubercloud.com/race"] != "bumped" || got.Spec.Template.Spec.Containers[0].Image != deployment.Spec.Template.Spec.Containers[0].Image {
			t.Fatalf("concurrent update or runtime spec was lost: annotations=%v image=%s", got.Annotations, got.Spec.Template.Spec.Containers[0].Image)
		}

		t.Run("endpoint service SSA also conflicts", func(t *testing.T) {
			endpointSpec := spec
			endpointSpec.Endpoint = &EndpointSpec{ContainerPort: 9000, ServicePort: 80, TargetPort: intstr.FromInt(9000), Protocol: corev1.ProtocolTCP}
			service, err := Service(endpointSpec)
			if err != nil {
				t.Fatal(err)
			}
			if err := kube.Create(ctx, service); err != nil {
				t.Fatal(err)
			}
			currentService := &corev1.Service{}
			serviceKey := client.ObjectKeyFromObject(service)
			if err := kube.Get(ctx, serviceKey, currentService); err != nil {
				t.Fatal(err)
			}
			serviceRace := &runtimeRaceApplyClient{Client: kube}
			_, err = (&RuntimeExecutor{Client: serviceRace, APIReader: kube}).applyEndpointFenced(ctx, endpointSpec, &RuntimeBinding{
				Kind: "Service", Role: "endpoint", Namespace: ns.Name, Name: service.Name,
				UID: string(currentService.UID), ResourceVersion: currentService.ResourceVersion, Generation: 1,
			})
			if !apierrors.IsConflict(err) {
				t.Fatalf("endpoint apply error = %v, want API conflict", err)
			}
		})

		if os.Getenv("LWS_CRD_PATH") != "" {
			t.Run("LWS SSA also conflicts", func(t *testing.T) {
				lwsSpec := RuntimeSpec{TenantID: "tenant-a", ServiceID: "lws-race", Name: "lws-race", Namespace: ns.Name, Image: "engine:v1", Generation: 1, Replicas: 1, WorkerReplicas: 2, RuntimeMode: "leader_worker_set"}
				lws, err := LeaderWorkerSet(lwsSpec)
				if err != nil {
					t.Fatal(err)
				}
				if err := kube.Create(ctx, lws); err != nil {
					t.Fatal(err)
				}
				currentLWS := &lwsv1.LeaderWorkerSet{}
				key := client.ObjectKeyFromObject(lws)
				if err := kube.Get(ctx, key, currentLWS); err != nil {
					t.Fatal(err)
				}
				tracing := &runtimeRaceApplyClient{Client: kube}
				_, err = (&RuntimeExecutor{Client: tracing, APIReader: kube}).applyFenced(ctx, lwsSpec, &RuntimeBinding{
					Kind: "LeaderWorkerSet", Role: "runtime", Namespace: ns.Name, Name: lws.Name,
					UID: string(currentLWS.UID), ResourceVersion: currentLWS.ResourceVersion, Generation: 1,
				})
				if !apierrors.IsConflict(err) {
					t.Fatalf("LWS apply error = %v, want API conflict", err)
				}
			})
		}
	})
}

// This starts only a private API server/etcd. Model/PG/provider results below
// are fixtures; this test proves status-subresource concurrency semantics.
func TestStatusProjectorEnvtestConcurrency(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run the isolated status API test")
	}
	useExisting := false
	env := &envtest.Environment{UseExistingCluster: &useExisting, CRDDirectoryPaths: []string{"../../../config/crd/bases"}}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop private API server: %v", err)
		}
	})
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := kube.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "status-projection-test"}}); err != nil {
		t.Fatal(err)
	}
	createCR := func(t *testing.T, name string) *crdv1.InferenceService {
		t.Helper()
		cr := &crdv1.InferenceService{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "status-projection-test", Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: name}},
			Spec:       crdv1.InferenceServiceSpec{Generation: 7, DesiredState: "running", ModelVersionID: "model-v1", Replicas: 1},
		}
		if err := kube.Create(ctx, cr); err != nil {
			t.Fatal(err)
		}
		return cr
	}

	t.Run("concurrent status refreshes the durable projection", func(t *testing.T) {
		cr := createCR(t, "concurrent")
		key := client.ObjectKeyFromObject(cr)
		controlBinding := statusControlBinding(cr)
		staleAfter := time.Now().UTC().Truncate(time.Second).Add(time.Minute)
		fresh := StatusProjection{AppliedGeneration: 6, RuntimePhase: "ready", RuntimeStaleAfter: &staleAfter, ControlBinding: controlBinding}
		calls := 0
		source := projectionSourceFunc(func(ctx context.Context, tenant, service string) (StatusProjection, error) {
			calls++
			if tenant != "tenant-a" || service != "concurrent" {
				t.Fatalf("unexpected identity %s/%s", tenant, service)
			}
			if calls == 1 {
				// A second writer advances status after the first APIReader.Get,
				// but before that first projection is patched.
				other := &crdv1.InferenceService{}
				if err := kube.Get(ctx, key, other); err != nil {
					return StatusProjection{}, err
				}
				other.Status = statusFromProjection(fresh)
				if err := kube.Status().Update(ctx, other); err != nil {
					return StatusProjection{}, err
				}
				return StatusProjection{AppliedGeneration: 3, RuntimePhase: "pending", ControlBinding: controlBinding}, nil
			}
			return fresh, nil
		})
		projector := &StatusProjector{Client: kube, APIReader: kube, Source: source}
		if err := projector.Project(ctx, key); err != nil {
			t.Fatal(err)
		}
		got := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if calls != 2 || got.Status.ObservedGeneration != 6 || got.Status.RuntimePhase != "ready" {
			t.Fatalf("old result overwrote current status: calls=%d status=%+v", calls, got.Status)
		}
		if got.Status.Runtime == nil || got.Status.Runtime.StaleAfter == nil || !got.Status.Runtime.StaleAfter.Time.Equal(staleAfter) {
			t.Fatalf("API pruned status freshness: %+v", got.Status.Runtime)
		}
		if got.Spec.Generation != 7 || got.Generation != cr.Generation || got.UID != cr.UID {
			t.Fatalf("status write changed desired identity/spec: %+v", got)
		}
		version := got.ResourceVersion
		if err := projector.Project(ctx, key); err != nil {
			t.Fatal(err)
		}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.ResourceVersion != version {
			t.Fatal("equal projection caused another status write")
		}
	})

	t.Run("replacement UID is not written by the old projection", func(t *testing.T) {
		cr := createCR(t, "replacement")
		key := client.ObjectKeyFromObject(cr)
		oldBinding := statusControlBinding(cr)
		calls := 0
		source := projectionSourceFunc(func(ctx context.Context, _, _ string) (StatusProjection, error) {
			calls++
			if calls == 1 {
				if err := kube.Delete(ctx, cr); err != nil {
					return StatusProjection{}, err
				}
				if err := waitForObjectGone(ctx, kube, key); err != nil {
					return StatusProjection{}, err
				}
				createCR(t, "replacement")
			}
			return StatusProjection{AppliedGeneration: 3, RuntimePhase: "pending", ControlBinding: oldBinding}, nil
		})
		projector := &StatusProjector{Client: kube, APIReader: kube, Source: source}
		if err := projector.Project(ctx, key); err == nil {
			t.Error("old invocation accepted a replacement UID")
		}
		got := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.UID == cr.UID || got.Status.RuntimePhase != "" || calls != 1 {
			t.Fatalf("replacement received old result: calls=%d uid=%s status=%+v", calls, got.UID, got.Status)
		}
	})

	t.Run("fresh invocation rejects an already replaced UID", func(t *testing.T) {
		cr := createCR(t, "replacement-fresh")
		key := client.ObjectKeyFromObject(cr)
		oldBinding := statusControlBinding(cr)
		if err := kube.Delete(ctx, cr); err != nil {
			t.Fatal(err)
		}
		if err := waitForObjectGone(ctx, kube, key); err != nil {
			t.Fatal(err)
		}
		replacement := createCR(t, "replacement-fresh")
		if replacement.UID == cr.UID {
			t.Fatal("replacement did not receive a new UID")
		}
		calls := 0
		source := projectionSourceFunc(func(context.Context, string, string) (StatusProjection, error) {
			calls++
			return StatusProjection{AppliedGeneration: 3, RuntimePhase: "pending", ControlBinding: oldBinding}, nil
		})
		projector := &StatusProjector{Client: kube, APIReader: kube, Source: source}
		if err := projector.Project(ctx, key); err == nil {
			t.Error("old binding was accepted for an already replaced UID")
		}
		got := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.UID == cr.UID || got.Status.RuntimePhase != "" || calls != 1 {
			t.Fatalf("replacement received old result: calls=%d uid=%s status=%+v", calls, got.UID, got.Status)
		}
	})

	t.Run("ownership change cannot receive another tenant's status", func(t *testing.T) {
		cr := createCR(t, "ownership")
		key := client.ObjectKeyFromObject(cr)
		calls := 0
		source := projectionSourceFunc(func(ctx context.Context, _, _ string) (StatusProjection, error) {
			calls++
			if calls == 1 {
				other := &crdv1.InferenceService{}
				if err := kube.Get(ctx, key, other); err != nil {
					return StatusProjection{}, err
				}
				other.Labels[tenantIDLabel] = "tenant-b"
				if err := kube.Update(ctx, other); err != nil {
					return StatusProjection{}, err
				}
			}
			return StatusProjection{AppliedGeneration: 3, RuntimePhase: "ready", ControlBinding: statusControlBinding(cr)}, nil
		})
		projector := &StatusProjector{Client: kube, APIReader: kube, Source: source}
		if err := projector.Project(ctx, key); err == nil {
			t.Error("old invocation accepted changed ownership")
		}
		got := &crdv1.InferenceService{}
		if err := kube.Get(ctx, key, got); err != nil {
			t.Fatal(err)
		}
		if got.UID != cr.UID || got.Labels[tenantIDLabel] != "tenant-b" || got.Status.RuntimePhase != "" || calls != 1 {
			t.Fatalf("changed ownership received old tenant result: calls=%d object=%+v", calls, got)
		}
	})
}

func waitForObjectGone(ctx context.Context, kube client.Client, key client.ObjectKey) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var current crdv1.InferenceService
		err := kube.Get(ctx, key, &current)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
