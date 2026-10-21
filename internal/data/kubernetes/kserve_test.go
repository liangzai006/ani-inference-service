package kubernetes

import (
	"context"
	"fmt"
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func testGPUPlan(t *testing.T, container string) (*gpu.Request, *gpu.Snapshot) {
	t.Helper()
	request := &gpu.Request{ClusterID: "11111111-1111-4111-8111-111111111111", PoolID: "22222222-2222-4222-8222-222222222222", ProfileID: "33333333-3333-4333-8333-333333333333", ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 1, ContainerName: container}
	snapshot := &gpu.Snapshot{Request: request, Execution: &gpu.Execution{SchedulerName: "volcano", QueueName: "inference", NodeSelector: map[string]string{"accelerator.ani.io/baseline-id": "baseline-a", "accelerator.ani.io/model-key": "model"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1"}, RuntimeClassName: "gpu-runtime", ExecutionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Display: &gpu.Display{ProfileID: request.ProfileID, ProfileVersion: 1, ProfileName: "shared", ModelKey: "model", Mode: 2}, ExecutionDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	return request, snapshot
}

func assertKServeNonRoot(t *testing.T, component map[string]interface{}) {
	t.Helper()
	security, found, err := unstructured.NestedMap(component, "securityContext")
	if err != nil || !found {
		t.Fatalf("securityContext missing: found=%v err=%v", found, err)
	}
	if security["runAsUser"] != int64(65534) || security["runAsGroup"] != int64(65534) || security["runAsNonRoot"] != true {
		t.Fatalf("securityContext = %#v", security)
	}
	seccomp, ok := security["seccompProfile"].(map[string]interface{})
	if !ok || seccomp["type"] != "RuntimeDefault" {
		t.Fatalf("seccompProfile = %#v", security["seccompProfile"])
	}
	if token, found, err := unstructured.NestedBool(component, "automountServiceAccountToken"); err != nil || !found || token {
		t.Fatalf("automountServiceAccountToken = %v found=%v err=%v", token, found, err)
	}
	containers, found, err := unstructured.NestedSlice(component, "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v", containers)
	}
	container, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("container type = %T", containers[0])
	}
	containerSecurity, ok := container["securityContext"].(map[string]interface{})
	if !ok || containerSecurity["allowPrivilegeEscalation"] != false {
		t.Fatalf("container securityContext = %#v", container["securityContext"])
	}
	drops, ok := containerSecurity["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("container capabilities = %#v", containerSecurity["capabilities"])
	}
	drop, ok := drops["drop"].([]interface{})
	if !ok || len(drop) != 1 || drop[0] != "ALL" {
		t.Fatalf("capability drop = %#v", drops["drop"])
	}
}

func TestRenderKServeWrapsRawEngineCommandAndModelMount(t *testing.T) {
	obj, err := renderKServeInferenceService(RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", Image: "registry/vllm:1",
		ArtifactProvider: "model", ModelClaim: "ani-model-claim", Generation: 4, Replicas: 1,
		ServiceAccountName: "ani-inference-runtime",
		EngineCommand:      `/usr/bin/env VLLM_USE_V1=1 vllm serve '/models' --max-model-len 8192 --served-model-name custom-qwen`,
		Resources:          resources.Normalized{Requests: map[string]string{"cpu": "2", "memory": "8Gi"}, Limits: map[string]string{"memory": "12Gi"}},
		Endpoint:           &EndpointSpec{ContainerPort: 8080, ServicePort: 80, Protocol: "TCP"},
	})
	if err != nil {
		t.Fatalf("renderKServeInferenceService() error = %v", err)
	}
	if got := obj.GetAnnotations()["serving.kserve.io/deploymentMode"]; got != "Standard" {
		t.Fatalf("deployment mode = %q", got)
	}
	if got := obj.GetAnnotations()["serving.kserve.io/autoscalerClass"]; got != "external" {
		t.Fatalf("autoscaler class = %q", got)
	}
	predictor, found, err := unstructured.NestedMap(obj.Object, "spec", "predictor")
	if err != nil || !found {
		t.Fatalf("predictor = %#v, found=%v, err=%v", predictor, found, err)
	}
	if got, _ := predictor["serviceAccountName"].(string); got != "ani-inference-runtime" {
		t.Fatalf("predictor service account = %q, want ani-inference-runtime", got)
	}
	assertKServeNonRoot(t, predictor)
	containers, found, err := unstructured.NestedSlice(predictor, "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v, found=%v, err=%v", containers, found, err)
	}
	container, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("container type = %T", containers[0])
	}
	if container["name"] != "kserve-container" {
		t.Fatalf("container name = %v; want KServe's canonical name", container["name"])
	}
	limits, found, err := unstructured.NestedMap(container, "resources", "limits")
	if err != nil || !found || limits["cpu"] != "2" || limits["memory"] != "12Gi" {
		t.Fatalf("KServe resource limits = %#v, want request-only cpu mirrored and explicit memory limit", limits)
	}
	command, found, err := unstructured.NestedStringSlice(container, "command")
	if err != nil || !found {
		t.Fatalf("command = %#v, found=%v, err=%v", command, found, err)
	}
	wantCommand := []string{"/bin/sh", "-c"}
	if len(command) != len(wantCommand) {
		t.Fatalf("command = %#v, want %#v", command, wantCommand)
	}
	for i := range wantCommand {
		if command[i] != wantCommand[i] {
			t.Fatalf("command = %#v, want %#v", command, wantCommand)
		}
	}
	args, found, err := unstructured.NestedStringSlice(container, "args")
	if err != nil || !found {
		t.Fatalf("args = %#v, found=%v, err=%v", args, found, err)
	}
	wantArgs := []string{`/usr/bin/env VLLM_USE_V1=1 vllm serve '/models' --max-model-len 8192 --served-model-name custom-qwen`}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("args = %#v, want %#v", args, wantArgs)
		}
	}
	mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
	if err != nil || !found || len(mounts) != 3 {
		t.Fatalf("volumeMounts = %#v, found=%v, err=%v", mounts, found, err)
	}
	volumes, found, err := unstructured.NestedSlice(predictor, "volumes")
	if err != nil || !found || len(volumes) != 3 {
		t.Fatalf("volumes = %#v, found=%v, err=%v", volumes, found, err)
	}
}

func TestRenderKServeMountsModelWithoutExplicitEndpoint(t *testing.T) {
	obj, err := renderKServeInferenceService(RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", Image: "registry/vllm:1",
		ArtifactProvider: "model", ModelClaim: "ani-model-claim", Generation: 1, Replicas: 1,
		EngineCommand: "exec vllm serve /models --served-model-name qwen",
	})
	if err != nil {
		t.Fatalf("renderKServeInferenceService() without endpoint error = %v", err)
	}
	predictor, found, err := unstructured.NestedMap(obj.Object, "spec", "predictor")
	if err != nil || !found {
		t.Fatalf("predictor = %#v, found=%v, err=%v", predictor, found, err)
	}
	containers, found, err := unstructured.NestedSlice(predictor, "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v, found=%v, err=%v", containers, found, err)
	}
	container, ok := containers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("container type = %T", containers[0])
	}
	if mounts, found, err := unstructured.NestedSlice(container, "volumeMounts"); err != nil || !found || len(mounts) != 3 {
		t.Fatalf("volumeMounts = %#v, found=%v, err=%v", mounts, found, err)
	}
	if _, found, err := unstructured.NestedSlice(predictor, "volumes"); err != nil || !found {
		t.Fatalf("predictor volumes missing: found=%v, err=%v", found, err)
	}
	if _, found, err := unstructured.NestedFieldNoCopy(container, "startupProbe"); err != nil || found {
		t.Fatalf("startupProbe = found=%v, err=%v; endpoint-less KServe rendering must not invent a probe", found, err)
	}
}

func TestRenderKServeAppliesFrozenGPUPlan(t *testing.T) {
	request, plan := testGPUPlan(t, "kserve-container")
	obj, err := renderKServeInferenceService(RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "gpu-model", Namespace: "models", Image: "registry/vllm:1",
		ArtifactProvider: "model", ModelClaim: "ani-model-claim", Generation: 4, Replicas: 1,
		Resources: resources.Normalized{GPU: request}, GPUSnapshot: plan,
		Endpoint: &EndpointSpec{ContainerPort: 8080, ServicePort: 80, Protocol: corev1.ProtocolTCP},
	})
	if err != nil {
		t.Fatalf("render GPU KServe service: %v", err)
	}
	predictor, found, err := unstructured.NestedMap(obj.Object, "spec", "predictor")
	if err != nil || !found {
		t.Fatalf("predictor = %#v, found=%v, err=%v", predictor, found, err)
	}
	if got, _ := predictor["schedulerName"].(string); got != "volcano" {
		t.Fatalf("schedulerName = %q", got)
	}
	assertKServeNonRoot(t, predictor)
	nodeSelector, found, err := unstructured.NestedMap(predictor, "nodeSelector")
	if err != nil || !found || nodeSelector["accelerator.ani.io/baseline-id"] != "baseline-a" {
		t.Fatalf("nodeSelector = %#v, found=%v, err=%v", nodeSelector, found, err)
	}
	if got, _ := predictor["runtimeClassName"].(string); got != "gpu-runtime" {
		t.Fatalf("runtimeClassName = %q", got)
	}
	containers, found, err := unstructured.NestedSlice(predictor, "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v, found=%v, err=%v", containers, found, err)
	}
	containerMap := containers[0].(map[string]interface{})
	limits, found, err := unstructured.NestedMap(containerMap, "resources", "limits")
	if err != nil || !found || limits["volcano.sh/vgpu-number"] != "1" {
		t.Fatalf("limits = %#v, found=%v, err=%v", limits, found, err)
	}
	annotations, found, err := unstructured.NestedMap(predictor, "annotations")
	if err != nil || !found || annotations["volcano.sh/vgpu-mode"] != "hami-core" {
		t.Fatalf("pod annotations = %#v, found=%v, err=%v", annotations, found, err)
	}
	if annotations["scheduling.volcano.sh/queue-name"] != "inference" {
		t.Fatalf("queue was not delivered to Volcano PodGroup contract: %#v", annotations)
	}
}

func TestRenderKServeRejectsGPUWithoutPlan(t *testing.T) {
	request, _ := testGPUPlan(t, "kserve-container")
	_, err := renderKServeInferenceService(RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "gpu-model", Namespace: "models", Image: "registry/vllm:1", Generation: 1, Replicas: 1, Resources: resources.Normalized{GPU: request}})
	if err == nil {
		t.Fatal("render GPU KServe service error = nil; want missing frozen plan")
	}
}

func TestRenderKServeLLMRejectsGPUUntilSynchronousProjectionExists(t *testing.T) {
	request, plan := testGPUPlan(t, "main")
	request.Replicas = 2 // one leader plus one worker in the single LWS group
	plan.Request.Replicas = request.Replicas
	digest, err := gpu.SnapshotDigest(plan)
	if err != nil {
		t.Fatalf("GPU plan digest: %v", err)
	}
	plan.ExecutionDigest = digest
	_, err = renderKServeLLMInferenceService(RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "gpu-distributed", Namespace: "models", Image: "registry/vllm:1",
		ArtifactProvider: "model", ModelClaim: "ani-model-claim", Generation: 4, Replicas: 1, WorkerReplicas: 1,
		RuntimeMode: "leader_worker_set", Resources: resources.Normalized{GPU: request}, GPUSnapshot: plan,
		Endpoint: &EndpointSpec{ContainerPort: KServeLLMWorkloadServicePort, ServicePort: KServeLLMWorkloadServicePort, TargetPort: intstr.FromInt32(KServeLLMWorkloadServicePort), Protocol: corev1.ProtocolTCP},
	})
	if err == nil {
		t.Fatal("GPU LLMI rendered despite unsupported queue and pod annotation propagation")
	}
}

func TestKServePodPlacementRejectsDroppedGPUFields(t *testing.T) {
	target := "gpu-runtime"
	expected := map[string]interface{}{
		"schedulerName":    "volcano",
		"nodeSelector":     map[string]interface{}{"accelerator.ani.io/baseline-id": "baseline-a"},
		"runtimeClassName": target,
		"annotations":      map[string]interface{}{"volcano.sh/vgpu-mode": "hami-core"},
	}
	runtimeClass := "different-runtime"
	actual := &corev1.PodSpec{SchedulerName: "volcano", NodeSelector: map[string]string{"accelerator.ani.io/baseline-id": "baseline-a"}, RuntimeClassName: &runtimeClass}
	if kservePodPlacementMatches(expected, actual, map[string]string{"volcano.sh/vgpu-mode": "hami-core"}) {
		t.Fatal("placement match = true; want runtime class mismatch")
	}
	runtimeClass = target
	if !kservePodPlacementMatches(expected, actual, map[string]string{"volcano.sh/vgpu-mode": "hami-core"}) {
		t.Fatal("placement match = false; want all frozen fields to match")
	}
}

func TestRenderKServeLeaderWorkerSetUsesLLMInferenceService(t *testing.T) {
	obj, err := RenderKServeRuntime(RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "distributed", Namespace: "models", Image: "registry/vllm:1",
		ArtifactProvider: "model", ModelClaim: "ani-model-claim", Generation: 1, Replicas: 1, WorkerReplicas: 1,
		RuntimeMode: "leader_worker_set", ServedModelName: "qwen", ServiceAccountName: "ani-inference-runtime",
		EngineCommand: "vllm serve /models --served-model-name qwen",
	})
	if err != nil {
		t.Fatalf("renderKServeRuntime() error = %v", err)
	}
	if obj.GetKind() != "LLMInferenceService" || obj.GetAPIVersion() != "serving.kserve.io/v1alpha1" {
		t.Fatalf("object = %s/%s; want serving.kserve.io/v1alpha1 LLMInferenceService", obj.GetAPIVersion(), obj.GetKind())
	}
	for _, path := range [][]string{{"spec", "template", "containers"}, {"spec", "worker", "containers"}} {
		containers, found, err := unstructured.NestedSlice(obj.Object, path...)
		if err != nil || !found || len(containers) != 1 {
			t.Fatalf("containers at %v = %#v, found=%v, err=%v", path, containers, found, err)
		}
		container, ok := containers[0].(map[string]interface{})
		if !ok {
			t.Fatalf("container at %v has type %T", path, containers[0])
		}
		command, found, err := unstructured.NestedStringSlice(container, "command")
		if err != nil || !found || len(command) != 2 || command[0] != "/bin/sh" || command[1] != "-c" {
			t.Fatalf("command at %v = %#v, found=%v, err=%v", path, command, found, err)
		}
		args, found, err := unstructured.NestedStringSlice(container, "args")
		if err != nil || !found || len(args) != 1 || args[0] != "vllm serve /models --served-model-name qwen" {
			t.Fatalf("args at %v = %#v, found=%v, err=%v", path, args, found, err)
		}
	}
	parallelism, found, err := unstructured.NestedMap(obj.Object, "spec", "parallelism")
	if err != nil || !found || parallelism["pipeline"] != int64(2) {
		t.Fatalf("parallelism = %#v, found=%v, err=%v; want pipeline=2", parallelism, found, err)
	}
	for _, path := range [][]string{{"spec", "template"}, {"spec", "worker"}} {
		pod, found, err := unstructured.NestedMap(obj.Object, path...)
		if err != nil || !found || pod["serviceAccountName"] != "ani-inference-runtime" {
			t.Fatalf("service account at %v = %#v, found=%v, err=%v", path, pod["serviceAccountName"], found, err)
		}
		assertKServeNonRoot(t, pod)
	}
	model, found, err := unstructured.NestedMap(obj.Object, "spec", "model")
	if err != nil || !found || model["uri"] != "pvc://ani-model-claim/data" || model["name"] != "qwen" {
		t.Fatalf("model = %#v, found=%v, err=%v", model, found, err)
	}
	volumes, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "volumes")
	if err != nil || !found || len(volumes) != 3 {
		t.Fatalf("template volumes = %#v, found=%v, err=%v; want model and shm mounts", volumes, found, err)
	}
}

func TestKServeReadyUsesReadyCondition(t *testing.T) {
	obj := newKServeInferenceService("models", "model")
	obj.SetGeneration(1)
	obj.Object["status"] = map[string]interface{}{"observedGeneration": int64(1), "conditions": []interface{}{
		map[string]interface{}{"type": "Ready", "status": "False", "reason": "PredictorPending"},
	}}
	ready, reason := kserveReady(obj)
	if ready || reason != "PredictorPending" {
		t.Fatalf("kserveReady() = %v, %q", ready, reason)
	}
	obj.Object["status"].(map[string]interface{})["conditions"] = []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}
	ready, reason = kserveReady(obj)
	if !ready || reason != "" {
		t.Fatalf("kserveReady() = %v, %q; want ready", ready, reason)
	}
}

func TestKServeRuntimeWaitsForCurrentPredictorRollout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modify    func(*unstructured.Unstructured, *appsv1.Deployment)
		missing   bool
		wantReady bool
	}{
		{name: "current ready predictor", wantReady: true},
		{name: "CR generation differs from deployment", wantReady: true, modify: func(obj *unstructured.Unstructured, _ *appsv1.Deployment) {
			obj.SetGeneration(4)
		}},
		{name: "ready deployment still has previous image", modify: func(_ *unstructured.Unstructured, dep *appsv1.Deployment) {
			dep.Spec.Template.Spec.Containers[0].Image = "engine:v1"
		}},
		{name: "stale KServe status", modify: func(obj *unstructured.Unstructured, _ *appsv1.Deployment) {
			_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "observedGeneration")
		}},
		{name: "missing predictor", missing: true},
		{name: "stale deployment status", modify: func(_ *unstructured.Unstructured, dep *appsv1.Deployment) {
			dep.Status.ObservedGeneration = 1
		}},
		{name: "old revision still ready", modify: func(_ *unstructured.Unstructured, dep *appsv1.Deployment) {
			dep.Status.UpdatedReplicas = 0
		}},
		{name: "new revision not available", modify: func(_ *unstructured.Unstructured, dep *appsv1.Deployment) {
			dep.Status.AvailableReplicas = 0
		}},
		{name: "unrelated deployment", modify: func(_ *unstructured.Unstructured, dep *appsv1.Deployment) {
			dep.OwnerReferences[0].UID = "other-isvc"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", Image: "engine:v2", Generation: 2, Replicas: 1}
			obj, err := renderKServeInferenceService(spec)
			if err != nil {
				t.Fatal(err)
			}
			obj.SetUID("isvc-uid")
			obj.SetResourceVersion("12")
			obj.SetGeneration(2)
			obj.Object["status"] = map[string]interface{}{"observedGeneration": int64(2), "conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}}
			replicas, controller := int32(1), true
			dep := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: KServePredictorServiceName(spec.Name), Namespace: spec.Namespace, Generation: 2, OwnerReferences: []metav1.OwnerReference{{APIVersion: "serving.kserve.io/v1beta1", Kind: "InferenceService", Name: spec.Name, UID: types.UID("isvc-uid"), Controller: &controller}}},
				Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
				Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, ReadyReplicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
			}
			containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "predictor", "containers")
			var container corev1.Container
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(containers[0].(map[string]interface{}), &container); err != nil {
				t.Fatal(err)
			}
			dep.Spec.Template.Spec.Containers = []corev1.Container{container}
			if tc.modify != nil {
				tc.modify(obj, dep)
			}
			objects := []client.Object{obj}
			if !tc.missing {
				objects = append(objects, dep)
			}
			cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()
			e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec}}}}
			got, err := e.ObserveRuntime(context.Background(), inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "lease"})
			if err != nil {
				t.Fatal(err)
			}
			if got.RuntimeMode != "deployment" {
				t.Errorf("RuntimeMode = %q; want deployment", got.RuntimeMode)
			}
			if got.Ready != tc.wantReady {
				t.Errorf("Ready = %v; want %v (%s)", got.Ready, tc.wantReady, got.Reason)
			}
		})
	}
}

type captureDeleteClient struct {
	client.Client
	policy *metav1.DeletionPropagation
}

func (c *captureDeleteClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	options := client.DeleteOptions{}
	for _, opt := range opts {
		opt.ApplyToDelete(&options)
	}
	c.policy = options.PropagationPolicy
	return nil
}

func TestKServeDeleteUsesForegroundAndAbsenceIsStopped(t *testing.T) {
	spec := DesiredRuntime{RuntimeSpec: RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", RuntimeMode: "deployment", Generation: 1}, Bindings: []RuntimeBinding{{Kind: KServeInferenceServiceKind, Namespace: "models", Name: "model", UID: "isvc-uid", ResourceVersion: "7", Role: "runtime"}}}
	obj := newKServeInferenceService("models", "model")
	obj.SetUID(types.UID("isvc-uid"))
	obj.SetResourceVersion("7")
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(obj).Build()
	cl := &captureDeleteClient{Client: base}
	e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{Client: cl}}
	if err := e.deleteKServe(context.Background(), spec.Bindings[0]); err != nil {
		t.Fatal(err)
	}
	if cl.policy == nil || *cl.policy != metav1.DeletePropagationForeground {
		t.Fatalf("delete propagation policy = %v; want foreground", cl.policy)
	}
	if err := base.Delete(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	got, err := e.observeKServeAbsence(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimePhase != "stopped" || got.RuntimeMode != "deployment" {
		t.Fatalf("absence observation = %#v; want stopped deployment", got)
	}
}

func TestKServeDeleteWithoutBindingAllowsKnownAbsence(t *testing.T) {
	spec := DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "missing-model", Namespace: "models",
		RuntimeMode: "deployment", Generation: 1,
	}, DesiredState: "stopped", PublicationWithdrawn: true}
	apiReader := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	// Keep the cached client separate from the uncached reader. A missing
	// object must be confirmed against the APIReader before deletion proceeds.
	cachedClient := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{
		Client: cachedClient, APIReader: apiReader, Source: fakeRuntimeSource{desired: spec},
	}}
	op := inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "lease"}
	if err := e.DeleteRuntime(context.Background(), op); err != nil {
		t.Fatalf("DeleteRuntime() error = %v; want known absence to be safe", err)
	}
	observation, err := e.ObserveAbsence(context.Background(), op)
	if err != nil {
		t.Fatalf("ObserveAbsence() error = %v", err)
	}
	if !observation.Absent || observation.RuntimePhase != "stopped" {
		t.Fatalf("absence observation = %+v; want stopped/absent", observation)
	}
}

func TestKServeDeleteWithoutBindingUsesAPIReaderAndDoesNotAdopt(t *testing.T) {
	spec := DesiredRuntime{RuntimeSpec: RuntimeSpec{
		TenantID: "tenant", ServiceID: "service", Name: "unbound-model", Namespace: "models",
		RuntimeMode: "deployment", Generation: 1,
	}, DesiredState: "stopped", PublicationWithdrawn: true}
	obj := newKServeInferenceService(spec.Namespace, spec.Name)
	obj.SetUID(types.UID("unbound-uid"))
	obj.SetResourceVersion("7")
	// The cache deliberately does not contain the object. The APIReader does,
	// proving the deletion guard cannot trust a stale cache or adopt by name.
	cachedClient := fake.NewClientBuilder().WithScheme(executorScheme(t)).Build()
	apiReader := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(obj).Build()
	e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{
		Client: cachedClient, APIReader: apiReader, Source: fakeRuntimeSource{desired: spec},
	}}
	op := inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "lease"}
	if err := e.DeleteRuntime(context.Background(), op); err == nil {
		t.Fatal("DeleteRuntime() succeeded for an existing unbound KServe object")
	}
	current := newKServeInferenceService(spec.Namespace, spec.Name)
	if err := apiReader.Get(context.Background(), client.ObjectKeyFromObject(obj), current); err != nil {
		t.Fatalf("APIReader object disappeared: %v", err)
	}
	if current.GetDeletionTimestamp() != nil {
		t.Fatal("unbound KServe object was marked for deletion")
	}
	if _, err := e.ObserveAbsence(context.Background(), op); err == nil {
		t.Fatal("ObserveAbsence() accepted an existing unbound KServe object")
	}
}

func TestKServeDeleteRemovesRawDeploymentAnnotationBeforeDelete(t *testing.T) {
	spec := DesiredRuntime{RuntimeSpec: RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", RuntimeMode: "deployment", Generation: 1}, Bindings: []RuntimeBinding{{Kind: KServeInferenceServiceKind, Namespace: "models", Name: "model", UID: "isvc-uid", ResourceVersion: "7", Role: "runtime"}}}
	obj := newKServeInferenceService("models", "model")
	obj.SetUID(types.UID("isvc-uid"))
	obj.SetResourceVersion("7")
	obj.SetAnnotations(map[string]string{"serving.kserve.io/deploymentMode": "RawDeployment"})
	base := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(obj).Build()
	cl := &captureDeleteClient{Client: base}
	e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{Client: cl}}
	if err := e.deleteKServe(context.Background(), spec.Bindings[0]); err != nil {
		t.Fatal(err)
	}
	current := newKServeInferenceService("models", "model")
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(current), current); err != nil {
		t.Fatal(err)
	}
	if _, found := current.GetAnnotations()["serving.kserve.io/deploymentMode"]; found {
		t.Fatal("RawDeployment annotation must be removed before KServe deletion")
	}
}

func TestKServeLLMRuntimeWaitsForCurrentLeaderWorkerSet(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		mutate                  func(*unstructured.Unstructured, *lwsv1.LeaderWorkerSet, []*corev1.Pod)
		missing                 bool
		wantReady               bool
		wantGroups, wantWorkers int32
	}{
		{name: "ready groups and workers", wantReady: true, wantGroups: 2, wantWorkers: 2},
		{name: "old LLM status", mutate: func(obj *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			_ = unstructured.SetNestedField(obj.Object, int64(1), "status", "observedGeneration")
		}},
		{name: "missing LWS", missing: true},
		{name: "unrelated LWS", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.OwnerReferences[0].UID = "other"
		}},
		{name: "old leader image", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Image = "engine:v1"
		}},
		{name: "old worker command", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Command = []string{"old"}
		}},
		{name: "injected worker args", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Args = []string{"--unexpected"}
		}},
		{name: "old group size", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			*lws.Spec.LeaderWorkerTemplate.Size = 3
		}},
		{name: "stale LWS status", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Status.ObservedGeneration = 1
		}},
		{name: "old groups still ready", mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Status.UpdatedReplicas = 1
		}},
		{name: "missing ready group", wantGroups: 1, wantWorkers: 2, mutate: func(_ *unstructured.Unstructured, lws *lwsv1.LeaderWorkerSet, _ []*corev1.Pod) {
			lws.Status.ReadyReplicas = 1
		}},
		{name: "unready worker", wantGroups: 2, wantWorkers: 1, mutate: func(_ *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, pods []*corev1.Pod) {
			pods[1].Status.Conditions[0].Status = corev1.ConditionFalse
		}},
		{name: "old worker image", wantGroups: 2, wantWorkers: 1, mutate: func(_ *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, pods []*corev1.Pod) {
			pods[1].Spec.Containers[0].Image = "engine:v1"
		}},
		{name: "duplicate worker cannot fill other group", wantGroups: 2, wantWorkers: 1, mutate: func(_ *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, pods []*corev1.Pod) {
			pods[1].Labels[lwsv1.GroupIndexLabelKey] = "0"
		}},
		{name: "finished worker", wantGroups: 2, wantWorkers: 1, mutate: func(_ *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, pods []*corev1.Pod) {
			pods[1].Status.Phase = corev1.PodSucceeded
		}},
		{name: "terminating worker", wantGroups: 2, wantWorkers: 1, mutate: func(_ *unstructured.Unstructured, _ *lwsv1.LeaderWorkerSet, pods []*corev1.Pod) {
			now := metav1.Now()
			pods[1].DeletionTimestamp = &now
			pods[1].Finalizers = []string{"test/hold"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "model", Namespace: "models", Image: "engine:v2", Generation: 2, Replicas: 2, WorkerReplicas: 1, RuntimeMode: "leader_worker_set", ArtifactProvider: "model", ModelClaim: "model-pvc", EngineCommand: "/requested-command caller-arg"}
			obj, err := RenderKServeRuntime(spec)
			if err != nil {
				t.Fatal(err)
			}
			obj.SetUID("llmisvc-uid")
			obj.SetResourceVersion("12")
			obj.SetGeneration(2)
			obj.Object["status"] = map[string]interface{}{"observedGeneration": int64(2), "conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}}
			replicas, size, controller := int32(2), int32(2), true
			lws := &lwsv1.LeaderWorkerSet{
				ObjectMeta: metav1.ObjectMeta{Name: "model-kserve-mn", Namespace: spec.Namespace, UID: "lws-uid", Generation: 3, OwnerReferences: []metav1.OwnerReference{{APIVersion: "serving.kserve.io/v1alpha1", Kind: "LLMInferenceService", UID: obj.GetUID(), Name: spec.Name, Controller: &controller}}},
				Spec:       lwsv1.LeaderWorkerSetSpec{Replicas: &replicas, LeaderWorkerTemplate: lwsv1.LeaderWorkerTemplate{Size: &size, LeaderTemplate: &corev1.PodTemplateSpec{}}},
				Status:     lwsv1.LeaderWorkerSetStatus{ObservedGeneration: 3, Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 2},
			}
			for _, target := range []struct {
				name string
				pod  *corev1.PodSpec
			}{{"template", &lws.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec}, {"worker", &lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec}} {
				value, _, _ := unstructured.NestedMap(obj.Object, "spec", target.name)
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(value, target.pod); err != nil {
					t.Fatal(err)
				}
			}
			pods := make([]*corev1.Pod, 2)
			for i := range pods {
				pods[i] = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", i), Namespace: spec.Namespace, Labels: map[string]string{lwsv1.SetNameLabelKey: lws.Name, lwsv1.GroupIndexLabelKey: fmt.Sprint(i), lwsv1.WorkerIndexLabelKey: "1"}}, Spec: *lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			}
			if tc.mutate != nil {
				tc.mutate(obj, lws, pods)
			}
			objects := []client.Object{obj}
			if !tc.missing {
				objects = append(objects, lws)
			}
			for _, pod := range pods {
				objects = append(objects, pod)
			}
			cl := fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()
			e := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{Client: cl, Source: fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec}}}}
			got, err := e.ObserveRuntime(context.Background(), inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "lease"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Ready != tc.wantReady {
				t.Errorf("Ready = %v, want %v (%s)", got.Ready, tc.wantReady, got.Reason)
			}
			if got.ReadyGroups != tc.wantGroups || got.ReadyReplicas != tc.wantGroups || got.ReadyWorkers != tc.wantWorkers {
				t.Errorf("groups/replicas/workers = %d/%d/%d, want %d/%d/%d", got.ReadyGroups, got.ReadyReplicas, got.ReadyWorkers, tc.wantGroups, tc.wantGroups, tc.wantWorkers)
			}
			if tc.wantReady && got.LWSUID != "lws-uid" {
				t.Errorf("LWSUID = %q", got.LWSUID)
			}
		})
	}
}

func TestKServeContainerMatchAllowsControllerInjectedFields(t *testing.T) {
	expected := corev1.Container{
		Name:    "main",
		Image:   "engine:v1",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{"vllm serve /models"},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
		VolumeMounts: []corev1.VolumeMount{{Name: "model", MountPath: "/models", ReadOnly: true}},
	}
	actual := *expected.DeepCopy()
	actual.Env = []corev1.EnvVar{{Name: "LWS_WORKER_INDEX", Value: "1"}}
	actual.VolumeMounts = append(actual.VolumeMounts, corev1.VolumeMount{Name: "kserve-pvc-source", MountPath: "/mnt/models", ReadOnly: true, SubPath: "data"})
	actual.ImagePullPolicy = corev1.PullIfNotPresent
	if !kserveContainerMatches(expected, actual) {
		t.Fatal("KServe controller-injected environment, mount and defaults should be accepted")
	}
	actual.Command = []string{"old-command"}
	if kserveContainerMatches(expected, actual) {
		t.Fatal("a changed caller command must be rejected")
	}
}
