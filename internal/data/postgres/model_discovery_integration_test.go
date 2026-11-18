package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policy "github.com/liangzai006/ani-inference-service/internal/biz/accesspolicy"
)

func TestModelDiscoveryUsesCurrentPolicyTenantPublicationAndSharedRateLimit(t *testing.T) {
	ctx, pool, tenant, serviceID := lifecycleDatabase(t)
	_, _, foreign, foreignService := lifecycleDatabase(t)
	repo := NewRepository(pool)
	keys := accessKeyTestService(t, repo)
	keys.Policy = repo
	now := time.Now().UTC()
	keys.Now = func() time.Time { return now }
	t.Cleanup(func() {
		for _, table := range []string{"inference_access_keys", "inference_access_policies", "inference_access_policy_audit_events"} {
			if _, err := pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE tenant_id=$1 OR tenant_id=$2", tenant, foreign); err != nil {
				t.Error(err)
			}
		}
	})
	model := "model-" + uuid.NewString()
	foreignModel := "model-" + uuid.NewString()
	for _, item := range []struct {
		tenant, service uuid.UUID
		model           string
	}{{tenant, serviceID, model}, {foreign, foreignService, foreignModel}} {
		_, err := repo.CreateService(ctx, CreateAggregateInput{TenantID: item.tenant.String(), ServiceID: item.service.String(), Name: item.model, ServedModelName: item.model, ModelVersionID: uuid.NewString(), RequestHash: "create", IdempotencyKey: "create"})
		if err != nil {
			t.Fatal(err)
		}
		if err := New(pool).UpsertPublication(ctx, UpsertPublicationParams{TenantID: pgUUID(item.tenant), ServiceID: pgUUID(item.service), Generation: 1, DesiredPhase: "published", ObservedPhase: "published"}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := repo.CreateAccessPolicy(ctx, policy.Policy{TenantID: tenant.String(), Name: "discovery", AllowedModelNames: []string{model, foreignModel, "missing-model"}, RequestsPerMinute: 2}, "actor", "create")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.Create(ctx, accesskey.CreateInput{TenantID: tenant.String(), Name: "client", PolicyID: p.ID, CreatedBy: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	decision, models, err := keys.ListModels(ctx, key.Secret)
	if err != nil || !decision.Allowed || len(models) != 1 || models[0].ID != model || models[0].Created.IsZero() {
		t.Fatalf("listing must exclude foreign and missing models: allowed=%v models=%v err=%v", decision.Allowed, models, err)
	}
	decision, err = keys.Check(ctx, key.Secret, "", model, 1)
	if err != nil || !decision.Allowed {
		t.Fatalf("second shared-bucket request: allowed=%v err=%v", decision.Allowed, err)
	}
	decision, models, err = keys.ListModels(ctx, key.Secret)
	if err != nil || decision.Allowed || decision.DenialCode != "rate_limited" || len(models) != 0 {
		t.Fatalf("discovery bypassed rate bucket: allowed=%v denial=%s err=%v", decision.Allowed, decision.DenialCode, err)
	}
	now = now.Add(time.Minute)
	p.AllowedModelNames = []string{}
	if _, err = repo.UpdateAccessPolicy(ctx, p, "actor", "empty"); err != nil {
		t.Fatal(err)
	}
	decision, models, err = keys.ListModels(ctx, key.Secret)
	if err != nil || !decision.Allowed || len(models) != 0 {
		t.Fatalf("current empty policy must return empty list: allowed=%v models=%v err=%v", decision.Allowed, models, err)
	}
	if _, err = keys.Revoke(ctx, tenant.String(), key.Record.ID, "actor", "revoke"); err != nil {
		t.Fatal(err)
	}
	decision, models, err = keys.ListModels(ctx, key.Secret)
	if err != nil || decision.Allowed || decision.DenialCode != "revoked" || len(models) != 0 {
		t.Fatalf("revoked key listed models: allowed=%v denial=%s err=%v", decision.Allowed, decision.DenialCode, err)
	}
}
