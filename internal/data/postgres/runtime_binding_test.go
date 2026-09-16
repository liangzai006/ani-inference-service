package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRuntimeBindingCASFencesUIDAndResourceVersion(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, service := uuid.New(), uuid.New()
	if _, err := p.Exec(ctx, "INSERT INTO inference_services(tenant_id,id,name,desired_state,desired_generation) VALUES($1,$2,$3,'running',1)", tenant, service, "binding-"+service.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, statement := range []string{
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1 AND service_id=$2",
			"DELETE FROM inference_services WHERE tenant_id=$1 AND id=$2",
		} {
			if _, err := p.Exec(context.Background(), statement, tenant, service); err != nil {
				t.Errorf("cleanup %q: %v", statement, err)
			}
		}
	})
	r := NewRepository(p)
	base := RuntimeBindingInput{TenantID: tenant.String(), ServiceID: service.String(), Generation: 1, ObjectKind: "Deployment", ObjectNamespace: "ns", ObjectName: "svc", ObjectUID: "uid-1", ResourceVersion: "10", Role: "runtime"}
	if err := r.UpsertRuntimeBindingCAS(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.ExpectedUID, base.ExpectedResourceVersion = "uid-1", "10"
	base.ResourceVersion = "11"
	if err := r.UpsertRuntimeBindingCAS(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.ExpectedUID, base.ExpectedResourceVersion = "uid-old", "10"
	base.ObjectUID, base.ResourceVersion = "uid-2", "12"
	if err := r.UpsertRuntimeBindingCAS(ctx, base); !errors.Is(err, ErrRuntimeBindingCAS) {
		t.Fatalf("stale binding err=%v", err)
	}
}
