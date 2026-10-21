package kubernetes

import (
	"testing"

	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCPUInferenceUsesVolcanoAcrossRuntimeKinds(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "cpu", Namespace: "models", Image: "engine:1", Generation: 1, Replicas: 1,
		Resources: resources.Normalized{Requests: map[string]string{"cpu": "1", "memory": "1Gi"}, Limits: map[string]string{"cpu": "1", "memory": "1Gi"}}}
	deployment, err := Deployment(spec)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Spec.SchedulerName != "volcano" || deployment.Spec.Template.Annotations["scheduling.volcano.sh/queue-name"] != "default" {
		t.Fatalf("CPU deployment did not select Volcano/default: %#v", deployment.Spec.Template)
	}
	for _, mode := range []string{"deployment", "leader_worker_set"} {
		t.Run(mode, func(t *testing.T) {
			input := spec
			input.RuntimeMode, input.WorkerReplicas = mode, 1
			input.ArtifactProvider, input.ModelClaim = "model", "model-claim"
			obj, err := RenderKServeRuntime(input)
			if err != nil {
				t.Fatal(err)
			}
			components := []string{"predictor"}
			if mode == "leader_worker_set" {
				components = []string{"template", "worker"}
			}
			for _, component := range components {
				pod, _, _ := unstructured.NestedMap(obj.Object, "spec", component)
				if pod["schedulerName"] != "volcano" {
					t.Fatalf("%s scheduler = %v, want volcano", component, pod["schedulerName"])
				}
				assertKServeNonRoot(t, pod)
				containers, _, _ := unstructured.NestedSlice(pod, "containers")
				limits, _, _ := unstructured.NestedStringMap(containers[0].(map[string]interface{}), "resources", "limits")
				if len(limits) != 2 || limits["cpu"] != "1" || limits["memory"] != "1Gi" {
					t.Fatalf("CPU resources changed: %#v", limits)
				}
				if component == "predictor" {
					queue, _, _ := unstructured.NestedString(pod, "annotations", "scheduling.volcano.sh/queue-name")
					if queue != "default" {
						t.Fatalf("CPU predictor queue = %q", queue)
					}
				}
			}
		})
	}
	spec.RuntimeMode, spec.WorkerReplicas = "leader_worker_set", 1
	lws, err := LeaderWorkerSet(spec)
	if err != nil {
		t.Fatal(err)
	}
	// LWS uses WorkerTemplate for the leader too when LeaderTemplate is nil.
	if lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.SchedulerName != "volcano" || lws.Spec.LeaderWorkerTemplate.WorkerTemplate.Annotations["scheduling.volcano.sh/queue-name"] != "default" {
		t.Fatalf("CPU LWS did not select Volcano/default: %#v", lws.Spec.LeaderWorkerTemplate)
	}
}

func TestGPUInferenceRejectsNonVolcanoExecution(t *testing.T) {
	request, plan := testGPUPlan(t, "kserve-container")
	plan.Execution.SchedulerName = "default-scheduler"
	_, err := RenderKServeRuntime(RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "gpu", Namespace: "models", Image: "engine:1", Generation: 1, Replicas: 1,
		Resources: resources.Normalized{GPU: request}, GPUSnapshot: plan})
	if err == nil {
		t.Fatal("GPU execution with another scheduler must fail, not override the frozen plan")
	}
	if plan.Execution.SchedulerName != "default-scheduler" {
		t.Fatal("renderer mutated the frozen GPU execution")
	}
}
