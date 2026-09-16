package kubernetes

// This file contains the controller-runtime adapter for the InferenceService
// CRD.  The adapter deliberately stays thin: durable operation state and
// observations are owned by the domain reconciler (and its PostgreSQL
// repository), while controller-runtime only supplies cache events and a
// periodic wake-up.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	lwsv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

// InferenceServiceGVK identifies the v1 InferenceService CRD.
var InferenceServiceGVK = crdv1.GroupVersion.WithKind("InferenceService")

// AddToScheme registers typed Kubernetes APIs used by this service.
func AddToScheme(scheme *runtime.Scheme) error {
	if err := crdv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return err
	}
	return lwsv1.AddToScheme(scheme)
}

// Controller adapts controller-runtime requests to the infrastructure-neutral
// domain reconciler.  PostgreSQL remains the source of the current generation;
// the CR is only an event source and Kubernetes desired-state projection.
type Controller struct {
	Client       client.Client
	PollInterval time.Duration
	Work         WorkNotifier
	// Status is optional during incremental rollout. When configured, it
	// copies the durable PostgreSQL projection into the CR status subresource
	// after the durable wake-up has been recorded.
	Status *StatusProjector
}

// WorkNotifier is the durable wake-up boundary. Implementations must update
// PostgreSQL resource_work; an event is only a hint and never the queue.
type WorkNotifier interface {
	Notify(context.Context, string, string, int64) error
}

var _ reconcile.Reconciler = (*Controller)(nil)

// Reconcile loads the CR, resolves its stable tenant/service labels, then
// delegates all durable work to the domain reconciler.  A missing CR is a
// normal delete event.  RequeueAfter is a safety net for lost watch events;
// it is not the durable work queue.
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if c.Client == nil {
		return reconcile.Result{}, fmt.Errorf("kubernetes controller client is nil")
	}
	if c.Work == nil {
		return reconcile.Result{}, fmt.Errorf("inference controller work notifier is nil")
	}

	obj := &crdv1.InferenceService{}
	if err := c.Client.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	tenantID, serviceID, err := identityFromObject(obj)
	if err != nil {
		return reconcile.Result{}, err
	}
	generation := obj.Spec.Generation
	if generation < 1 {
		return reconcile.Result{}, fmt.Errorf("inference service %s/%s has invalid generation", obj.GetNamespace(), obj.GetName())
	}
	if err := c.Work.Notify(ctx, tenantID, serviceID, generation); err != nil {
		return reconcile.Result{}, err
	}
	if c.Status != nil {
		if err := c.Status.Project(ctx, req.NamespacedName); err != nil {
			return reconcile.Result{}, err
		}
	}

	interval := c.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return reconcile.Result{RequeueAfter: interval}, nil
}

func identityFromObject(obj client.Object) (tenantID, serviceID string, err error) {
	labels := obj.GetLabels()
	tenantID = labels[tenantIDLabel]
	serviceID = labels[serviceIDLabel]
	if tenantID == "" || serviceID == "" {
		return "", "", fmt.Errorf("inference service %s/%s is missing %s and %s labels", obj.GetNamespace(), obj.GetName(), tenantIDLabel, serviceIDLabel)
	}
	return tenantID, serviceID, nil
}

// SetupWithManager registers the CRD watch.  Runtime resources can be added
// with explicit label/UID mapping once their durable binding repository is
// available; they must not be adopted solely by name.
func (c *Controller) SetupWithManager(mgr manager.Manager) error {
	if c.Work == nil {
		return fmt.Errorf("inference controller work notifier is nil")
	}
	if c.Client == nil {
		c.Client = mgr.GetClient()
	}
	if c.Status != nil {
		c.Status.Client = c.Client
		c.Status.APIReader = mgr.GetAPIReader()
	}
	if err := AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}
	obj := &crdv1.InferenceService{}
	mapRuntime := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		return c.mapRuntimeObject(ctx, o)
	})
	// Status is a projection and must not turn its own status writes into a
	// durable-work hot loop. Spec changes still increment metadata.generation
	// and wake the worker; runtime watches remain event-driven below.
	return builder.ControllerManagedBy(mgr).For(obj, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&appsv1.Deployment{}, mapRuntime).
		Watches(&corev1.Service{}, mapRuntime).
		Watches(&corev1.Pod{}, mapRuntime).
		Watches(&batchv1.Job{}, mapRuntime).
		Watches(&lwsv1.LeaderWorkerSet{}, mapRuntime).
		Complete(c)
}

// mapRuntimeObject resolves runtime events through the service-id and tenant
// labels against the cached CR list. Runtime Pods commonly have generated
// names, so using the runtime object's name as the CR key would silently drop
// their events. Labels only select candidates; the durable runtime binding
// adapter must still validate UID/resourceVersion before writing observation.
func (c *Controller) mapRuntimeObject(ctx context.Context, o client.Object) []reconcile.Request {
	tenant, service, err := identityFromObject(o)
	if err != nil || c.Client == nil {
		return nil
	}
	var list crdv1.InferenceServiceList
	if err := c.Client.List(ctx, &list,
		client.InNamespace(o.GetNamespace()),
		client.MatchingLabels{tenantIDLabel: tenant, serviceIDLabel: service},
	); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return requests
}

// NotifyRuntimeEvent records a durable wake-up after checking ownership labels
// and the generation carried by the runtime object. UID validation belongs to
// the runtime binding repository before it calls this adapter.
func (c *Controller) NotifyRuntimeEvent(ctx context.Context, obj client.Object) error {
	if c.Work == nil {
		return fmt.Errorf("inference controller work notifier is nil")
	}
	tenant, service, err := identityFromObject(obj)
	if err != nil {
		return err
	}
	generation, err := strconv.ParseInt(obj.GetLabels()[generationLabel], 10, 64)
	if err != nil || generation < 1 {
		return fmt.Errorf("runtime object is missing valid %s label", generationLabel)
	}
	return c.Work.Notify(ctx, tenant, service, generation)
}
