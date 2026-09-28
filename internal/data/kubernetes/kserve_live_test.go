package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestKServeLiveLifecycle is opt-in: its caller supplies a RuntimeSpec fixture
// (including the entire engine command), context, expected API server and
// gateway URL. It exercises real runtime/publication adapters, using in-memory
// bindings; it does not claim to verify the gRPC/DB operation runner.
func TestKServeLiveLifecycle(t *testing.T) {
	path := os.Getenv("ANI_KSERVE_LIVE_SPEC")
	if path == "" {
		t.Skip("set ANI_KSERVE_LIVE_SPEC to run against an explicitly selected cluster")
	}
	contextName, expectedServer := os.Getenv("ANI_KSERVE_LIVE_CONTEXT"), os.Getenv("ANI_KSERVE_LIVE_SERVER")
	if contextName == "" || expectedServer == "" || os.Getenv("ANI_KSERVE_LIVE_GATEWAY_URL") == "" {
		t.Fatal("live test requires context, expected API server and gateway URL")
	}
	gatewayURL, err := url.Parse(os.Getenv("ANI_KSERVE_LIVE_GATEWAY_URL"))
	if err != nil || gatewayURL.Host == "" || (gatewayURL.Path != "" && gatewayURL.Path != "/") {
		t.Fatal("gateway URL must be a root URL, without /v1/completions")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{CurrentContext: contextName}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != expectedServer {
		t.Fatalf("API server %q does not match expected %q", config.Host, expectedServer)
	}
	cl, err := client.New(config, client.Options{Scheme: executorScheme(t)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec RuntimeSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	obj := newKServeInferenceService(spec.Namespace, spec.Name)
	if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), obj); !apierrors.IsNotFound(err) {
		t.Fatalf("canary name must be unused; get returned %v", err)
	}
	source := &fakeRuntimeSource{desired: DesiredRuntime{RuntimeSpec: spec, DesiredState: "running", ModelReady: true, ModelReadyKnown: true}}
	bindings := &recordingBindingStore{}
	executor := &KServeRuntimeExecutor{RuntimeExecutor: &RuntimeExecutor{Client: cl, APIReader: cl, Source: source, Bindings: bindings}}
	publisher := &HTTPRoutePublisher{Client: cl, APIReader: cl, Source: source, RuntimeProvider: "kserve", GatewayNamespace: "higress-system", GatewayName: "ani-higress", RouteNamespace: spec.Namespace, PublicBaseURL: os.Getenv("ANI_KSERVE_LIVE_GATEWAY_URL")}
	op := inferencebiz.OperationContext{TenantID: spec.TenantID, ServiceID: spec.ServiceID, TargetGeneration: spec.Generation, LeaseToken: "live-test"}
	pub := publication.Publication{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation}
	syncBinding := func() {
		if len(bindings.records) > 0 {
			b := bindings.records[len(bindings.records)-1]
			source.desired.Bindings = []RuntimeBinding{{Kind: b.Kind, Namespace: b.Namespace, Name: b.Name, UID: b.UID, ResourceVersion: b.ResourceVersion, Role: b.Role, Generation: b.Generation}}
		}
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 2*time.Minute)
		defer done()
		if err := publisher.Withdraw(cleanupCtx, pub); err != nil {
			t.Errorf("cleanup publication: %v", err)
		}
		syncBinding()
		for _, b := range source.desired.Bindings {
			if err := executor.deleteKServe(cleanupCtx, b); err != nil {
				t.Errorf("cleanup runtime: %v", err)
			}
		}
	})
	if err := executor.ApplyRuntime(ctx, op); err != nil {
		t.Fatal(err)
	}
	syncBinding()
	t.Log("KServe runtime applied; binding recorded")
	lastReason := ""
	for {
		observation, err := executor.ObserveRuntime(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		if observation.Ready {
			break
		}
		if observation.Reason != lastReason {
			t.Logf("waiting: %s", observation.Reason)
			lastReason = observation.Reason
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runtime readiness: %v (%s)", ctx.Err(), lastReason)
		case <-time.After(5 * time.Second):
		}
	}
	// Re-apply after KServe has changed resourceVersion via status writes.
	if err := executor.ApplyRuntime(ctx, op); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	syncBinding()
	t.Log("runtime Ready; repeated server-side apply passed")
	if err := publisher.Publish(ctx, pub); err != nil {
		t.Fatal(err)
	}
	for {
		ready, err := publisher.ConfirmPublished(ctx, pub)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	endpoint, err := publisher.Endpoint(ctx, pub)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{"model": spec.ServedModelName, "prompt": "KServe adapter test:", "max_tokens": 6, "temperature": 0})
	var completion struct {
		Model   string `json:"model"`
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	propagationDeadline := time.Now().Add(30 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		// The current Gateway API publication contract uses an exact model header.
		// The JSON model remains the vLLM request identity as well.
		req.Header.Set("x-higress-llm-model", spec.ServedModelName)
		response, err := (&http.Client{Timeout: time.Minute}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var errorBody []byte
		if response.StatusCode == http.StatusOK {
			err = json.NewDecoder(response.Body).Decode(&completion)
		} else {
			errorBody, _ = io.ReadAll(io.LimitReader(response.Body, 4096))
		}
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode == http.StatusOK {
			break
		}
		if time.Now().After(propagationDeadline) || (response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusServiceUnavailable) {
			t.Fatalf("completion %s: %s", response.Status, errorBody)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("completion returned %s after gateway propagation wait: %s", response.Status, errorBody)
		case <-time.After(2 * time.Second):
		}
	}
	if completion.Model != spec.ServedModelName || len(completion.Choices) == 0 || completion.Choices[0].Text == "" {
		t.Fatalf("unexpected completion: %+v", completion)
	}
	t.Logf("Higress model-header routing and completion passed: model=%s", completion.Model)
	if err := publisher.Withdraw(ctx, pub); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := publisher.ConfirmWithdrawn(ctx, pub)
	if err != nil || !withdrawn {
		t.Fatalf("publication withdrawn=%v err=%v", withdrawn, err)
	}
	source.desired.PublicationWithdrawn = true
	if err := executor.DeleteRuntime(ctx, op); err != nil {
		t.Fatal(err)
	}
	for {
		observation, err := executor.ObserveAbsence(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		if observation.Absent {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	key := client.ObjectKey{Namespace: spec.Namespace, Name: KServePredictorServiceName(spec.Name)}
	for _, child := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		if err := cl.Get(ctx, key, child); !apierrors.IsNotFound(err) {
			t.Fatalf("dependent %T still exists or cannot be read: %v", child, err)
		}
	}
	t.Log("publication withdrawn; foreground deletion removed KServe runtime and dependent Deployment/Service")
}
