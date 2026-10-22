package kubernetes

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInvocationProbeUsesBoundPodAndRealGeneration(t *testing.T) {
	calls := 0
	valid := true
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || r.URL.Path != "/v1/completions" || body["model"] != "model-a" || body["max_tokens"] != float64(1) {
			t.Error("wrong probe", body)
		}
		if valid {
			w.Write([]byte(`{"choices":[{"text":"yes"}]}`))
		} else {
			w.Write([]byte(`{"choices":[]}`))
		}
	}))
	defer endpoint.Close()
	u, _ := url.Parse(endpoint.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	d, scope := diagnosticFixture(t)
	src := d.Source.(diagnosticSource)
	src.value.DesiredState = "running"
	src.value.PublicationPublished = true
	src.value.Endpoint = &EndpointSpec{ContainerPort: int32(p)}
	d.Source = src
	pod, _ := d.Core.Pods("runtime").Get(context.Background(), "owned", metav1.GetOptions{})
	pod.Status.PodIP = host
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	d.Core.Pods("runtime").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{})
	for _, want := range []string{"healthy", "unhealthy"} {
		result, err := d.ProbeInvocation(context.Background(), scope, "model-a")
		if err != nil || result.State != want || result.ObservedAt.IsZero() {
			t.Fatal(result, err)
		}
		valid = false
	}
	src.value.DesiredState = "stopped"
	d.Source = src
	result, err := d.ProbeInvocation(context.Background(), scope, "model-a")
	if err != nil || result.State != "unknown" || calls != 2 {
		t.Fatal("stopped runtime probed", result, err, calls)
	}
}
