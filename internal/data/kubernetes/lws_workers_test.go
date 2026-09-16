package kubernetes

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

func TestLWSWorkerReadinessCountsOnlyDistinctLiveWorkerSlots(t *testing.T) {
	spec := RuntimeSpec{TenantID: "tenant", ServiceID: "service", Name: "lws", Namespace: "ns", Generation: 3, Replicas: 2, WorkerReplicas: 2, RuntimeMode: "leader_worker_set"}
	pod := func(group, worker string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: spec.Namespace, Labels: map[string]string{
			tenantIDLabel: spec.TenantID, serviceIDLabel: spec.ServiceID, generationLabel: "3",
			lwsv1.SetNameLabelKey: spec.Name, lwsv1.GroupIndexLabelKey: group, lwsv1.WorkerIndexLabelKey: worker,
		}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	}
	terminating := pod("0", "2")
	now := metav1.Now()
	terminating.DeletionTimestamp, terminating.Finalizers = &now, []string{"test.ani.kubercloud.com/hold"}
	completed := pod("0", "2")
	completed.Status.Phase = corev1.PodSucceeded
	old := pod("0", "2")
	old.Labels[generationLabel] = "2"
	for _, tt := range []struct {
		name string
		pods []*corev1.Pod
		want int32
	}{
		{"complete groups with leaders", []*corev1.Pod{pod("0", "0"), pod("0", "1"), pod("0", "2"), pod("1", "0"), pod("1", "1"), pod("1", "2")}, 4},
		{"leader cannot replace missing worker", []*corev1.Pod{pod("0", "0"), pod("0", "1")}, 1},
		{"terminating worker", []*corev1.Pod{pod("0", "1"), terminating}, 1},
		{"finished worker", []*corev1.Pod{completed}, 0},
		{"duplicate slot cannot fill another group", []*corev1.Pod{pod("0", "1"), pod("0", "1"), pod("0", "2"), pod("0", "2")}, 2},
		{"invalid coordinates", []*corev1.Pod{pod("0", "bad"), pod("0", "-1"), pod("0", "3"), pod("2", "1"), pod("", "1"), pod("0", "")}, 0},
		{"old generation", []*corev1.Pod{old}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]client.Object, 0, len(tt.pods))
			for i, p := range tt.pods {
				p.Name = fmt.Sprintf("pod-%d", i)
				objects = append(objects, p)
			}
			e := &RuntimeExecutor{Client: fake.NewClientBuilder().WithScheme(executorScheme(t)).WithObjects(objects...).Build()}
			got, err := e.readyLWSWorkers(context.Background(), spec)
			if err != nil || got != tt.want {
				t.Fatalf("ready workers=%d err=%v, want %d", got, err, tt.want)
			}
		})
	}
}
