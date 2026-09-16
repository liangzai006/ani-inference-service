package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	crdv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/crd/v1"
	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestStatusProjectionSourceReadsTenantScopedAggregate(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	created, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "status-" + service.String(),
		ModelVersionID: uuid.NewString(), Resources: []byte(`{"requests":{"cpu":"1"},"limits":{"cpu":"1"}}`),
		RequestHash: "status", IdempotencyKey: "status", Replicas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewStatusProjectionSource(pool).GetStatusProjection(ctx, tenant.String(), service.String())
	if err != nil {
		t.Fatal(err)
	}
	if projection.AppliedGeneration != 0 || projection.Operation == nil || projection.Operation.ID != created.OperationID || projection.Operation.Phase != "pending" {
		t.Fatalf("unexpected status projection: %+v", projection)
	}
}

func TestStatusProjectionDoesNotExposeOlderPublicationAsEffective(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	created, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "status-stale-" + service.String(),
		ModelVersionID: uuid.NewString(), Resources: []byte(`{"requests":{"cpu":"1"}}`),
		RequestHash: "status-stale", IdempotencyKey: "status-stale", Replicas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 9, 12, 1, 2, 3, 456000000, time.UTC)
	staleAfter := observedAt.Add(2 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE inference_runtime SET observed_at=$3, stale_after=$4 WHERE tenant_id=$1 AND service_id=$2`, tenant, service, observedAt, staleAfter); err != nil {
		t.Fatal(err)
	}
	q := New(pool)
	if err := q.UpsertPublication(ctx, UpsertPublicationParams{
		TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 1,
		DesiredPhase: "published", ObservedPhase: "published", InvocationUrl: "http://old", LastErrorCode: "",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2`, tenant, service); err != nil {
		t.Fatal(err)
	}
	projection, err := NewStatusProjectionSource(pool).GetStatusProjection(ctx, tenant.String(), service.String())
	if err != nil {
		t.Fatal(err)
	}
	if projection.PublicationPhase != "published" || projection.PublicationEffective {
		t.Fatalf("older publication route was hidden or marked effective: %+v", projection)
	}
	if projection.Operation == nil || projection.Operation.ID != created.OperationID {
		t.Fatalf("current operation missing from single snapshot: %+v", projection)
	}
	if projection.ObservedAt == nil || !projection.ObservedAt.Equal(observedAt) || projection.RuntimeStaleAfter == nil || !projection.RuntimeStaleAfter.Equal(staleAfter) {
		t.Fatalf("runtime freshness was not projected: %+v", projection)
	}
	if err := q.UpsertPublication(ctx, UpsertPublicationParams{
		TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 2,
		DesiredPhase: "published", ObservedPhase: "published", InvocationUrl: "http://new", LastErrorCode: "",
	}); err != nil {
		t.Fatal(err)
	}
	projection, err = NewStatusProjectionSource(pool).GetStatusProjection(ctx, tenant.String(), service.String())
	if err != nil {
		t.Fatal(err)
	}
	if projection.PublicationPhase != "published" || !projection.PublicationEffective {
		t.Fatalf("current generation publication was not effective: %+v", projection)
	}
}

func TestStatusProjectionRejectsWrongTenant(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	if _, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "status-tenant-" + service.String(),
		ModelVersionID: uuid.NewString(), Resources: []byte(`{"requests":{"cpu":"1"}}`),
		RequestHash: "status-tenant", IdempotencyKey: "status-tenant", Replicas: 1,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := NewStatusProjectionSource(pool).GetStatusProjection(context.Background(), uuid.NewString(), service.String())
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("expected wrong tenant to return an error")
	}
}

func TestStatusProjectorRequiresPersistedControlIdentity(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	repo := NewRepository(pool)
	if _, err := repo.CreateService(ctx, CreateAggregateInput{
		TenantID: tenant.String(), ServiceID: service.String(), Name: "control-status-" + service.String(),
		ModelVersionID: uuid.NewString(), Resources: []byte(`{"requests":{"cpu":"1"}}`),
		RequestHash: "control-status", IdempotencyKey: "control-status", Replicas: 1,
	}); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := crdv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cr := &crdv1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Namespace: "inference-test", Name: "svc", UID: "cr-original", ResourceVersion: "20",
			Labels: map[string]string{"ani.kubercloud.com/tenant-id": tenant.String(), "ani.kubercloud.com/service-id": service.String()}},
		Spec: crdv1.InferenceServiceSpec{Generation: 1, DesiredState: "running"},
	}
	check := func(t *testing.T, obj *crdv1.InferenceService, wantSuccess bool) {
		t.Helper()
		cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
		projector := &kube.StatusProjector{Client: cl, APIReader: cl, Source: NewStatusProjectionSource(pool)}
		err := projector.Project(ctx, client.ObjectKeyFromObject(obj))
		if (err == nil) != wantSuccess {
			t.Fatalf("Project success=%v, want %v: %v", err == nil, wantSuccess, err)
		}
		got := &crdv1.InferenceService{}
		if err := cl.Get(ctx, client.ObjectKeyFromObject(obj), got); err != nil {
			t.Fatal(err)
		}
		if wantSuccess && (got.Status.Operation == nil || got.Status.Operation.Kind != "create") {
			t.Fatalf("bound CR did not receive operation status: %+v", got.Status)
		}
		if !wantSuccess && got.ResourceVersion != obj.ResourceVersion {
			t.Fatalf("rejected CR was written: before=%s after=%s", obj.ResourceVersion, got.ResourceVersion)
		}
	}
	t.Run("missing binding", func(t *testing.T) { check(t, cr, false) })
	binding := RuntimeBindingInput{TenantID: tenant.String(), ServiceID: service.String(), Generation: 1,
		ObjectKind: "InferenceService", Role: "control", ObjectNamespace: cr.Namespace, ObjectName: cr.Name,
		ObjectUID: string(cr.UID), ResourceVersion: "10"}
	if err := repo.UpsertRuntimeBindingCAS(ctx, binding); err != nil {
		t.Fatal(err)
	}
	t.Run("bound UID permits status RV advancement", func(t *testing.T) { check(t, cr, true) })
	t.Run("fresh invocation on replacement UID", func(t *testing.T) {
		replacement := cr.DeepCopy()
		replacement.UID = "cr-replacement"
		check(t, replacement, false)
	})
	t.Run("different namespace", func(t *testing.T) {
		other := cr.DeepCopy()
		other.Namespace = "unbound-namespace"
		check(t, other, false)
	})
	t.Run("different name", func(t *testing.T) {
		other := cr.DeepCopy()
		other.Name = "unbound-name"
		check(t, other, false)
	})
	// Stop/update advance desired before apply_cr. The last known CR identity
	// must remain eligible until a newer binding is durably recorded.
	if _, err := pool.Exec(ctx, `UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2`, tenant, service); err != nil {
		t.Fatal(err)
	}
	t.Run("previous generation before next CR apply", func(t *testing.T) { check(t, cr, true) })
	binding.Generation, binding.ObjectUID = 3, "cr-future"
	if err := repo.UpsertRuntimeBindingCAS(ctx, binding); err != nil {
		t.Fatal(err)
	}
	t.Run("future binding cannot replace current identity", func(t *testing.T) { check(t, cr, true) })
	binding.Generation, binding.ObjectUID = 2, "cr-new"
	if err := repo.UpsertRuntimeBindingCAS(ctx, binding); err != nil {
		t.Fatal(err)
	}
	t.Run("latest binding supersedes historical UID", func(t *testing.T) { check(t, cr, false) })
	newCR := cr.DeepCopy()
	newCR.UID = "cr-new"
	newCR.Spec.Generation = 2
	t.Run("latest binding projects", func(t *testing.T) { check(t, newCR, true) })
	if _, err := pool.Exec(ctx, `UPDATE inference_services SET deleted_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, service); err != nil {
		t.Fatal(err)
	}
	t.Run("deleted service cannot project", func(t *testing.T) { check(t, newCR, false) })
}
