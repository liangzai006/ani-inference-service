package kubernetes

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrlLog "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
	lwscontrollers "sigs.k8s.io/lws/pkg/controllers"
)

// TestOfficialLWSControllerCreatesStatefulSetEnvtest exercises the official
// LWS controller against an isolated API server. envtest does not run
// kube-controller-manager, so this intentionally verifies the LWS-owned
// StatefulSet handoff; StatefulSet -> Pod creation requires a real cluster.
func TestOfficialLWSControllerCreatesStatefulSetEnvtest(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" || os.Getenv("LWS_CRD_PATH") == "" {
		t.Skip("set KUBEBUILDER_ASSETS and LWS_CRD_PATH to run official LWS controller envtest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctrlLog.SetLogger(logr.Discard())
	useExisting := false
	testEnv := &envtest.Environment{
		UseExistingCluster:    &useExisting,
		CRDDirectoryPaths:     []string{os.Getenv("LWS_CRD_PATH")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testEnv.Stop() })

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := lwsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrlmanager.New(cfg, ctrlmanager.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		PprofBindAddress:       "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := lwscontrollers.SetupIndexes(mgr.GetFieldIndexer()); err != nil {
		t.Fatal(err)
	}
	reconciler := lwscontrollers.NewLeaderWorkerSetReconciler(mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorder("lws"))
	if err := reconciler.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	managerErr := make(chan error, 1)
	go func() { managerErr <- mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("LWS controller cache did not sync")
	}

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lws-controller-test"}}
	if err := mgr.GetClient().Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	lws, err := LeaderWorkerSet(RuntimeSpec{
		TenantID: "tenant-a", ServiceID: "service-a", Name: ns.Name, Namespace: ns.Name,
		Image: "example.invalid/inference:v1", Generation: 1, Replicas: 1, WorkerReplicas: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.GetClient().Create(ctx, lws); err != nil {
		t.Fatal(err)
	}

	key := client.ObjectKey{Namespace: ns.Name, Name: lws.Name}
	deadline := time.NewTicker(50 * time.Millisecond)
	defer deadline.Stop()
	for {
		var stsList appsv1.StatefulSetList
		err := mgr.GetAPIReader().List(ctx, &stsList, client.InNamespace(ns.Name), client.MatchingLabels{lwsv1.SetNameLabelKey: lws.Name})
		if err == nil && len(stsList.Items) == 1 {
			sts := &stsList.Items[0]
			if sts.Spec.Replicas != nil && *sts.Spec.Replicas == 1 && sts.Spec.ServiceName != "" {
				return
			}
		}
		select {
		case err := <-managerErr:
			t.Fatalf("LWS manager exited before StatefulSet creation: %v", err)
		case <-ctx.Done():
			t.Fatalf("LWS controller did not create StatefulSet for %s: %v", key, err)
		case <-deadline.C:
		}
	}
}
