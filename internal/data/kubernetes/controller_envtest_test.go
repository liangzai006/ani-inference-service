package kubernetes

import (
	"context"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/stdr"
	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type recordingWorkNotifier struct {
	mu     sync.Mutex
	events []workNotification
}

type envtestStatusSource struct {
	Reader client.Reader
	Key    client.ObjectKey
}

func (s envtestStatusSource) GetStatusProjection(ctx context.Context, _, _ string) (StatusProjection, error) {
	current := &crdv1.InferenceService{}
	if err := s.Reader.Get(ctx, s.Key, current); err != nil {
		return StatusProjection{}, err
	}
	return StatusProjection{
		RuntimePhase: "pending", PublicationPhase: "withdrawn", InvocationHealth: "unknown",
		ControlBinding: statusControlBinding(current),
	}, nil
}

type workNotification struct {
	tenant, service string
	generation      int64
}

func (n *recordingWorkNotifier) Notify(_ context.Context, tenant, service string, generation int64) error {
	n.mu.Lock()
	n.events = append(n.events, workNotification{tenant: tenant, service: service, generation: generation})
	n.mu.Unlock()
	return nil
}

func (n *recordingWorkNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

func TestControllerEnvtestWatchNotifiesDurableBoundary(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run controller-runtime API-server watch test")
	}
	lwsCRDPath := os.Getenv("LWS_CRD_PATH")
	if lwsCRDPath == "" {
		t.Skip("set LWS_CRD_PATH to the official LeaderWorkerSet CRD directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctrlLog.SetLogger(stdr.New(log.New(os.Stderr, "controller-runtime ", log.LstdFlags)))

	useExistingCluster := false
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:  []string{"../../../config/crd/bases", lwsCRDPath},
		UseExistingCluster: &useExistingCluster,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	apiClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apiClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "inference-test"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
		_ = cleanupCtx
	})

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	notifier := &recordingWorkNotifier{}
	status := &StatusProjector{}
	controller := &Controller{Work: notifier, PollInterval: time.Hour, Status: status}
	mgr, err := NewManager(cfg, controller, ctrlmanager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		PprofBindAddress:       "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	status.Source = envtestStatusSource{Reader: mgr.GetAPIReader(), Key: client.ObjectKey{Namespace: "inference-test", Name: "svc"}}
	managerErr := make(chan error, 1)
	go func() { managerErr <- mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("controller-runtime cache did not sync")
	}

	kube := mgr.GetClient()
	cr := &crdv1.InferenceService{ObjectMeta: metav1.ObjectMeta{
		Name: "svc", Namespace: "inference-test",
		Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a"},
	}, Spec: crdv1.InferenceServiceSpec{
		Generation: 1, DesiredState: "running", ModelVersionID: "model-v1",
		Resource: corev1.ResourceRequirements{}, Replicas: 1,
	}}
	if err := kube.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	var cached crdv1.InferenceService
	if err := waitForCachedObject(ctx, kube, client.ObjectKey{Namespace: "inference-test", Name: "svc"}, &cached); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, ctx, notifier, 1)
	if err := waitForStatus(ctx, kube, client.ObjectKey{Namespace: "inference-test", Name: "svc"}, "pending"); err != nil {
		t.Fatalf("status projector did not write status subresource: %v", err)
	}

	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "runtime", Namespace: "inference-test",
		Labels: map[string]string{tenantIDLabel: "tenant-a", serviceIDLabel: "service-a", generationLabel: "1"},
	}, Spec: appsv1.DeploymentSpec{Replicas: ptrInt32(1), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "runtime"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "runtime"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "inference", Image: "example.invalid/inference"}}}}}}
	if err := kube.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, ctx, notifier, 2)

	if err := kube.Delete(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, ctx, notifier, 3)

	select {
	case err := <-managerErr:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("manager stopped unexpectedly: %v", err)
		}
	default:
	}
}

func waitForNotification(t *testing.T, ctx context.Context, notifier *recordingWorkNotifier, want int) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if notifier.count() >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for notification %d, got %d", want, notifier.count())
		case <-ticker.C:
		}
	}
}

func waitForCachedObject(ctx context.Context, kube client.Client, key client.ObjectKey, obj client.Object) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := kube.Get(ctx, key, obj); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForStatus(ctx context.Context, kube client.Client, key client.ObjectKey, phase string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var obj crdv1.InferenceService
		if err := kube.Get(ctx, key, &obj); err == nil && obj.Status.RuntimePhase == phase {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func ptrInt32(v int32) *int32 { return &v }

var _ client.Object = (*crdv1.InferenceService)(nil)
