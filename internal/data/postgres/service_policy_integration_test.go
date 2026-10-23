package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policybiz "github.com/liangzai006/ani-inference-service/internal/biz/servicepolicy"
)

func TestServicePolicyIntegration(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	repo := NewRepository(pool)
	model := "policy-" + uuid.NewString()
	_, err := repo.CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: service.String(), Name: model, ServedModelName: model, ModelVersionID: uuid.NewString(), RequestHash: "create", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	q := New(pool)
	if err = q.UpsertPublication(ctx, UpsertPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(service), Generation: 1, DesiredPhase: "published", ObservedPhase: "published"}); err != nil {
		t.Fatal(err)
	}
	policies := &policybiz.Service{Repository: repo}
	keys := accesskey.NewService(repo)
	keys.Policy = repo
	// Fixed time keeps limits deterministic even across a wall-clock boundary.
	now := time.Date(2026, 10, 22, 8, 12, 10, 0, time.UTC)
	keys.Now = func() time.Time { return now }
	other := uuid.NewString()
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM inference_access_keys WHERE tenant_id=$1 OR tenant_id=$2", tenant.String(), other)
		if err != nil {
			t.Error(err)
		}
	})
	createKey := func(tid string) accesskey.CreateResult {
		t.Helper()
		key, err := keys.Create(ctx, accesskey.CreateInput{TenantID: tid, Name: "policy-key", AllowedServiceIDs: []string{service.String()}, AllowedModelNames: []string{model}, CreatedBy: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	ownerKey, foreignKey := createKey(tenant.String()), createKey(other)
	check := func(key accesskey.CreateResult, want string) {
		t.Helper()
		out, err := keys.Check(ctx, key.Secret, "", model, 1)
		if err != nil {
			t.Fatal(err)
		}
		if want == "" && !out.Allowed || want != "" && (out.Allowed || out.DenialCode != want) {
			t.Fatalf("check allowed=%v code=%q want=%q", out.Allowed, out.DenialCode, want)
		}
	}
	check(ownerKey, "")
	check(foreignKey, "policy_denied")
	if _, err = policies.Get(ctx, other, service.String()); !errors.Is(err, policybiz.ErrNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if _, err = policies.Update(ctx, other, "test", "req", policybiz.Policy{ResourceID: service.String()}); !errors.Is(err, policybiz.ErrNotFound) {
		t.Fatalf("cross-tenant write: %v", err)
	}
	p := policybiz.Policy{ResourceID: service.String(), AllowedTenants: []string{tenant.String(), other}, RateLimits: map[string]policybiz.RateLimit{other: {RequestsPerMinute: 2, RequestsPerHour: 3}}}
	if _, err = policies.Update(ctx, tenant.String(), "test", "bind", p); err != nil {
		t.Fatal(err)
	}
	// Two different keys share the same tenant's service allowance.
	foreignKey2 := createKey(other)
	check(foreignKey, "")
	check(foreignKey2, "")
	check(foreignKey, "rate_limited")
	p.RateLimits[other] = policybiz.RateLimit{RequestsPerMinute: 1, RequestsPerHour: 3}
	if _, err = policies.Update(ctx, tenant.String(), "test", "lower", p); err != nil {
		t.Fatal(err)
	}
	check(foreignKey, "rate_limited") // replacement must not reset counters
	now = now.Add(time.Minute)
	check(foreignKey, "")
	check(foreignKey, "rate_limited") // hour limit
	p.AllowedTenants = []string{}
	p.RateLimits = nil
	if _, err = policies.Update(ctx, tenant.String(), "test", "clear", p); err != nil {
		t.Fatal(err)
	}
	check(ownerKey, "policy_denied")
	check(foreignKey, "policy_denied")
	got, err := policies.Get(ctx, tenant.String(), service.String())
	if err != nil || len(got.AllowedTenants) != 0 || len(got.RateLimits) != 0 {
		t.Fatalf("explicit clear: %+v %v", got, err)
	}
	// Reject invalid replacement and retain the committed deny-all policy.
	p.AllowedTenants = []string{other}
	p.RateLimits = map[string]policybiz.RateLimit{other: {RequestsPerMinute: -1}}
	if _, err = policies.Update(ctx, tenant.String(), "test", "invalid", p); !errors.Is(err, policybiz.ErrInvalidInput) {
		t.Fatalf("negative rate: %v", err)
	}
	check(foreignKey, "policy_denied")
	// Atomic shared limit under concurrent requests from different keys.
	p.RateLimits = map[string]policybiz.RateLimit{other: {RequestsPerMinute: 1}}
	if _, err = policies.Update(ctx, tenant.String(), "test", "again", p); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	var wg sync.WaitGroup
	results := make(chan accesskey.CheckResult, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := foreignKey
			if i%2 == 0 {
				key = foreignKey2
			}
			out, err := keys.Check(ctx, key.Secret, "", model, 1)
			results <- out
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	allowed := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for out := range results {
		if out.Allowed {
			allowed++
		} else if out.DenialCode != "rate_limited" {
			t.Fatalf("concurrent denial: %s", out.DenialCode)
		}
	}
	if allowed != 1 {
		t.Fatalf("concurrent allowed=%d want 1", allowed)
	}
	// Authenticated caller cannot spoof a different service to select its policy.
	out, err := keys.Check(ctx, ownerKey.Secret, uuid.NewString(), model, 1)
	if err != nil || out.Allowed || out.DenialCode != "scope_denied" {
		t.Fatalf("spoof: %v %v", out, err)
	}
	// The policy and its audit are one commit: an audit failure rolls back the
	// allowlist replacement rather than silently losing attribution.
	functionName := "fail_policy_audit_" + uuid.NewString()[:8]
	triggerName := functionName + "_trigger"
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic policy audit failure'; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON inference_service_policy_audit_events FOR EACH ROW WHEN (NEW.tenant_id='%s'::uuid) EXECUTE FUNCTION %s()`, functionName, triggerName, tenant.String(), functionName))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrigger := func() {
		_, err := pool.Exec(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON inference_service_policy_audit_events; DROP FUNCTION IF EXISTS %s()", triggerName, functionName))
		if err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(cleanupTrigger)
	_, err = policies.Update(ctx, tenant.String(), "test", "rollback", policybiz.Policy{ResourceID: service.String(), AllowedTenants: []string{tenant.String()}})
	if err == nil {
		t.Fatal("audit failure did not abort replacement")
	}
	unchanged, err := policies.Get(ctx, tenant.String(), service.String())
	if err != nil || !unchanged.Allows(other) || unchanged.Allows(tenant.String()) {
		t.Fatalf("audit failure changed policy: %+v %v", unchanged, err)
	}
	cleanupTrigger()

	// A second service with the same published model makes routing ambiguous.
	duplicate := uuid.New()
	_, err = repo.CreateService(ctx, CreateAggregateInput{TenantID: tenant.String(), ServiceID: duplicate.String(), Name: model + "-duplicate", ServedModelName: model, ModelVersionID: uuid.NewString(), RequestHash: "duplicate", IdempotencyKey: "duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	if err = q.UpsertPublication(ctx, UpsertPublicationParams{TenantID: pgUUID(tenant), ServiceID: pgUUID(duplicate), Generation: 1, DesiredPhase: "published", ObservedPhase: "published"}); err != nil {
		t.Fatal(err)
	}
	check(foreignKey, "scope_denied")
	if _, err = pool.Exec(ctx, "UPDATE inference_publications SET desired_phase='withdrawn',observed_phase='withdrawn' WHERE tenant_id=$1 AND service_id=$2", tenant, duplicate); err != nil {
		t.Fatal(err)
	}
	// A too-expensive first request cannot bypass a key's empty rate bucket.
	large, err := keys.Create(ctx, accesskey.CreateInput{TenantID: other, Name: "cost-key", AllowedModelNames: []string{model}, RequestsPerMinute: 1, CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	out, err = keys.Check(ctx, large.Secret, "", model, 2)
	if err != nil || out.Allowed || out.DenialCode != "rate_limited" {
		t.Fatalf("large first cost: %v %v", out, err)
	}
	// The denied key consumption must roll back the service bucket as well.
	check(foreignKey, "")

	if _, err = pool.Exec(ctx, "UPDATE inference_publications SET desired_phase='withdrawn',observed_phase='withdrawn' WHERE tenant_id=$1 AND service_id=$2", tenant, service); err != nil {
		t.Fatal(err)
	}
	check(foreignKey, "scope_denied")
	var audits int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM inference_service_policy_audit_events WHERE tenant_id=$1 AND service_id=$2", tenant, service).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}
