package kubernetes

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	biz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RuntimeDiagnostics uses uncached reads and server-owned bindings. Only
// the persisted UID or its verified descendants authorize
// diagnostics access. Caller-controlled pod names never establish ownership.
type RuntimeDiagnostics struct {
	Source DesiredRuntimeSource
	Reader client.Reader
	Core   typedcorev1.CoreV1Interface
}

var _ biz.Reader = (*RuntimeDiagnostics)(nil)

const maxDiagnosticPods = 500
const maxDiagnosticEvents = 5000
const maxLogBytes = 2 * 1024 * 1024

func (d *RuntimeDiagnostics) ownedPods(ctx context.Context, scope biz.Scope) (DesiredRuntime, []corev1.Pod, error) {
	if d == nil || d.Source == nil || d.Reader == nil || d.Core == nil {
		return DesiredRuntime{}, nil, biz.ErrUnavailable
	}
	runtime, err := d.Source.CurrentRuntime(ctx, scope.TenantID, scope.ServiceID, scope.Generation)
	if err != nil {
		return runtime, nil, err
	}
	if runtime.TenantID != scope.TenantID || runtime.ServiceID != scope.ServiceID || runtime.Generation != scope.Generation || runtime.Namespace == "" {
		return runtime, nil, fmt.Errorf("runtime scope mismatch")
	}
	// KServe/LWS may not propagate ANI labels to every worker Pod. Ownership
	// ancestry, not label propagation, is the authorization source.
	list, err := d.Core.Pods(runtime.Namespace).List(ctx, metav1.ListOptions{Limit: maxDiagnosticPods + 1})
	if err != nil {
		return runtime, nil, err
	}
	if len(list.Items) > maxDiagnosticPods || list.Continue != "" {
		return runtime, nil, biz.ErrLimit
	}
	pods := make([]corev1.Pod, 0, len(list.Items))
	for _, pod := range list.Items {
		if tenant := pod.Labels[tenantIDLabel]; tenant != "" && tenant != scope.TenantID {
			continue
		}
		if service := pod.Labels[serviceIDLabel]; service != "" && service != scope.ServiceID {
			continue
		}
		owned, err := d.boundObject(ctx, runtime, pod.Namespace, "Pod", pod.Name, string(pod.UID), pod.OwnerReferences, 0)
		if err != nil {
			return runtime, nil, err
		}
		if owned {
			pods = append(pods, pod)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return runtime, pods, nil
}
func (d *RuntimeDiagnostics) boundObject(ctx context.Context, runtime DesiredRuntime, namespace, kind, name, uid string, owners []metav1.OwnerReference, depth int) (bool, error) {
	if uid == "" || depth > 8 {
		return false, nil
	}
	for _, binding := range runtime.Bindings {
		if binding.Namespace == namespace && diagnosticBindingKind(binding.Kind) == kind && binding.Name == name && binding.UID == uid {
			return true, nil
		}
	}
	for _, owner := range owners {
		if !diagnosticOwnerKind(owner.APIVersion, owner.Kind) {
			continue
		}
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		object := &unstructured.Unstructured{}
		object.SetAPIVersion(owner.APIVersion)
		object.SetKind(owner.Kind)
		err := d.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: owner.Name}, object)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if object.GetUID() != owner.UID {
			continue
		}
		matched, err := d.boundObject(ctx, runtime, namespace, owner.Kind, owner.Name, string(owner.UID), object.GetOwnerReferences(), depth+1)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}
func (d *RuntimeDiagnostics) Instances(ctx context.Context, scope biz.Scope) ([]biz.Instance, error) {
	_, pods, err := d.ownedPods(ctx, scope)
	if err != nil {
		return nil, err
	}
	result := make([]biz.Instance, 0, len(pods))
	for _, pod := range pods {
		instance := biz.Instance{Name: pod.Name, UID: string(pod.UID), Phase: string(pod.Status.Phase)}
		for _, container := range pod.Spec.Containers {
			instance.Containers = append(instance.Containers, container.Name)
		}
		for _, container := range pod.Spec.InitContainers {
			instance.Containers = append(instance.Containers, container.Name)
		}
		result = append(result, instance)
	}
	return result, nil
}
func (d *RuntimeDiagnostics) Logs(ctx context.Context, scope biz.Scope, query biz.LogsQuery, emit func(biz.LogLine) error) (bool, error) {
	runtime, pods, err := d.ownedPods(ctx, scope)
	if err != nil {
		return false, err
	}
	var selected *corev1.Pod
	for i := range pods {
		if pods[i].Name == query.Instance {
			selected = &pods[i]
			break
		}
	}
	if selected == nil {
		return false, biz.ErrNotFound
	}
	container := query.Container
	if container == "" {
		if len(selected.Spec.Containers) != 1 {
			return false, fmt.Errorf("container must be selected")
		}
		container = selected.Spec.Containers[0].Name
	}
	valid := false
	for _, c := range append(append([]corev1.Container(nil), selected.Spec.Containers...), selected.Spec.InitContainers...) {
		if c.Name == container {
			valid = true
		}
	}
	if !valid {
		return false, biz.ErrNotFound
	}
	options := &corev1.PodLogOptions{Container: container, Follow: query.Follow, Timestamps: true, TailLines: &query.TailLines}
	if !query.Since.IsZero() {
		since := metav1.NewTime(query.Since)
		options.SinceTime = &since
	}
	// API logs address a pod by name. Recheck its UID after opening to reject a
	// replaced pod before emitting any content.
	stream, err := d.Core.Pods(runtime.Namespace).GetLogs(selected.Name, options).Stream(ctx)
	if err != nil {
		return false, err
	}
	defer stream.Close()
	actual, err := d.Core.Pods(runtime.Namespace).Get(ctx, selected.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if actual.UID != selected.UID {
		return false, biz.ErrNotFound
	}
	return readRuntimeLogs(ctx, stream, query, emit)
}
func readRuntimeLogs(ctx context.Context, reader io.Reader, query biz.LogsQuery, emit func(biz.LogLine) error) (bool, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxLogBytes+1))
	scanner.Buffer(make([]byte, 4096), 256*1024)
	bytes := 0
	lines := int64(0)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		raw := scanner.Text()
		bytes += len(raw) + 1
		if bytes > maxLogBytes {
			return true, nil
		}
		stamp, message, ok := strings.Cut(raw, " ")
		if !ok {
			return false, fmt.Errorf("runtime log timestamp missing")
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return false, fmt.Errorf("runtime log timestamp invalid")
		}
		if !query.Since.IsZero() && at.Before(query.Since) {
			continue
		}
		if !query.Until.IsZero() && !at.Before(query.Until) {
			if query.Follow {
				return false, nil
			}
			continue
		}
		if !query.Follow && lines >= query.TailLines {
			return true, nil
		}
		if err := emit(biz.LogLine{Timestamp: at, Message: message}); err != nil {
			return false, err
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		return false, err
	}
	return bytes >= maxLogBytes, nil
}
func (d *RuntimeDiagnostics) Events(ctx context.Context, scope biz.Scope, query biz.EventsQuery) ([]biz.Event, int64, error) {
	runtime, pods, err := d.ownedPods(ctx, scope)
	if err != nil {
		return nil, 0, err
	}
	uids := map[string]string{}
	for _, binding := range runtime.Bindings {
		if binding.Namespace == runtime.Namespace && binding.UID != "" {
			uids[binding.UID] = binding.Name
		}
	}
	for _, pod := range pods {
		uids[string(pod.UID)] = pod.Name
	}
	events := map[string]biz.Event{}
	scanned := 0
	for uid := range uids {
		continuation := ""
		for {
			list, err := d.Core.Events(runtime.Namespace).List(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", uid).String(), Limit: 500, Continue: continuation})
			if err != nil {
				return nil, 0, err
			}
			scanned += len(list.Items)
			if scanned > maxDiagnosticEvents {
				return nil, 0, biz.ErrLimit
			}
			for _, event := range list.Items {
				if string(event.InvolvedObject.UID) != uid {
					continue
				}
				at := event.LastTimestamp.Time
				if at.IsZero() {
					at = event.EventTime.Time
				}
				if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
					at = event.Series.LastObservedTime.Time
				}
				if at.IsZero() {
					at = event.CreationTimestamp.Time
				}
				if (!query.Since.IsZero() && at.Before(query.Since)) || (!query.Until.IsZero() && !at.Before(query.Until)) || (query.Type != "" && query.Type != event.Type) {
					continue
				}
				count := int64(event.Count)
				if event.Series != nil {
					count = int64(event.Series.Count)
				}
				if count == 0 {
					count = 1
				}
				events[string(event.UID)] = biz.Event{ID: string(event.UID), Type: event.Type, Reason: event.Reason, Message: event.Message, Timestamp: at, Instance: event.InvolvedObject.Name, Count: count}
			}
			continuation = list.Continue
			if continuation == "" {
				break
			}
		}
	}
	result := make([]biz.Event, 0, len(events))
	for _, event := range events {
		result = append(result, event)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Timestamp.Equal(result[j].Timestamp) {
			return result[i].ID > result[j].ID
		}
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	total := int64(len(result))
	if query.Offset >= len(result) {
		return []biz.Event{}, total, nil
	}
	end := query.Offset + query.Limit
	if end > len(result) {
		end = len(result)
	}
	return result[query.Offset:end], total, nil
}

func diagnosticBindingKind(kind string) string {
	switch kind {
	case KServeInferenceServiceKind:
		return "InferenceService"
	case KServeLLMInferenceServiceKind:
		return "LLMInferenceService"
	default:
		return kind
	}
}
func diagnosticOwnerKind(apiVersion, kind string) bool {
	switch kind {
	case "Deployment", "ReplicaSet", "StatefulSet":
		return apiVersion == "apps/v1"
	case "Job":
		return apiVersion == "batch/v1"
	case "LeaderWorkerSet":
		return strings.HasPrefix(apiVersion, "leaderworkerset.x-k8s.io/")
	case "InferenceService":
		return strings.HasPrefix(apiVersion, "serving.kserve.io/") || strings.HasPrefix(apiVersion, "ani.kubercloud.com/")
	case "LLMInferenceService":
		return strings.HasPrefix(apiVersion, "serving.kserve.io/")
	default:
		return false
	}
}
