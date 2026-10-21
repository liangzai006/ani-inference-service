# Volcano and Inference Non-root Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep Volcano as an independently installed scheduler and make every workload created by Inference run as a non-root image-defined UID/GID (65534, the `nobody` user) without changing the caller's engine command, then verify the CPU inference path in the new cluster.

**Architecture:** Add one Kubernetes adapter helper that applies the shared pod/container security contract. Use it in the legacy Deployment/LWS renderers, KServe InferenceService/LLMInferenceService projections, and the model materializer Job. Keep the control-plane ServiceAccount able to access Kubernetes while disabling token mounting for runtime Pods and materializer Jobs; keep CPU on the default scheduler and project Volcano only from a validated GPU execution snapshot.

**Tech Stack:** Go 1.26.9 module, client-go/corev1 typed objects, KServe unstructured rendering, LeaderWorkerSet, Kubernetes Jobs, Helm 3, Volcano 1.12.1, existing CPU vLLM and model-import-worker images.

## Global Constraints

- The repository gateway boundary remains unchanged: Higress only serves OpenAI-compatible `/v1/...`; Governance serves management `/api/v1/...`.
- Volcano remains a separately installed platform component and is not added to `charts/ani-inference`.
- Every Inference-created runtime workload and materializer Job uses `runAsUser: 65534`, `runAsGroup: 65534`, `runAsNonRoot: true`, `fsGroup: 65534`, and `seccompProfile.type: RuntimeDefault`; the control-plane Deployment receives the same pod/container hardening but keeps its Kubernetes API token.
- Every Inference-created container uses `allowPrivilegeEscalation: false` and drops capability `ALL`.
- Runtime Pods and materializer Jobs set `automountServiceAccountToken: false`; the control-plane Deployment keeps `automountServiceAccountToken: true` because it calls the Kubernetes API.
- Do not set `readOnlyRootFilesystem`; preserve writable `/tmp` and `/dev/shm` mounts needed by inference engines and import scripts.
- Preserve the complete caller-provided engine command byte-for-byte; do not parse it, append flags, or convert environment variables into flags.
- Do not modify `/root/kubercon/ANI`, Model, Governance, Accelerator, or Console repositories.
- Preserve the three pre-existing uncommitted certificate changes in `charts/ani-inference/templates/certificate.yaml`, `charts/ani-inference/templates/server-certificate.yaml`, and `charts/ani-inference/values.yaml`; stage only security-context hunks from the values file if needed.
- The cluster has no GPU nodes, so GPU validation is limited to rendering and Volcano control-plane readiness; do not claim GPU end-to-end success.

---

### Task 1: Add and test the shared non-root security helper

**Files:**
- Create: `internal/data/kubernetes/security.go`
- Create: `internal/data/kubernetes/security_test.go`

**Interfaces:**
- Produces `func ApplyNonRootSecurity(pod *corev1.PodSpec)` for all renderers and the materializer.
- The function mutates only `pod.SecurityContext`, `pod.AutomountServiceAccountToken`, and each existing container's `SecurityContext`; it does not alter containers, commands, args, volumes, resources, scheduler fields, or service account names.

- [ ] **Step 1: Write the failing helper test**

```go
func TestApplyNonRootSecurity(t *testing.T) {
	pod := &corev1.PodSpec{
		ServiceAccountName: "ani-runtime",
		Containers: []corev1.Container{{Name: "engine", Command: []string{"sh"}, Args: []string{"-c", "vllm serve /models"}}},
	}

	ApplyNonRootSecurity(pod)

	if pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 {
		t.Fatalf("RunAsUser = %#v, want 65534", pod.SecurityContext)
	}
	if pod.SecurityContext.RunAsGroup == nil || *pod.SecurityContext.RunAsGroup != 65534 ||
		pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 65534 {
		t.Fatalf("pod security context = %#v, want non-root 65534", pod.SecurityContext)
	}
	if pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("seccomp profile = %#v, want RuntimeDefault", pod.SecurityContext.SeccompProfile)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automount token = %v, want false", pod.AutomountServiceAccountToken)
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("container security context = %#v, want no privilege escalation", container.SecurityContext)
	}
	if len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != corev1.Capability("ALL") {
		t.Fatalf("capability drop = %#v, want ALL", container.SecurityContext.Capabilities.Drop)
	}
	if string(container.Command[0]) != "sh" || container.Args[1] != "vllm serve /models" {
		t.Fatalf("command changed: %v %v", container.Command, container.Args)
	}
}
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `go test ./internal/data/kubernetes -run TestApplyNonRootSecurity -count=1`

Expected: FAIL because `ApplyNonRootSecurity` does not exist.

- [ ] **Step 3: Implement the minimal helper**

```go
package kubernetes

import (
	 corev1 "k8s.io/api/core/v1"
	 "k8s.io/utils/ptr"
)

const inferenceRuntimeUID int64 = 65534

func ApplyNonRootSecurity(pod *corev1.PodSpec) {
	if pod == nil {
		return
	}
	uid := inferenceRuntimeUID
	pod.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser:    ptr.To(uid),
		RunAsGroup:   ptr.To(uid),
		RunAsNonRoot: ptr.To(true),
		FSGroup:      ptr.To(uid),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	pod.AutomountServiceAccountToken = ptr.To(false)
	for index := range pod.Containers {
		pod.Containers[index].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{corev1.Capability("ALL")},
			},
		}
	}
}
```

- [ ] **Step 4: Run the helper test**

Run: `gofmt -w internal/data/kubernetes/security.go internal/data/kubernetes/security_test.go && go test ./internal/data/kubernetes -run TestApplyNonRootSecurity -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the isolated helper**

```bash
git add internal/data/kubernetes/security.go internal/data/kubernetes/security_test.go
git commit -m "security: define non-root inference runtime contract"
```

### Task 2: Apply the contract to legacy Deployment and LeaderWorkerSet renderers

**Files:**
- Modify: `internal/data/kubernetes/renderer.go` in `Deployment` and `LeaderWorkerSet`
- Modify: `internal/data/kubernetes/renderer_test.go`

**Interfaces:**
- Consumes `ApplyNonRootSecurity` from Task 1.
- Produces Deployment and LWS objects with identical pod/container security fields and unchanged command/args.

- [ ] **Step 1: Add renderer assertions**

Add a table-driven assertion helper in `renderer_test.go`:

```go
func assertNonRootPod(t *testing.T, pod *corev1.PodSpec) {
	t.Helper()
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 {
		t.Fatalf("pod security context = %#v", pod.SecurityContext)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("automount token = %v", pod.AutomountServiceAccountToken)
	}
	if len(pod.Containers) != 1 || pod.Containers[0].SecurityContext == nil ||
		pod.Containers[0].SecurityContext.AllowPrivilegeEscalation == nil ||
		*pod.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("container security context = %#v", pod.Containers)
	}
}
```

Call it from the existing Deployment and LWS tests and assert their existing command/args still equal the caller input.

- [ ] **Step 2: Run the renderer tests before implementation**

Run: `go test ./internal/data/kubernetes -run 'Test(Deployment|LeaderWorkerSet)' -count=1`

Expected: the new security assertions fail while the pre-existing renderer assertions continue to pass.

- [ ] **Step 3: Apply security after constructing each pod**

In `Deployment`, replace the inline `corev1.PodSpec{...}` construction with a local `pod := corev1.PodSpec{ServiceAccountName: ..., Containers: ...}`, call `ApplyNonRootSecurity(&pod)`, and assign `Spec: pod`. In `LeaderWorkerSet`, call `ApplyNonRootSecurity(&template.Spec)` before `mountModel`.

- [ ] **Step 4: Run focused renderer tests**

Run: `gofmt -w internal/data/kubernetes/renderer.go internal/data/kubernetes/renderer_test.go && go test ./internal/data/kubernetes -run 'Test(Deployment|LeaderWorkerSet)' -count=1`

Expected: PASS, including command-preservation assertions.

- [ ] **Step 5: Commit the renderer changes**

```bash
git add internal/data/kubernetes/renderer.go internal/data/kubernetes/renderer_test.go
git commit -m "security: harden legacy inference renderers"
```

### Task 3: Propagate security to KServe predictor and LLMI templates

**Files:**
- Modify: `internal/data/kubernetes/kserve.go` in `renderKServeInferenceService`, `renderKServeLLMInferenceService`, and `applyGPUSnapshotToPredictor`
- Modify: `internal/data/kubernetes/kserve_test.go`

**Interfaces:**
- Consumes `ApplyNonRootSecurity`.
- Produces KServe unstructured objects whose predictor/template/worker maps contain pod security context, container security context, and `automountServiceAccountToken: false`.

- [ ] **Step 1: Add KServe render assertions**

Add a helper that reads the unstructured maps and checks:

```go
func assertKServeNonRoot(t *testing.T, component map[string]interface{}) {
	t.Helper()
	security, found, err := unstructured.NestedMap(component, "securityContext")
	if err != nil || !found {
		t.Fatalf("securityContext missing: found=%v err=%v", found, err)
	}
	if security["runAsUser"] != int64(65534) || security["runAsGroup"] != int64(65534) ||
		security["runAsNonRoot"] != true {
		t.Fatalf("securityContext = %#v", security)
	}
	if got, _ := security["seccompProfile"].(map[string]interface{}); got["type"] != "RuntimeDefault" {
		t.Fatalf("seccompProfile = %#v", security["seccompProfile"])
	}
	if token, found, err := unstructured.NestedBool(component, "automountServiceAccountToken"); err != nil || !found || token {
		t.Fatalf("automountServiceAccountToken = %v found=%v err=%v", token, found, err)
	}
	containers, found, err := unstructured.NestedSlice(component, "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("containers = %#v", containers)
	}
	container := containers[0].(map[string]interface{})
	containerSecurity := container["securityContext"].(map[string]interface{})
	if containerSecurity["allowPrivilegeEscalation"] != false {
		t.Fatalf("container securityContext = %#v", containerSecurity)
	}
}
```

Use it for the `spec.predictor` map in the InferenceService test and both `spec.template` and `spec.worker` maps in the LLMI test; keep the existing exact command/args assertions.

- [ ] **Step 2: Run the focused KServe tests before implementation**

Run: `go test ./internal/data/kubernetes -run 'TestRenderKServe' -count=1`

Expected: the new assertions fail because security context is not yet projected.

- [ ] **Step 3: Harden the KServe pod before GPU projection**

After setting `pod.ServiceAccountName` in `renderKServeInferenceService`, call `ApplyNonRootSecurity(&pod)`. In `renderKServeLLMInferenceService`, call it immediately after constructing `pod` and before model mounting/GPU projection.

- [ ] **Step 4: Project pod security context**

Extend `applyGPUSnapshotToPredictor` with:

```go
if pod.SecurityContext != nil {
	securityContext, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod.SecurityContext)
	if err == nil {
		predictor["securityContext"] = securityContext
	}
}
if pod.AutomountServiceAccountToken != nil {
	predictor["automountServiceAccountToken"] = *pod.AutomountServiceAccountToken
}
```

Change the helper signature to return `(error)` if conversion fails, and propagate that error from both KServe renderers; do not silently omit a required security field. Preserve existing scheduler, node selector, runtime class, service account, annotations, volumes, and command behavior.

- [ ] **Step 5: Run KServe tests and commit**

Run: `gofmt -w internal/data/kubernetes/kserve.go internal/data/kubernetes/kserve_test.go && go test ./internal/data/kubernetes -run 'TestRenderKServe' -count=1`

Expected: PASS.

```bash
git add internal/data/kubernetes/kserve.go internal/data/kubernetes/kserve_test.go
git commit -m "security: propagate non-root context through KServe"
```

### Task 4: Harden the model materializer Job

**Files:**
- Modify: `internal/data/model/materializer.go`
- Modify: `internal/data/model/materializer_test.go`

**Interfaces:**
- Consumes `kube.ApplyNonRootSecurity`.
- Produces a materializer Job with non-root pod/container security, no service-account token, and a writable `/tmp` `emptyDir` mount while preserving the model PVC and download command.

- [ ] **Step 1: Add materializer Job assertions**

After the first `EnsureModel` call in `TestMaterializerRequiresVerifiedJobBeforeReady`, fetch the created Job and assert:

```go
pod := job.Spec.Template.Spec
if pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 {
	t.Fatalf("pod security context = %#v", pod.SecurityContext)
}
if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
	t.Fatalf("automount token = %v", pod.AutomountServiceAccountToken)
}
if len(pod.Containers) != 1 || pod.Containers[0].SecurityContext == nil ||
	pod.Containers[0].SecurityContext.AllowPrivilegeEscalation == nil ||
	*pod.Containers[0].SecurityContext.AllowPrivilegeEscalation {
	t.Fatalf("container security context = %#v", pod.Containers)
}
tmpFound := false
for _, volume := range pod.Volumes {
	if volume.Name == "tmp" && volume.EmptyDir != nil {
		tmpFound = true
		break
	}
}
if !tmpFound {
	t.Fatalf("volumes = %#v, want writable tmp emptyDir", pod.Volumes)
}
```

- [ ] **Step 2: Run the focused materializer test before implementation**

Run: `go test ./internal/data/model -run TestMaterializerRequiresVerifiedJobBeforeReady -count=1`

Expected: the new security assertions fail.

- [ ] **Step 3: Add the writable temporary directory and security**

When constructing `jobSpec`, add:

```go
Volumes: []corev1.Volume{
	{Name: "model", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}}},
	{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
},
```

and add `corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"}` to the downloader. After optional CA volume setup and before creating the Job, call `kube.ApplyNonRootSecurity(&jobSpec)`. Keep `ServiceAccountName` and `AutomountServiceAccountToken: false`.

- [ ] **Step 4: Run the materializer tests and commit**

Run: `gofmt -w internal/data/model/materializer.go internal/data/model/materializer_test.go && go test ./internal/data/model -run 'TestMaterializer' -count=1`

Expected: PASS.

```bash
git add internal/data/model/materializer.go internal/data/model/materializer_test.go
git commit -m "security: run model materializer as non-root"
```

### Task 5: Set hardened control-plane container defaults in Helm

**Files:**
- Modify: `charts/ani-inference/values.yaml` only in the `podSecurityContext`/`securityContext` block
- Test: `charts/ani-inference/templates/deployment.yaml` via `helm template`

**Interfaces:**
- Produces control-plane containers with no privilege escalation, all Linux capabilities dropped, and RuntimeDefault seccomp while retaining the existing pod identity and Kubernetes API token.

- [ ] **Step 1: Update only the security values**

Keep the existing certificate duration fields untouched and set:

```yaml
podSecurityContext:
  runAsUser: 65534
  runAsGroup: 65534
  runAsNonRoot: true
  fsGroup: 65534
  seccompProfile:
    type: RuntimeDefault

securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop:
      - ALL
```

- [ ] **Step 2: Render and inspect the chart**

Run:

```bash
helm lint charts/ani-inference
helm template ani-inference charts/ani-inference --namespace ani-system \
  --set higress.publicBaseURL=http://higress-gateway.higress-system.svc.cluster.local \
  --set higress.consoleURL=http://higress-console.higress-system.svc.cluster.local \
  --set higress.consoleCredentialsSecret.name=higress-console \
  --set database.existingSecret=inference-database \
  --set model.tls.existingSecret=inference-model-mtls \
  --set serverTLS.existingSecret=ani-inference-server-tls \
  > /tmp/ani-inference-rendered.yaml
rg -n 'runAsUser: 65534|runAsGroup: 65534|runAsNonRoot: true|fsGroup: 65534|RuntimeDefault|allowPrivilegeEscalation: false|drop:' /tmp/ani-inference-rendered.yaml
```

Expected: lint passes and the rendered control-plane Deployment contains all listed security fields; `automountServiceAccountToken: true` remains present for the control-plane Pod.

- [ ] **Step 3: Stage only the security hunk and commit**

Use `git add -p charts/ani-inference/values.yaml` and select only the `podSecurityContext`/`securityContext` hunk. Verify `git diff --cached -- charts/ani-inference/values.yaml` contains no certificate duration changes, then commit:

```bash
git add -p charts/ani-inference/values.yaml
# Select only the podSecurityContext/securityContext hunk; leave the
# pre-existing certificate duration hunks unstaged.
git diff --cached -- charts/ani-inference/values.yaml
git commit -m "security: harden inference control plane chart"
```

### Task 6: Run repository verification and validate Volcano/platform prerequisites

**Files:**
- Modify: no source files
- Evidence: `/tmp/ani-inference-nonroot-verification-<timestamp>.txt`

- [ ] **Step 1: Run focused and repository checks**

Run:

```bash
go test ./internal/data/kubernetes ./internal/data/model
go test ./...
go vet ./...
go build ./...
helm lint charts/ani-inference
```

Expected: all commands exit 0. This repository has no `Makefile`, so `make verify` is not an available gate; use the listed Go and Helm commands instead and record any environment/tool failure exactly.

- [ ] **Step 2: Verify Volcano release and webhook health**

Run:

```bash
kubectl config current-context
helm status volcano -n volcano-system
kubectl get pods -n volcano-system -o wide
kubectl get crd podgroups.scheduling.volcano.sh queues.scheduling.volcano.sh
kubectl get validatingwebhookconfiguration volcano-admission-service
```

Expected: context is `kubernetes-admin@kubekey`, Volcano release is deployed, all three core Pods are Ready, CRDs and admission webhook exist. Record the scheduler ConfigMap name and do not create an alias in this task.

- [ ] **Step 3: Inspect the final source diff**

Run: `git status --short && git diff --check && git diff --cached --check`

Expected: only intended implementation commits are clean; the three pre-existing certificate modifications remain visible as unstaged changes and are not included in the security commits.

### Task 7: Perform real non-root probes and CPU inference integration

**Files:**
- Modify: no source files
- Evidence: `/tmp/ani-inference-real-e2e-<timestamp>/`

- [ ] **Step 1: Run non-root probes with the actual images**

Create two short-lived Pods in `ani-system` using the already verified images:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: ani-inference-nonroot-probe
  namespace: ani-system
spec:
  restartPolicy: Never
  securityContext:
    runAsUser: 65534
    runAsGroup: 65534
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: probe
    image: docker.changqingyun.cn/mirror/vllm-openai-cpu@sha256:4c697ae650eb
    command: ["/bin/sh", "-c"]
    args: ["id; touch /tmp/nonroot-probe && echo tmp-writable"]
    securityContext:
      allowPrivilegeEscalation: false
      capabilities: {drop: ["ALL"]}
```

Run the same probe with `docker.changqingyun.cn/ani/model-import-worker:model-search-20261014-r1`, then delete both Pods. Expected logs contain UID/GID 65534 and `tmp-writable`, and no probe Pods remain.

- [ ] **Step 2: Build and push the updated inference image**

This repository has no Makefile image target, so use the repository Dockerfile directly:

```bash
docker build -t docker.changqingyun.cn/ani/inference-service:nonroot-20261021 .
docker push docker.changqingyun.cn/ani/inference-service:nonroot-20261021
```

Record the pushed digest, and use this tag only through a temporary values override; do not edit the committed chart image tag for the cluster check.

- [ ] **Step 3: Deploy the updated inference chart with Helm only**

Run `helm upgrade --install ani-inference charts/ani-inference --namespace ani-system --create-namespace --set image.repository=docker.changqingyun.cn/ani/inference-service --set image.tag=nonroot-20261021` plus the environment’s existing required values. Do not use `scripts/install.sh`. Wait for the migration Job and control-plane Deployment, then verify its pod security context and `/readyz`.

- [ ] **Step 4: Execute the real CPU lifecycle**

Using the existing verified tenant credentials and the Governance NodePort, perform:

1. import or select a ready model;
2. create one CPU inference service with the complete caller command and `resources.requests/limits` CPU/memory;
3. wait for materializer Job completion, KServe CR, predictor Deployment, Pod readiness, and published Higress route;
4. call the Higress OpenAI endpoint using the model’s `served_model_name`;
5. list the service through Governance `/api/v1/inference/services?page=1&pageSize=10`;
6. delete the inference service and wait until KServe, Pods, materializer resources, and native Higress route are absent;
7. call the Governance list again and record that it returns normally.

Inspect the resulting Pods with `kubectl get pod -o yaml` and record UID/GID, non-root, seccomp, privilege-escalation, and capability fields. Expected: all pass; no GPU claim is made.

- [ ] **Step 5: Save evidence and report limitations**

Save command outputs, object YAML, route status, and OpenAI response under `/tmp/ani-inference-real-e2e-<timestamp>/`. Explicitly report any remaining blocker such as CPU engine incompatibility, missing tenant permission, KServe readiness delay, or unavailable GPU nodes; do not turn a partial API check into an end-to-end success claim.
