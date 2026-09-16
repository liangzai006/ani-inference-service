package postgres

import (
	"testing"

	"github.com/google/uuid"
)

func TestRuntimeSourcePublishedFactRequiresCurrentConfirmedGeneration(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	_, err := NewRepository(pool).CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: service.String(), Name: "publication-" + service.String(), ModelVersionID: uuid.NewString(), RequestHash: "create", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	q := New(pool)
	if rows, err := q.CloneSpecGeneration(ctx, CloneSpecGenerationParams{ID: pgUUID(uuid.New()), TenantID: pgUUID(tenant), ServiceID: pgUUID(service), SourceGeneration: 1, TargetGeneration: 2}); err != nil || rows != 1 {
		t.Fatalf("clone rows=%d err=%v", rows, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE inference_services SET desired_generation=2 WHERE tenant_id=$1 AND id=$2", tenant, service); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		generation        int64
		desired, observed string
		want              bool
	}{
		{1, "published", "published", false},
		{2, "publishing", "withdrawn", false},
		{2, "published", "published", true},
		{2, "withdrawing", "published", false},
		{2, "withdrawn", "withdrawn", false},
	} {
		if err := q.UpsertPublication(ctx, UpsertPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: tt.generation, DesiredPhase: tt.desired, ObservedPhase: tt.observed}); err != nil {
			t.Fatal(err)
		}
		got, err := NewRuntimeSource(pool, "ns").CurrentRuntime(ctx, tenant.String(), service.String(), 2)
		if err != nil || got.PublicationPublished != tt.want {
			t.Fatalf("publication=%+v runtime=%+v err=%v", tt, got, err)
		}
	}
}
