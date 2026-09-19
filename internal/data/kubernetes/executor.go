package kubernetes

// This file is the Kubernetes execution boundary for durable Inference work.
// PostgreSQL remains the source of the desired generation; this adapter only
// renders typed objects and applies/deletes objects through controller-runtime.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	bizinference "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	bizreconcile "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/reconcile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

// DesiredRuntime is the projection read from the Inference-owned PostgreSQL
// spec. Bindings are only used for fenced deletion; an object is never adopted
// by name alone.
type DesiredRuntime struct {
	RuntimeSpec
	DesiredState         string
	RuntimePhase         string
	ModelReady           bool
	ModelReadyKnown      bool
	QuotaReserved        bool
	PublicationWithdrawn bool
	// True only when desired and observed publication are published for this
	// exact business generation, never inherited from the previous runtime.
	PublicationPublished bool
	Bindings             []RuntimeBinding
}

// RuntimeBinding identifies one object previously recorded in PostgreSQL.
// UID and ResourceVersion are mandatory for deletion so an old worker cannot
// remove a replacement object with the same name.
type RuntimeBinding struct {
	Kind, Namespace, Name string
	UID, ResourceVersion  string
	Role                  string
	Generation            int64
}

// BindingRecord is the durable identity captured immediately after a runtime
// apply. Keeping this small adapter contract in the Kubernetes package avoids
// coupling the executor to a particular persistence implementation.
type BindingRecord struct {
	TenantID, ServiceID     string
	Generation              int64
	Kind, Namespace         string
	Name, UID               string
	ResourceVersion         string
	Role                    string
	ExpectedUID             string
	ExpectedResourceVersion string
}

// BindingStore persists the Kubernetes identity before the worker proceeds to
// observation. This closes the crash window between an API apply and the
// later observation transaction.
type BindingStore interface {
	UpsertRuntimeBinding(context.Context, BindingRecord) error
}

// DesiredRuntimeSource reads the current desired generation and its immutable
// spec. Implementations normally use a tenant-scoped sqlc query.
type DesiredRuntimeSource interface {
	CurrentRuntime(context.Context, string, string, int64) (DesiredRuntime, error)
}

// RuntimeExecutor turns durable desired state into typed Kubernetes writes.
// Apply uses server-side apply with a stable field manager and never forces
// ownership. Delete uses both UID and resourceVersion preconditions.
type RuntimeExecutor struct {
	Client client.Client
	// APIReader is the uncached reader used immediately after an apply. The
	// manager cache may lag the API server and cannot be used as the binding
	// identity source at this crash-sensitive boundary.
	APIReader    client.Reader
	CRClient     client.Client
	Source       DesiredRuntimeSource
	Bindings     BindingStore
	FieldManager string
}

var _ bizreconcile.Runtime = (*RuntimeExecutor)(nil)
var _ bizinference.RuntimePort = (*RuntimeExecutor)(nil)

// operationRuntime loads one operation's target generation from PostgreSQL
// and carries the durable provider facts into the runtime gates. The lease
// token is required at this boundary; the durable operation/work store fences
// the caller before invoking this adapter.
func (e *RuntimeExecutor) operationRuntime(ctx context.Context, op bizinference.OperationContext) (DesiredRuntime, error) {
	if e == nil || e.Client == nil {
		return DesiredRuntime{}, errors.New("kubernetes runtime executor client is nil")
	}
	if e.Source == nil {
		return DesiredRuntime{}, errors.New("kubernetes runtime executor desired source is nil")
	}
	if op.TenantID == "" || op.ServiceID == "" || op.TargetGeneration < 1 || op.LeaseToken == "" {
		return DesiredRuntime{}, bizreconcile.ErrStaleGeneration
	}
	spec, err := e.Source.CurrentRuntime(ctx, op.TenantID, op.ServiceID, op.TargetGeneration)
	if err != nil {
		return DesiredRuntime{}, err
	}
	if spec.TenantID != "" && spec.TenantID != op.TenantID {
		return DesiredRuntime{}, errors.New("desired runtime tenant mismatch")
	}
	if spec.ServiceID != "" && spec.ServiceID != op.ServiceID {
		return DesiredRuntime{}, errors.New("desired runtime service mismatch")
	}
	if spec.Generation != 0 && spec.Generation != op.TargetGeneration {
		return DesiredRuntime{}, bizreconcile.ErrStaleGeneration
	}
	spec.TenantID, spec.ServiceID, spec.Generation = op.TenantID, op.ServiceID, op.TargetGeneration
	// The operation row is the durable hand-off from quota/publication
	// providers. Preserve source facts while accepting the confirmed result
	// recorded for this operation.
	if op.Reservation.State == "confirmed" {
		spec.QuotaReserved = true
	}
	if op.Publication.State == "withdrawn" {
		spec.PublicationWithdrawn = true
	}
	if op.Publication.State == "published" && op.Publication.Generation == op.TargetGeneration {
		spec.PublicationPublished = true
	}
	if spec.DesiredState == "" {
		switch op.Kind {
		case "stop":
			spec.DesiredState = "stopped"
		case "delete":
			spec.DesiredState = "deleted"
		default:
			spec.DesiredState = "running"
		}
	}
	return spec, nil
}

// ApplyCR projects the operation's target generation into the typed
// InferenceService CR. It does not apply runtime objects.
func (e *RuntimeExecutor) ApplyCR(ctx context.Context, op bizinference.OperationContext) error {
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return err
	}
	binding := controlBindingForSpec(spec)
	return e.applyCR(ctx, spec.RuntimeSpec, spec.DesiredState, binding)
}

// ApplyRuntime server-side-applies the typed runtime object and, when an
// explicit endpoint is configured, its Inference-owned Service. Each object
// retains its persisted binding fence when one already exists.
func (e *RuntimeExecutor) ApplyRuntime(ctx context.Context, op bizinference.OperationContext) error {
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return err
	}
	if !spec.QuotaReserved {
		return errors.New("quota reservation must be confirmed before runtime apply")
	}
	binding, err := runtimeBindingForMode(spec)
	if err != nil {
		return err
	}
	if _, err = e.applyFenced(ctx, spec.RuntimeSpec, binding); err != nil {
		return err
	}
	if endpointConfigured(spec.RuntimeSpec) {
		if _, err = e.applyEndpointFenced(ctx, spec.RuntimeSpec, endpointBindingForSpec(spec)); err != nil {
			return err
		}
	}
	return nil
}

// ObserveRuntime reads runtime and model facts from Kubernetes and PostgreSQL.
func (e *RuntimeExecutor) ObserveRuntime(ctx context.Context, op bizinference.OperationContext) (bizinference.RuntimeObservation, error) {
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return bizinference.RuntimeObservation{}, err
	}
	binding, err := runtimeBindingForMode(spec)
	if err != nil {
		return bizinference.RuntimeObservation{}, err
	}
	observation, err := e.observeHealth(ctx, spec, binding)
	if err != nil {
		return bizinference.RuntimeObservation{}, err
	}
	return bizinference.RuntimeObservation{
		Ready: observation.RuntimePhase == "ready", RuntimePhase: observation.RuntimePhase,
		RuntimeMode: observation.RuntimeMode, ReadyReplicas: observation.ReadyReplicas,
		ReadyGroups: observation.ReadyGroups, ReadyWorkers: observation.ReadyWorkers,
		LWSUID: observation.LWSUID, Objects: observation.Objects, Reason: observation.Reason,
		ModelReady: observation.ModelReady, ModelReadyKnown: observation.ModelReadyKnown,
	}, nil
}

// Both active operations and terminal-operation scans use the same health
// observation.
func (e *RuntimeExecutor) observeHealth(ctx context.Context, spec DesiredRuntime, binding *RuntimeBinding) (bizreconcile.Observation, error) {
	observation, err := e.observe(ctx, spec.RuntimeSpec, binding)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	if endpointConfigured(spec.RuntimeSpec) {
		endpoint := endpointBindingForSpec(spec)
		if endpoint == nil {
			observation.RuntimePhase = "degraded"
			observation.Reason = "endpoint binding is missing"
		} else if fact, missing, observeErr := e.observeBinding(ctx, spec.TenantID, spec.ServiceID, *endpoint); observeErr != nil {
			return bizreconcile.Observation{}, observeErr
		} else {
			observation.Objects = append(observation.Objects, fact)
			if missing {
				observation.RuntimePhase = "degraded"
				observation.Reason = "endpoint service is missing"
			} else if fact.Terminating {
				observation.RuntimePhase = "degraded"
				observation.Reason = "endpoint service is being deleted"
			}
		}
	}
	observation.ModelReady, observation.ModelReadyKnown = spec.ModelReady, spec.ModelReadyKnown
	return observation, nil
}

// DeleteRuntime issues fenced deletes for every persisted runtime binding.
// Publication withdrawal is checked again here so a runner retry cannot
// delete a published runtime due to stale operation state.
func (e *RuntimeExecutor) DeleteRuntime(ctx context.Context, op bizinference.OperationContext) error {
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return err
	}
	if !spec.PublicationWithdrawn {
		return errors.New("publication withdrawal must be confirmed before runtime deletion")
	}
	deleted := false
	for _, binding := range spec.Bindings {
		if binding.Role != "" && binding.Role != "runtime" && binding.Role != "endpoint" {
			continue
		}
		if err := e.Delete(ctx, binding); err != nil {
			return err
		}
		deleted = true
	}
	if !deleted {
		return errors.New("runtime deletion requires a persisted runtime binding")
	}
	return nil
}

// ObserveAbsence confirms all persisted runtime bindings are gone. A delete
// request succeeding is not treated as absence until this check converges.
func (e *RuntimeExecutor) ObserveAbsence(ctx context.Context, op bizinference.OperationContext) (bizinference.RuntimeObservation, error) {
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return bizinference.RuntimeObservation{}, err
	}
	if !spec.PublicationWithdrawn {
		return bizinference.RuntimeObservation{}, errors.New("publication withdrawal must be confirmed before runtime absence observation")
	}
	observation, err := e.observeDeletion(ctx, op.TargetGeneration, spec.RuntimeSpec, spec.Bindings)
	if err != nil {
		return bizinference.RuntimeObservation{}, err
	}
	return bizinference.RuntimeObservation{Absent: observation.RuntimePhase == "stopped", RuntimePhase: observation.RuntimePhase, RuntimeMode: observation.RuntimeMode, ReadyReplicas: observation.ReadyReplicas, ReadyGroups: observation.ReadyGroups, ReadyWorkers: observation.ReadyWorkers, LWSUID: observation.LWSUID, Objects: observation.Objects, Reason: observation.Reason}, nil
}

// DeleteCR removes the InferenceService projection only after runtime absence
// has been confirmed by the preceding durable operation step. UID and
// resourceVersion prevent a stale delete from removing a replacement CR.
func (e *RuntimeExecutor) DeleteCR(ctx context.Context, op bizinference.OperationContext) error {
	if e == nil || e.CRClient == nil {
		return errors.New("kubernetes CR client is nil")
	}
	spec, err := e.operationRuntime(ctx, op)
	if err != nil {
		return err
	}
	binding := controlBindingForSpec(spec)
	reader := client.Reader(e.CRClient)
	if e.APIReader != nil {
		reader = e.APIReader
	}
	obj := &crdv1.InferenceService{}
	if err := reader.Get(ctx, client.ObjectKey{Namespace: spec.Namespace, Name: spec.Name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !OwnedBy(obj, op.TenantID, op.ServiceID) {
		return errors.New("inference service CR is occupied by a different tenant or service")
	}
	if binding == nil {
		return errors.New("inference service CR requires a persisted control binding")
	}
	if binding.Kind != "InferenceService" || binding.Role != "control" || binding.Namespace != obj.GetNamespace() || binding.Name != obj.GetName() || binding.UID != string(obj.GetUID()) {
		return bizreconcile.ErrStaleGeneration
	}
	uid, rv := types.UID(obj.GetUID()), obj.GetResourceVersion()
	if uid == "" || rv == "" {
		return errors.New("inference service CR is missing current UID or resourceVersion")
	}
	return e.CRClient.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &rv})
}

func (e *RuntimeExecutor) Ensure(ctx context.Context, desired bizreconcile.Desired) (bizreconcile.Observation, error) {
	if e == nil || e.Client == nil {
		return bizreconcile.Observation{}, errors.New("kubernetes runtime executor client is nil")
	}
	if e.Source == nil {
		return bizreconcile.Observation{}, errors.New("kubernetes runtime executor desired source is nil")
	}
	if desired.TenantID == "" || desired.ServiceID == "" || desired.Generation < 1 {
		return bizreconcile.Observation{}, errors.New("tenant, service and positive generation are required")
	}
	spec, err := e.Source.CurrentRuntime(ctx, desired.TenantID, desired.ServiceID, desired.Generation)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	if spec.TenantID != "" && spec.TenantID != desired.TenantID {
		return bizreconcile.Observation{}, fmt.Errorf("desired runtime tenant mismatch")
	}
	if spec.ServiceID != "" && spec.ServiceID != desired.ServiceID {
		return bizreconcile.Observation{}, fmt.Errorf("desired runtime service mismatch")
	}
	if spec.Generation != 0 && spec.Generation != desired.Generation {
		return bizreconcile.Observation{}, bizreconcile.ErrStaleGeneration
	}
	spec.TenantID, spec.ServiceID, spec.Generation = desired.TenantID, desired.ServiceID, desired.Generation

	switch spec.DesiredState {
	case "", "running":
		if !spec.QuotaReserved {
			return bizreconcile.Observation{}, errors.New("quota reservation must be confirmed before runtime apply")
		}
		controlBinding := controlBindingForSpec(spec)
		if err := e.applyCR(ctx, spec.RuntimeSpec, spec.DesiredState, controlBinding); err != nil {
			return bizreconcile.Observation{}, err
		}
		binding, err := runtimeBindingForMode(spec)
		if err != nil {
			return bizreconcile.Observation{}, err
		}
		if _, err := e.applyFenced(ctx, spec.RuntimeSpec, binding); err != nil {
			return bizreconcile.Observation{}, err
		}
		if endpointConfigured(spec.RuntimeSpec) {
			if _, err := e.applyEndpointFenced(ctx, spec.RuntimeSpec, endpointBindingForSpec(spec)); err != nil {
				return bizreconcile.Observation{}, err
			}
		}
		return e.observeHealth(ctx, spec, binding)
	case "stopped", "deleted":
		if !spec.PublicationWithdrawn {
			return bizreconcile.Observation{}, errors.New("publication withdrawal must be confirmed before runtime deletion")
		}
		controlBinding := controlBindingForSpec(spec)
		if err := e.applyCR(ctx, spec.RuntimeSpec, spec.DesiredState, controlBinding); err != nil {
			return bizreconcile.Observation{}, err
		}
		if len(spec.Bindings) == 0 {
			if spec.RuntimePhase == "stopped" {
				return bizreconcile.Observation{Generation: desired.Generation, RuntimeMode: runtimeModeForSpec(spec.RuntimeSpec), RuntimePhase: "stopped", PublicationPhase: "unknown", InvocationHealth: "unknown"}, nil
			}
			return bizreconcile.Observation{}, errors.New("runtime deletion requires a persisted runtime binding")
		}
		deleted := false
		for _, binding := range spec.Bindings {
			if binding.Role != "" && binding.Role != "runtime" && binding.Role != "endpoint" {
				continue
			}
			if err := e.Delete(ctx, binding); err != nil {
				return bizreconcile.Observation{}, err
			}
			deleted = true
		}
		if !deleted {
			return bizreconcile.Observation{}, errors.New("runtime deletion requires a persisted runtime binding")
		}
		return e.observeDeletion(ctx, desired.Generation, spec.RuntimeSpec, spec.Bindings)
	default:
		return bizreconcile.Observation{}, fmt.Errorf("unsupported desired state %q", spec.DesiredState)
	}
}

func runtimeModeForSpec(spec RuntimeSpec) string {
	if spec.RuntimeMode == "" {
		return "deployment"
	}
	return spec.RuntimeMode
}

// applyCR projects the durable PostgreSQL generation into the typed
// InferenceService object. It is optional for unit adapters; production
// composition supplies the manager client. Runtime objects remain separately
// fenced and are never inferred from the CR alone.
func (e *RuntimeExecutor) applyCR(ctx context.Context, spec RuntimeSpec, desiredState string, expected *RuntimeBinding) error {
	if e.CRClient == nil {
		return nil
	}
	spec = normalizeEndpoint(spec)
	requests, err := resourceList(spec.Resources.Requests)
	if err != nil {
		return err
	}
	limits, err := resourceList(spec.Resources.Limits)
	if err != nil {
		return err
	}
	obj := &crdv1.InferenceService{
		TypeMeta: metav1.TypeMeta{APIVersion: crdv1.GroupVersion.String(), Kind: "InferenceService"},
		ObjectMeta: metav1.ObjectMeta{
			Name: spec.Name, Namespace: spec.Namespace,
			Labels: map[string]string{tenantIDLabel: spec.TenantID, serviceIDLabel: spec.ServiceID, generationLabel: strconv.FormatInt(spec.Generation, 10)},
		},
		Spec: crdv1.InferenceServiceSpec{
			Generation: spec.Generation, DesiredState: "running", ModelVersionID: spec.ModelVersionID,
			ModelRef:        &crdv1.ModelReference{VersionID: spec.ModelVersionID, ArtifactDigest: spec.ArtifactSHA256},
			ServedModelName: spec.ServedModelName, Resource: corev1.ResourceRequirements{Requests: requests, Limits: limits},
			Replicas: spec.Replicas, RuntimeMode: spec.RuntimeMode, WorkerReplicas: spec.WorkerReplicas,
			Engine: &crdv1.EngineSpec{Type: spec.EngineRuntime, Image: spec.Image, Command: append([]string(nil), spec.CommandArgv...)},
		},
	}
	if endpoint := endpointForCR(spec); endpoint != nil {
		obj.Spec.Endpoint = endpoint
	}
	if desiredState == "stopped" || desiredState == "deleted" {
		obj.Spec.DesiredState = desiredState
	}
	reader := client.Reader(e.CRClient)
	if e.APIReader != nil {
		reader = e.APIReader
	}
	current := &crdv1.InferenceService{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), current); err == nil {
		if !OwnedBy(current, spec.TenantID, spec.ServiceID) {
			return errors.New("inference service CR is occupied by a different tenant or service")
		}
		if expected == nil {
			return errors.New("existing inference service CR requires a persisted control binding")
		}
		if expected.Kind != "InferenceService" || expected.Role != "control" || expected.Namespace != current.GetNamespace() || expected.Name != current.GetName() || expected.UID == "" || expected.UID != string(current.GetUID()) {
			return bizreconcile.ErrStaleGeneration
		}
		if current.Spec.Generation > spec.Generation {
			return bizreconcile.ErrStaleGeneration
		}
		// A status-only write advances resourceVersion without changing the
		// control binding. Use this fresh RV as the SSA optimistic precondition;
		// the binding RV is only the spec-apply observation and is not suitable
		// after status projection.
		obj.SetUID(current.GetUID())
		obj.SetResourceVersion(current.GetResourceVersion())
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	if err := e.CRClient.Patch(ctx, obj, client.Apply, client.FieldOwner(ownerForRuntime(e.FieldManager))); err != nil {
		return err
	}
	observed := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), observed); err != nil {
		return err
	}
	if e.Bindings != nil {
		var expectedUID, expectedResourceVersion string
		if expected != nil {
			expectedUID, expectedResourceVersion = expected.UID, expected.ResourceVersion
		}
		if err := e.Bindings.UpsertRuntimeBinding(ctx, BindingRecord{
			TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation,
			Kind: "InferenceService", Namespace: observed.GetNamespace(), Name: observed.GetName(),
			UID: string(observed.GetUID()), ResourceVersion: observed.GetResourceVersion(), Role: "control",
			ExpectedUID: expectedUID, ExpectedResourceVersion: expectedResourceVersion,
		}); err != nil {
			return err
		}
	}
	return nil
}

// endpointForCR preserves the explicit endpoint contract in the Kubernetes
// projection. An omitted endpoint stays omitted; no listener port is guessed.
func endpointForCR(spec RuntimeSpec) *crdv1.EndpointSpec {
	if spec.Endpoint == nil && spec.ContainerPort == 0 && spec.ServicePort == 0 && spec.TargetPort.Type == 0 && spec.ServiceProtocol == "" {
		return nil
	}
	target := ""
	if spec.TargetPort.Type == intstr.Int {
		target = strconv.Itoa(spec.TargetPort.IntValue())
	} else if spec.TargetPort.Type == intstr.String {
		target = spec.TargetPort.StrVal
	}
	return &crdv1.EndpointSpec{ContainerPort: spec.ContainerPort, ServicePort: spec.ServicePort, TargetPort: target, Protocol: string(spec.ServiceProtocol)}
}

func ownerForRuntime(owner string) string {
	if owner == "" {
		return "ani-inference"
	}
	return owner
}

func resourceList(values map[string]string) (corev1.ResourceList, error) {
	result := corev1.ResourceList{}
	for name, value := range values {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid resource %q for %s: %w", value, name, err)
		}
		result[corev1.ResourceName(name)] = quantity
	}
	return result, nil
}

func (e *RuntimeExecutor) observe(ctx context.Context, spec RuntimeSpec, expected *RuntimeBinding) (bizreconcile.Observation, error) {
	runtimeMode := spec.RuntimeMode
	if runtimeMode == "" {
		runtimeMode = "deployment"
	}
	observation := bizreconcile.Observation{Generation: spec.Generation, RuntimeMode: runtimeMode, PublicationPhase: "unknown", InvocationHealth: "unknown"}
	kind := kindForRuntimeMode(runtimeMode)
	if expected != nil && expected.Kind != kind {
		return bizreconcile.Observation{}, bizreconcile.ErrStaleGeneration
	}
	obj, err := objectForKind(kind)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	obj.SetNamespace(spec.Namespace)
	obj.SetName(spec.Name)
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) {
			observation.RuntimePhase = "degraded"
			observation.Reason = "runtime object is missing"
			if expected != nil {
				observation.Objects = []bizreconcile.RuntimeObject{{Kind: expected.Kind, Namespace: expected.Namespace, Name: expected.Name, UID: expected.UID, ResourceVersion: expected.ResourceVersion, Role: expected.Role, BindingGeneration: expected.Generation, ExpectedUID: expected.UID, ExpectedResourceVersion: expected.ResourceVersion, Missing: true}}
			}
			return observation, nil
		}
		return bizreconcile.Observation{}, err
	}
	objectFact, err := validateObservedObject(obj, spec, expected)
	if err != nil {
		return bizreconcile.Observation{}, err
	}
	observation.Objects = []bizreconcile.RuntimeObject{objectFact}
	if objectFact.Missing {
		observation.RuntimePhase = "degraded"
		observation.Reason = "runtime object is missing"
		return observation, nil
	}
	if obj.GetDeletionTimestamp() != nil {
		observation.RuntimePhase = "degraded"
		observation.Reason = "runtime object is being deleted"
		return observation, nil
	}
	if lws, ok := obj.(*lwsv1.LeaderWorkerSet); ok {
		observation.ReadyGroups = lws.Status.ReadyReplicas
		observation.ReadyReplicas = lws.Status.ReadyReplicas
		observation.LWSUID = string(lws.UID)
		readyWorkers, err := e.readyLWSWorkers(ctx, spec)
		if err != nil {
			return bizreconcile.Observation{}, err
		}
		observation.ReadyWorkers = readyWorkers
		desired := int32(0)
		if lws.Spec.Replicas != nil {
			desired = *lws.Spec.Replicas
		}
		desiredWorkers := int32(0)
		if lws.Spec.LeaderWorkerTemplate.Size != nil && *lws.Spec.LeaderWorkerTemplate.Size > 1 {
			desiredWorkers = desired * (*lws.Spec.LeaderWorkerTemplate.Size - 1)
		}
		current := lws.Generation > 0 && lws.Status.ObservedGeneration >= lws.Generation
		updated := lws.Status.UpdatedReplicas >= desired
		ready := desired > 0 && lws.Status.ReadyReplicas >= desired && readyWorkers >= desiredWorkers
		switch {
		case current && updated && ready:
			observation.RuntimePhase = "ready"
		case current && updated:
			observation.RuntimePhase = "degraded"
			observation.Reason = "runtime readiness regressed"
		default:
			observation.RuntimePhase = "pending"
		}
		return observation, nil
	}
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		return bizreconcile.Observation{}, fmt.Errorf("unsupported observed runtime kind %T", obj)
	}
	observation.ReadyReplicas = deployment.Status.ReadyReplicas
	desired := int32(0)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	current := deployment.Generation > 0 && deployment.Status.ObservedGeneration >= deployment.Generation
	updated := deployment.Status.UpdatedReplicas >= desired
	available := deployment.Status.AvailableReplicas >= desired
	ready := desired > 0 && deployment.Status.ReadyReplicas >= desired
	switch {
	case current && updated && available && ready:
		observation.RuntimePhase = "ready"
	case current && updated:
		observation.RuntimePhase = "degraded"
		observation.Reason = "runtime readiness regressed"
	default:
		observation.RuntimePhase = "pending"
	}
	return observation, nil
}

// readyLWSWorkers counts ready worker Pods using the labels defined by the
// official LWS API. LWS ReadyReplicas reports groups, not individual workers,
// so group readiness alone is insufficient for a distributed runtime.
func (e *RuntimeExecutor) readyLWSWorkers(ctx context.Context, spec RuntimeSpec) (int32, error) {
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods,
		client.InNamespace(spec.Namespace),
		client.MatchingLabels{tenantIDLabel: spec.TenantID, serviceIDLabel: spec.ServiceID, generationLabel: strconv.FormatInt(spec.Generation, 10), lwsv1.SetNameLabelKey: spec.Name},
	); err != nil {
		return 0, err
	}
	// A group has one leader (index 0) and worker indices 1..WorkerReplicas.
	// Count each current group/worker slot once; duplicates or terminating Pods
	// must not conceal a missing worker in another group.
	ready := make(map[[2]int64]struct{})
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		worker, err := strconv.ParseInt(pod.Labels[lwsv1.WorkerIndexLabelKey], 10, 32)
		if err != nil || worker < 1 || worker > int64(spec.WorkerReplicas) {
			continue
		}
		group, err := strconv.ParseInt(pod.Labels[lwsv1.GroupIndexLabelKey], 10, 32)
		if err != nil || group < 0 || group >= int64(spec.Replicas) {
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready[[2]int64{group, worker}] = struct{}{}
				break
			}
		}
	}
	return int32(len(ready)), nil
}

func (e *RuntimeExecutor) observeDeletion(ctx context.Context, desiredGeneration int64, spec RuntimeSpec, bindings []RuntimeBinding) (bizreconcile.Observation, error) {
	observation := bizreconcile.Observation{Generation: desiredGeneration, RuntimeMode: spec.RuntimeMode, PublicationPhase: "unknown", InvocationHealth: "unknown", RuntimePhase: "stopped"}
	for _, binding := range bindings {
		if binding.Role != "" && binding.Role != "runtime" && binding.Role != "endpoint" {
			continue
		}
		fact, missing, err := e.observeBinding(ctx, spec.TenantID, spec.ServiceID, binding)
		if err != nil {
			return bizreconcile.Observation{}, err
		}
		observation.Objects = append(observation.Objects, fact)
		if !missing {
			observation.RuntimePhase = "degraded"
			observation.Reason = "runtime deletion pending"
		}
	}
	if len(observation.Objects) == 0 {
		return bizreconcile.Observation{}, errors.New("runtime deletion requires a persisted runtime binding")
	}
	return observation, nil
}

func (e *RuntimeExecutor) observeBinding(ctx context.Context, tenantID, serviceID string, binding RuntimeBinding) (bizreconcile.RuntimeObject, bool, error) {
	obj, err := objectForKind(binding.Kind)
	if err != nil {
		return bizreconcile.RuntimeObject{}, false, err
	}
	obj.SetNamespace(binding.Namespace)
	obj.SetName(binding.Name)
	if err := e.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) {
			return bizreconcile.RuntimeObject{Kind: binding.Kind, Namespace: binding.Namespace, Name: binding.Name, UID: binding.UID, ResourceVersion: binding.ResourceVersion, Role: binding.Role, BindingGeneration: binding.Generation, ExpectedUID: binding.UID, ExpectedResourceVersion: binding.ResourceVersion, Missing: true}, true, nil
		}
		return bizreconcile.RuntimeObject{}, false, err
	}
	fact, err := validateObservedObject(obj, RuntimeSpec{TenantID: tenantID, ServiceID: serviceID, Generation: binding.Generation}, &binding)
	if err != nil {
		return bizreconcile.RuntimeObject{}, false, err
	}
	return fact, false, nil
}

func validateObservedObject(obj client.Object, spec RuntimeSpec, expected *RuntimeBinding) (bizreconcile.RuntimeObject, error) {
	if !OwnedBy(obj, spec.TenantID, spec.ServiceID) {
		return bizreconcile.RuntimeObject{}, errors.New("observed runtime ownership labels do not match")
	}
	generation, err := strconv.ParseInt(obj.GetLabels()[generationLabel], 10, 64)
	if err != nil || generation < 1 {
		return bizreconcile.RuntimeObject{}, errors.New("observed runtime is missing a valid generation label")
	}
	if generation != spec.Generation {
		return bizreconcile.RuntimeObject{}, bizreconcile.ErrStaleGeneration
	}
	if obj.GetUID() == "" {
		return bizreconcile.RuntimeObject{}, errors.New("observed runtime is missing UID")
	}
	if obj.GetResourceVersion() == "" {
		return bizreconcile.RuntimeObject{}, errors.New("observed runtime is missing resourceVersion")
	}
	if expected != nil {
		if expected.Namespace != obj.GetNamespace() || expected.Name != obj.GetName() {
			return bizreconcile.RuntimeObject{}, bizreconcile.ErrStaleGeneration
		}
		if expected.UID == "" || expected.UID != string(obj.GetUID()) {
			return bizreconcile.RuntimeObject{}, bizreconcile.ErrStaleGeneration
		}
	}
	role := "runtime"
	if expected != nil && expected.Role != "" {
		role = expected.Role
	}
	return bizreconcile.RuntimeObject{Kind: kindForObject(obj), Namespace: obj.GetNamespace(), Name: obj.GetName(), UID: string(obj.GetUID()), ResourceVersion: obj.GetResourceVersion(), Role: role, BindingGeneration: spec.Generation, ExpectedUID: expectedUID(expected), ExpectedResourceVersion: expectedResourceVersion(expected), Terminating: obj.GetDeletionTimestamp() != nil}, nil
}

func expectedUID(binding *RuntimeBinding) string {
	if binding == nil {
		return ""
	}
	return binding.UID
}

func expectedResourceVersion(binding *RuntimeBinding) string {
	if binding == nil {
		return ""
	}
	return binding.ResourceVersion
}

func kindForObject(obj client.Object) string {
	switch obj.(type) {
	case *crdv1.InferenceService:
		return "InferenceService"
	case *lwsv1.LeaderWorkerSet:
		return "LeaderWorkerSet"
	case *corev1.Service:
		return "Service"
	case *corev1.Pod:
		return "Pod"
	default:
		return "Deployment"
	}
}

func controlBindingForSpec(spec DesiredRuntime) *RuntimeBinding {
	for i := range spec.Bindings {
		binding := &spec.Bindings[i]
		if binding.Role == "control" && binding.Kind == "InferenceService" {
			return binding
		}
	}
	return nil
}

// Apply renders and server-side-applies the selected typed runtime object.
// client.Apply (PatchType ApplyPatchType) serializes the typed object as an
// apply patch; no unstructured object is used to build the resource tree.
func (e *RuntimeExecutor) applyFenced(ctx context.Context, spec RuntimeSpec, expected *RuntimeBinding) (client.Object, error) {
	if e == nil || e.Client == nil {
		return nil, errors.New("kubernetes runtime executor client is nil")
	}
	var obj client.Object
	var err error
	mode := spec.RuntimeMode
	if mode == "" || mode == "deployment" {
		obj, err = Deployment(spec)
	} else if mode == "leader_worker_set" {
		obj, err = LeaderWorkerSet(spec)
	} else {
		return nil, fmt.Errorf("unsupported runtime mode %q", mode)
	}
	if err != nil {
		return nil, err
	}
	reader := client.Reader(e.Client)
	if e.APIReader != nil {
		reader = e.APIReader
	}
	current := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), current); err == nil {
		if !OwnedBy(current, spec.TenantID, spec.ServiceID) {
			return nil, errors.New("runtime name is occupied by a different tenant or service")
		}
		generation, parseErr := strconv.ParseInt(current.GetLabels()[generationLabel], 10, 64)
		if parseErr != nil || generation < 1 {
			return nil, errors.New("existing runtime is missing its generation label")
		}
		if generation > spec.Generation {
			return nil, bizreconcile.ErrStaleGeneration
		}
		if expected == nil {
			return nil, errors.New("existing runtime requires a persisted runtime binding")
		}
		if expected.Kind != kindForRuntimeMode(mode) || expected.Namespace != current.GetNamespace() || expected.Name != current.GetName() || expected.UID != string(current.GetUID()) || expected.ResourceVersion != current.GetResourceVersion() {
			return nil, bizreconcile.ErrStaleGeneration
		}
		if current.GetDeletionTimestamp() != nil {
			return nil, errors.New("runtime deletion is still in progress")
		}
		// Apply must carry the RV read above. Without it, a replacement or
		// concurrent writer between GET and SSA can be silently overwritten.
		obj.SetResourceVersion(current.GetResourceVersion())
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	owner := e.FieldManager
	if owner == "" {
		owner = "ani-inference"
	}
	// Omitting client.ForceOwnership is deliberate: another field owner must
	// produce a conflict rather than being silently taken over.
	if err := e.Client.Patch(ctx, obj, client.Apply, client.FieldOwner(owner)); err != nil {
		return nil, err
	}
	// Apply responses are not required to populate all server-assigned
	// metadata on the submitted object. Read the object back before persisting
	// the binding so UID/resourceVersion are the API server's current values.
	observed := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), observed); err != nil {
		return nil, err
	}
	if e.Bindings != nil {
		var expectedUID, expectedResourceVersion string
		if expected != nil {
			expectedUID, expectedResourceVersion = expected.UID, expected.ResourceVersion
		}
		if err := e.Bindings.UpsertRuntimeBinding(ctx, BindingRecord{
			TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation,
			Kind: kindForObject(observed), Namespace: observed.GetNamespace(), Name: observed.GetName(),
			UID: string(observed.GetUID()), ResourceVersion: observed.GetResourceVersion(), Role: "runtime",
			ExpectedUID: expectedUID, ExpectedResourceVersion: expectedResourceVersion,
		}); err != nil {
			return nil, err
		}
	}
	return observed, nil
}

func endpointConfigured(spec RuntimeSpec) bool {
	configured := spec.Endpoint != nil
	spec = normalizeEndpoint(spec)
	return configured || spec.ContainerPort > 0 || spec.ServicePort > 0 || spec.TargetPort.Type != 0 || spec.ServiceProtocol != ""
}

func endpointBindingForSpec(spec DesiredRuntime) *RuntimeBinding {
	for i := range spec.Bindings {
		binding := &spec.Bindings[i]
		if binding.Role == "endpoint" && binding.Kind == "Service" {
			return binding
		}
	}
	return nil
}

// applyEndpointFenced applies the stable Inference-owned Service and records
// its UID/resourceVersion before the worker advances to observation. The
// endpoint has its own binding because LWS also creates a separate discovery
// Service with the runtime name.
func (e *RuntimeExecutor) applyEndpointFenced(ctx context.Context, spec RuntimeSpec, expected *RuntimeBinding) (client.Object, error) {
	if e == nil || e.Client == nil {
		return nil, errors.New("kubernetes runtime executor client is nil")
	}
	obj, err := Service(spec)
	if err != nil {
		return nil, err
	}
	reader := client.Reader(e.Client)
	if e.APIReader != nil {
		reader = e.APIReader
	}
	current := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), current); err == nil {
		if !OwnedBy(current, spec.TenantID, spec.ServiceID) {
			return nil, errors.New("endpoint name is occupied by a different tenant or service")
		}
		generation, parseErr := strconv.ParseInt(current.GetLabels()[generationLabel], 10, 64)
		if parseErr != nil || generation < 1 {
			return nil, errors.New("existing endpoint is missing its generation label")
		}
		if generation > spec.Generation {
			return nil, bizreconcile.ErrStaleGeneration
		}
		if expected == nil || expected.Kind != "Service" || expected.Namespace != current.GetNamespace() || expected.Name != current.GetName() || expected.UID != string(current.GetUID()) || expected.ResourceVersion != current.GetResourceVersion() {
			return nil, bizreconcile.ErrStaleGeneration
		}
		if current.GetDeletionTimestamp() != nil {
			return nil, errors.New("endpoint deletion is still in progress")
		}
		obj.SetResourceVersion(current.GetResourceVersion())
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	owner := e.FieldManager
	if owner == "" {
		owner = "ani-inference"
	}
	if err := e.Client.Patch(ctx, obj, client.Apply, client.FieldOwner(owner)); err != nil {
		return nil, err
	}
	observed := obj.DeepCopyObject().(client.Object)
	if err := reader.Get(ctx, client.ObjectKeyFromObject(obj), observed); err != nil {
		return nil, err
	}
	if e.Bindings != nil {
		var expectedUID, expectedResourceVersion string
		if expected != nil {
			expectedUID, expectedResourceVersion = expected.UID, expected.ResourceVersion
		}
		if err := e.Bindings.UpsertRuntimeBinding(ctx, BindingRecord{TenantID: spec.TenantID, ServiceID: spec.ServiceID, Generation: spec.Generation, Kind: "Service", Namespace: observed.GetNamespace(), Name: observed.GetName(), UID: string(observed.GetUID()), ResourceVersion: observed.GetResourceVersion(), Role: "endpoint", ExpectedUID: expectedUID, ExpectedResourceVersion: expectedResourceVersion}); err != nil {
			return nil, err
		}
	}
	return observed, nil
}

// Apply is exported for the worker and focused adapter tests. It returns the
// typed object after the API response has been applied to its metadata.
func (e *RuntimeExecutor) Apply(ctx context.Context, spec RuntimeSpec) (client.Object, error) {
	return e.applyFenced(ctx, spec, nil)
}

func kindForRuntimeMode(mode string) string {
	if mode == "leader_worker_set" {
		return "LeaderWorkerSet"
	}
	return "Deployment"
}

func runtimeBindingForMode(spec DesiredRuntime) (*RuntimeBinding, error) {
	kind := kindForRuntimeMode(spec.RuntimeMode)
	for i := range spec.Bindings {
		binding := &spec.Bindings[i]
		if binding.Role == "runtime" && binding.Kind == kind {
			return binding, nil
		}
	}
	// No binding is expected for the first apply, provided the named runtime
	// object does not already exist. applyFenced rejects adopting an existing
	// object without a durable identity.
	return nil, nil
}

// Delete removes one previously bound runtime object. Both preconditions are
// required; a missing object is already converged and is therefore successful.
func (e *RuntimeExecutor) Delete(ctx context.Context, binding RuntimeBinding) error {
	if e == nil || e.Client == nil {
		return errors.New("kubernetes runtime executor client is nil")
	}
	if binding.Namespace == "" || binding.Name == "" || binding.UID == "" || binding.ResourceVersion == "" {
		return errors.New("runtime binding requires namespace, name, UID and resourceVersion")
	}
	obj, err := objectForKind(binding.Kind)
	if err != nil {
		return err
	}
	obj.SetNamespace(binding.Namespace)
	obj.SetName(binding.Name)
	uid, rv := types.UID(binding.UID), binding.ResourceVersion
	if err := e.Client.Delete(ctx, obj, client.Preconditions{UID: &uid, ResourceVersion: &rv}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func objectForKind(kind string) (client.Object, error) {
	switch kind {
	case "Deployment":
		return &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: appsv1.SchemeGroupVersion.String(), Kind: "Deployment"}}, nil
	case "LeaderWorkerSet":
		return &lwsv1.LeaderWorkerSet{TypeMeta: metav1.TypeMeta{APIVersion: lwsv1.GroupVersion.String(), Kind: "LeaderWorkerSet"}}, nil
	case "Service":
		return &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Service"}}, nil
	case "Pod":
		return &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Pod"}}, nil
	case "InferenceService":
		return &crdv1.InferenceService{TypeMeta: metav1.TypeMeta{APIVersion: crdv1.GroupVersion.String(), Kind: "InferenceService"}}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime binding kind %q", kind)
	}
}
