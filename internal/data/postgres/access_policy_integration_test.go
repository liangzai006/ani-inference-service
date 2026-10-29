package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policy "github.com/liangzai006/ani-inference-service/internal/biz/accesspolicy"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAccessPolicyAuthorizationIntegration(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	repo := NewRepository(pool)
	model := "policy-model-" + uuid.NewString()
	other := uuid.NewString()
	t.Cleanup(func() {
		_, e := pool.Exec(context.Background(), `DELETE FROM inference_access_keys WHERE tenant_id=$1 OR tenant_id=$2`, tenant, other)
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(context.Background(), `DELETE FROM inference_access_policies WHERE tenant_id=$1 OR tenant_id=$2`, tenant, other)
		if e != nil {
			t.Error(e)
		}
		_, e = pool.Exec(context.Background(), `DELETE FROM inference_access_policy_audit_events WHERE tenant_id=$1 OR tenant_id=$2`, tenant, other)
		if e != nil {
			t.Error(e)
		}
	})
	_, err := repo.CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: service.String(), Name: model, ServedModelName: model, ModelVersionID: uuid.NewString(), RequestHash: "create", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if err = New(pool).UpsertPublication(ctx, UpsertPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 1, DesiredPhase: "published", ObservedPhase: "published"}); err != nil {
		t.Fatal(err)
	}
	// A legacy service-bound deny policy must no longer affect requests.
	if _, err = pool.Exec(ctx, `INSERT INTO inference_service_policies(tenant_id,service_id,allowed_tenants) VALUES($1,$2,ARRAY[]::text[])`, tenant, service); err != nil {
		t.Fatal(err)
	}
	p, err := repo.CreateAccessPolicy(ctx, policy.Policy{TenantID: tenant.String(), Name: "shared", AllowedModelNames: []string{model}, RequestsPerMinute: 2}, "actor", "create")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := repo.CreateAccessPolicy(ctx, policy.Policy{TenantID: other, Name: "foreign", AllowedModelNames: []string{model}}, "actor", "create")
	if err != nil {
		t.Fatal(err)
	}
	keys := accessKeyTestService(t, repo)
	keys.Policy = repo
	now := time.Date(2026, 10, 22, 8, 12, 10, 0, time.UTC)
	keys.Now = func() time.Time { return now }
	create := func(tid, id string) accesskey.CreateResult {
		t.Helper()
		result, e := keys.Create(ctx, accesskey.CreateInput{TenantID: tid, Name: "client", PolicyID: id, CreatedBy: "actor"})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	a, b, foreignKey := create(tenant.String(), p.ID), create(tenant.String(), p.ID), create(other, foreign.ID)
	check := func(k accesskey.CreateResult, want string) {
		t.Helper()
		r, e := keys.Check(ctx, k.Secret, "", model, 1)
		if e != nil || (want == "" && !r.Allowed) || (want != "" && (r.Allowed || r.DenialCode != want)) {
			t.Fatalf("check: result=%+v error=%v want=%s", r, e, want)
		}
	}
	check(a, "")
	check(a, "")
	check(a, "rate_limited")
	check(b, "")
	check(b, "")
	check(b, "rate_limited")
	check(foreignKey, "scope_denied")
	if _, err = repo.GetAccessPolicy(ctx, other, p.ID); !errors.Is(err, policy.ErrNotFound) {
		t.Fatalf("cross tenant policy read: %v", err)
	}
	if _, err = repo.BindAccessPolicy(ctx, other, foreignKey.Record.ID, p.ID, "actor", "bind"); !errors.Is(err, policy.ErrNotFound) {
		t.Fatalf("cross tenant bind: %v", err)
	}
	if err = repo.DeleteAccessPolicy(ctx, tenant.String(), p.ID, "actor", "delete"); !errors.Is(err, policy.ErrInUse) {
		t.Fatalf("delete bound policy: %v", err)
	}
	p.AllowedModelNames = []string{}
	p, err = repo.UpdateAccessPolicy(ctx, p, "actor", "remove-model")
	if err != nil {
		t.Fatal(err)
	}
	check(a, "scope_denied")
	check(b, "scope_denied")
	p.AllowedModelNames = []string{model}
	p, err = repo.UpdateAccessPolicy(ctx, p, "actor", "restore")
	if err != nil {
		t.Fatal(err)
	}
	check(a, "rate_limited")
	now = now.Add(time.Minute)
	check(a, "")
	check(b, "")
	if _, err = pool.Exec(ctx, `UPDATE inference_access_keys SET policy_id=NULL WHERE tenant_id=$1 AND id=$2`, tenant, a.Record.ID); err != nil {
		t.Fatal(err)
	}
	check(a, "scope_denied")
	if _, err = repo.BindAccessPolicy(ctx, tenant.String(), a.Record.ID, p.ID, "actor", "bind"); err != nil {
		t.Fatal(err)
	}
	check(a, "")
	now = now.Add(time.Minute)
	if result, err := keys.Check(ctx, a.Secret, "", model, 3); err != nil || result.Allowed || result.DenialCode != "rate_limited" {
		t.Fatalf("oversized first request must be limited: %+v %v", result, err)
	}
	check(a, "") // The rejected oversized request must not consume the bucket.
}

func TestAccessPolicyManagementRPCIntegration(t *testing.T) {
	base, pool, tenant, _ := lifecycleDatabase(t)
	repo := NewRepository(pool)
	keys := accessKeyTestService(t, repo)
	server := service.NewInferenceAccessKeyServer(keys)
	ctx := service.WithActor(service.WithTenantID(base, tenant.String()), "governance:user:test")
	t.Cleanup(func() {
		for _, statement := range []string{`DELETE FROM inference_access_keys WHERE tenant_id=$1`, `DELETE FROM inference_access_policies WHERE tenant_id=$1`, `DELETE FROM inference_access_policy_audit_events WHERE tenant_id=$1`} {
			if _, err := pool.Exec(context.Background(), statement, tenant); err != nil {
				t.Error(err)
			}
		}
	})
	createPolicy := func(name string) *inferencev1.InferenceAccessPolicy {
		t.Helper()
		p, err := server.CreateInferenceAccessPolicy(ctx, &inferencev1.CreateInferenceAccessPolicyRequest{RequestId: "req", Policy: &inferencev1.InferenceAccessPolicy{Name: name, AllowedModelNames: []string{"model-a"}, RequestsPerMinute: 2}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1, p2 := createPolicy("one"), createPolicy("two")
	created, err := server.CreateInferenceAccessKey(ctx, &inferencev1.CreateInferenceAccessKeyRequest{RequestId: "req", Name: "key", PolicyId: p1.Id})
	if err != nil || created.Key.GetPolicyId() != p1.Id {
		t.Fatalf("create key: %v", err)
	}
	if _, err = server.DeleteInferenceAccessPolicy(ctx, &inferencev1.DeleteInferenceAccessPolicyRequest{Id: p1.Id, RequestId: "req"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("delete bound policy: %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE inference_access_keys SET secret_ciphertext='invalid' WHERE tenant_id=$1 AND id=$2`, tenant, created.Key.Id); err != nil {
		t.Fatal(err)
	}
	bound, err := server.BindInferenceAccessKeyPolicy(ctx, &inferencev1.BindInferenceAccessKeyPolicyRequest{KeyId: created.Key.Id, PolicyId: p2.Id, RequestId: "req"})
	if err != nil || bound.GetPolicyId() != p2.Id || bound.GetSecret() != "" {
		t.Fatalf("bind with invalid ciphertext: %+v, %v", bound, err)
	}
	if _, err = server.DeleteInferenceAccessPolicy(ctx, &inferencev1.DeleteInferenceAccessPolicyRequest{Id: p1.Id, RequestId: "req"}); err != nil {
		t.Fatal(err)
	}
	other := service.WithTenantID(ctx, uuid.NewString())
	if _, err = server.GetInferenceAccessPolicy(other, &inferencev1.GetInferenceAccessPolicyRequest{Id: p2.Id}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross tenant: %v", err)
	}
	if _, err = server.CreateInferenceAccessKey(ctx, &inferencev1.CreateInferenceAccessKeyRequest{RequestId: "req", Name: "old", PolicyId: p2.Id, AllowedModelNames: []string{"model-a"}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("legacy key scope accepted: %v", err)
	}
}
