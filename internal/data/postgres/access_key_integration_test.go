package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	policy "github.com/liangzai006/ani-inference-service/internal/biz/accesspolicy"
)

type accessKeyPageTrace struct {
	pageRead func()
}

type accessKeyPageQuery struct{}

func accessKeyTestService(t *testing.T, repository *Repository) *accesskey.Service {
	t.Helper()
	cipher, err := accesskey.NewSecretCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	svc := accesskey.NewService(repository, cipher)
	svc.Policy = keyTestPolicy{repository: repository, serviceID: "test-service"}
	return svc
}

// Key lifecycle tests exercise the real key repository without requiring a
// publication. The full publication and policy path has its own integration test.
type keyTestPolicy struct {
	repository *Repository
	serviceID  string
}

func (p keyTestPolicy) Resolve(context.Context, string) (string, error) { return p.serviceID, nil }
func (p keyTestPolicy) ConsumeAccess(ctx context.Context, key accesskey.Record, _ string, model string, now time.Time, cost int64) (accesskey.CheckResult, error) {
	r := accesskey.CheckResult{Record: key, DenialCode: "scope_denied"}
	policy, err := p.repository.GetAccessPolicy(ctx, key.TenantID, key.PolicyID)
	if err != nil {
		return r, err
	}
	if !policy.AllowsModel(model) {
		return r, nil
	}
	key.RequestsPerSecond, key.RequestsPerMinute, key.RequestsPerHour = policy.RequestsPerSecond, policy.RequestsPerMinute, policy.RequestsPerHour
	ok, retry, err := p.repository.Consume(ctx, key, now, cost)
	r.Allowed = ok
	r.RetryAfter = retry
	if !ok {
		r.DenialCode = "rate_limited"
	} else {
		r.DenialCode = ""
	}
	return r, err
}
func createKeyTestPolicy(t *testing.T, ctx context.Context, repo *Repository, tenant string, models []string, rpm int32) string {
	t.Helper()
	p, err := repo.CreateAccessPolicy(ctx, policy.Policy{TenantID: tenant, Name: "test-policy", AllowedModelNames: models, RequestsPerMinute: rpm}, "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, e := repo.pool.Exec(context.Background(), `DELETE FROM inference_access_keys WHERE tenant_id=$1`, tenant)
		if e != nil {
			t.Error(e)
		}
		_, e = repo.pool.Exec(context.Background(), `DELETE FROM inference_access_policies WHERE tenant_id=$1`, tenant)
		if e != nil {
			t.Error(e)
		}
		_, e = repo.pool.Exec(context.Background(), `DELETE FROM inference_access_policy_audit_events WHERE tenant_id=$1`, tenant)
		if e != nil {
			t.Error(e)
		}
	})
	return p.ID
}

func (t *accessKeyPageTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, accessKeyPageQuery{}, strings.Contains(data.SQL, "-- name: ListInferenceAccessKeys"))
}

func (t *accessKeyPageTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if page, _ := ctx.Value(accessKeyPageQuery{}).(bool); page && data.Err == nil {
		t.pageRead()
	}
}

func TestInferenceAccessKeyListSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	writer, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(writer.Close)
	tenant := uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := writer.Exec(cleanupCtx, `DELETE FROM inference_access_keys WHERE tenant_id=$1`, tenant); err != nil {
			t.Errorf("cleanup snapshot keys: %v", err)
		}
	})
	service := accessKeyTestService(t, NewRepository(writer))
	policyID := createKeyTestPolicy(t, ctx, NewRepository(writer), tenant, []string{"snapshot-model"}, 0)
	create := func(name string) error {
		_, err := service.Create(ctx, accesskey.CreateInput{
			TenantID: tenant, Name: name, CreatedBy: "snapshot-test", PolicyID: policyID,
		})
		return err
	}
	if err := create("before-page"); err != nil {
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var writeErr error
	config.ConnConfig.Tracer = &accessKeyPageTrace{pageRead: func() { writeErr = create("after-page") }}
	reader, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	items, total, err := NewRepository(reader).List(ctx, tenant, accesskey.ListOptions{Limit: 10})
	if err != nil || writeErr != nil {
		t.Fatalf("list=%v concurrent insert=%v", err, writeErr)
	}
	var committed int
	if err := writer.QueryRow(ctx, `SELECT count(*) FROM inference_access_keys WHERE tenant_id=$1`, tenant).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if committed != 2 || len(items) != 1 || total != 1 || items[0].Name != "before-page" {
		t.Fatalf("list/count must share a snapshot: committed=%d items=%d total=%d", committed, len(items), total)
	}
}

func TestInferenceAccessKeyRepositoryIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	tenant, otherTenant, serviceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM inference_access_keys WHERE tenant_id=$1 OR tenant_id=$2`, tenant, otherTenant); err != nil {
			t.Errorf("cleanup access keys: %v", err)
		}
	})

	repository := NewRepository(pool)
	service := accessKeyTestService(t, repository)
	service.Policy = keyTestPolicy{repository: repository, serviceID: serviceID}
	policyID := createKeyTestPolicy(t, ctx, repository, tenant, []string{"model-a"}, 2)
	otherPolicyID := createKeyTestPolicy(t, ctx, repository, otherTenant, []string{"model-a"}, 1)
	created, err := service.Create(ctx, accesskey.CreateInput{
		TenantID: tenant, RequestID: "req-create-1", Name: "integration-key",
		PolicyID: policyID, CreatedBy: "integration-admin", IdempotencyKey: "create-1",
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if created.Secret == "" || created.Replayed {
		t.Fatalf("create must return a secret exactly once: %+v", created)
	}
	var storedHash string
	if err := pool.QueryRow(ctx, `SELECT secret_hash FROM inference_access_keys WHERE tenant_id=$1 AND id=$2`, tenant, created.Record.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == created.Secret || storedHash == "" {
		t.Fatal("plaintext access key was persisted")
	}

	replayed, err := service.Create(ctx, accesskey.CreateInput{
		TenantID: tenant, RequestID: "req-create-replay", Name: "integration-key",
		PolicyID: policyID, CreatedBy: "integration-admin", IdempotencyKey: "create-1",
	})
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if !replayed.Replayed || replayed.Record.ID != created.Record.ID || replayed.Secret != "" {
		t.Fatalf("idempotency replay exposed a new secret: %+v", replayed)
	}

	if _, err := service.Get(ctx, otherTenant, created.Record.ID); !errors.Is(err, accesskey.ErrNotFound) {
		t.Fatalf("cross-tenant get was not isolated: %v", err)
	}
	items, total, err := service.List(ctx, tenant, accesskey.ListOptions{Limit: 10})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("list result: total=%d items=%d err=%v", total, len(items), err)
	}

	allowed, err := service.Check(ctx, created.Secret, serviceID, "model-a", 1)
	if err != nil || !allowed.Allowed {
		t.Fatalf("valid check denied: %+v err=%v", allowed, err)
	}
	deniedScope, err := service.Check(ctx, created.Secret, uuid.NewString(), "model-a", 1)
	if err != nil || deniedScope.Allowed || deniedScope.DenialCode != "scope_denied" {
		t.Fatalf("scope check: %+v err=%v", deniedScope, err)
	}

	type rotateResult struct {
		result accesskey.RotateResult
		err    error
	}
	rotations := make(chan rotateResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := service.Rotate(ctx, tenant, created.Record.ID, "rotate-1", "integration-admin", "req-rotate-1")
			rotations <- rotateResult{result: result, err: err}
		}()
	}
	var rotated accesskey.RotateResult
	var replayedRotate accesskey.RotateResult
	for i := 0; i < 2; i++ {
		out := <-rotations
		if out.err != nil {
			t.Fatalf("concurrent rotate: %v", out.err)
		}
		if out.result.Secret != "" {
			rotated = out.result
		} else {
			replayedRotate = out.result
		}
	}
	if rotated.Secret == "" || rotated.Replayed || !replayedRotate.Replayed || replayedRotate.Record.ID != rotated.Record.ID {
		t.Fatalf("concurrent rotate did not replay safely: first=%+v replay=%+v", rotated, replayedRotate)
	}
	oldKey, err := service.Check(ctx, created.Secret, serviceID, "model-a", 1)
	if err != nil || oldKey.Allowed || oldKey.DenialCode != "invalid_key" {
		t.Fatalf("old key remained valid after rotate: %+v err=%v", oldKey, err)
	}
	newKey, err := service.Check(ctx, rotated.Secret, serviceID, "model-a", 1)
	if err != nil || !newKey.Allowed {
		t.Fatalf("rotated key denied: %+v err=%v", newKey, err)
	}

	if _, err := service.Revoke(ctx, tenant, created.Record.ID, "integration-admin", "req-revoke-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	revoked, err := service.Check(ctx, rotated.Secret, serviceID, "model-a", 1)
	if err != nil || revoked.Allowed || revoked.DenialCode != "revoked" {
		t.Fatalf("revoked key remained valid: %+v err=%v", revoked, err)
	}

	rateLimitedKey, err := service.Create(ctx, accesskey.CreateInput{
		TenantID: otherTenant, RequestID: "req-rate-create", Name: "rate-key",
		PolicyID: otherPolicyID, CreatedBy: "integration-admin",
	})
	if err != nil {
		t.Fatalf("create rate key: %v", err)
	}
	first, err := service.Check(ctx, rateLimitedKey.Secret, serviceID, "model-a", 1)
	if err != nil || !first.Allowed {
		t.Fatalf("first rate check denied: %+v err=%v", first, err)
	}
	second, err := service.Check(ctx, rateLimitedKey.Secret, serviceID, "model-a", 1)
	if err != nil || second.Allowed || second.DenialCode != "rate_limited" || second.RetryAfter <= 0 {
		t.Fatalf("second rate check was not limited: %+v err=%v", second, err)
	}

	var auditCount, bucketCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_key_audit_events WHERE tenant_id=$1 AND key_id=$2`, tenant, created.Record.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_key_rate_buckets WHERE tenant_id=$1 AND key_id=$2`, otherTenant, rateLimitedKey.Record.ID).Scan(&bucketCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 || bucketCount != 1 {
		t.Fatalf("durable audit/rate state mismatch: audits=%d buckets=%d", auditCount, bucketCount)
	}
}

func TestInferenceAccessKeySecretPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tenant := uuid.NewString()
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DELETE FROM inference_access_keys WHERE tenant_id=$1", tenant); err != nil {
			t.Error(err)
		}
	})
	repository := NewRepository(pool)
	svc := accessKeyTestService(t, repository)
	policyID := createKeyTestPolicy(t, ctx, repository, tenant, []string{"model"}, 0)
	created, err := svc.Create(ctx, accesskey.CreateInput{TenantID: tenant, Name: "recoverable", PolicyID: policyID, CreatedBy: "test", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext, hash string
	if err := pool.QueryRow(ctx, "SELECT secret_ciphertext,secret_hash FROM inference_access_keys WHERE tenant_id=$1 AND id=$2", tenant, created.Record.ID).Scan(&ciphertext, &hash); err != nil {
		t.Fatal(err)
	}
	if ciphertext == "" || strings.Contains(ciphertext, created.Secret) || hash == created.Secret {
		t.Fatal("plaintext must not be persisted")
	}
	// A separately constructed service and cipher must recover durable values.
	svc = accessKeyTestService(t, NewRepository(pool))
	checkReads := func(secret string) {
		t.Helper()
		got, err := svc.Get(ctx, tenant, created.Record.ID)
		if err != nil || got.Secret != secret {
			t.Fatalf("get persisted credential: %v", err)
		}
		items, total, err := svc.List(ctx, tenant, accesskey.ListOptions{Limit: 10})
		if err != nil || total != 1 || len(items) != 1 || items[0].Secret != secret {
			t.Fatalf("list persisted credential: %v", err)
		}
	}
	checkReads(created.Secret)
	if _, err := svc.Get(ctx, uuid.NewString(), created.Record.ID); !errors.Is(err, accesskey.ErrNotFound) {
		t.Fatalf("cross-tenant get must fail: %v", err)
	}
	// Simulate an existing pre-migration row. Invalid and unauthorized
	// requests must not archive a supplied value, even with the right prefix.
	if _, err := pool.Exec(ctx, "UPDATE inference_access_keys SET secret_ciphertext=NULL WHERE tenant_id=$1 AND id=$2", tenant, created.Record.ID); err != nil {
		t.Fatal(err)
	}
	checkReads("")
	if result, err := svc.Check(ctx, created.Record.KeyPrefix+strings.Repeat("0", 31), "", "model", 1); err != nil || result.Allowed {
		t.Fatalf("invalid legacy credential: %v", err)
	}
	checkReads("")
	if result, err := svc.Check(ctx, created.Secret, "", "model", 1); err != nil || !result.Allowed {
		t.Fatalf("valid legacy credential: %v", err)
	}
	checkReads(created.Secret)
	legacy, err := repository.Get(ctx, tenant, created.Record.ID)
	if err != nil || legacy.Version != created.Record.Version || legacy.SecretHash != hash {
		t.Fatalf("archive changed credential identity: %v", err)
	}
	rotated, err := svc.Rotate(ctx, tenant, created.Record.ID, "rotate", "test", "request")
	if err != nil {
		t.Fatal(err)
	}
	checkReads(rotated.Secret)
	// A delayed archive of the previous version cannot replace the new key.
	if err := repository.ArchiveSecret(ctx, legacy, legacy.SecretCiphertext); err != nil {
		t.Fatal(err)
	}
	checkReads(rotated.Secret)
	if _, err := pool.Exec(ctx, "UPDATE inference_access_keys SET secret_ciphertext='v1:corrupt' WHERE tenant_id=$1 AND id=$2", tenant, created.Record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, tenant, created.Record.ID); !errors.Is(err, accesskey.ErrSecretUnavailable) {
		t.Fatalf("corrupt detail must fail closed: %v", err)
	}
	if items, _, err := svc.List(ctx, tenant, accesskey.ListOptions{Limit: 10}); !errors.Is(err, accesskey.ErrSecretUnavailable) || items != nil {
		t.Fatal("corrupt page must fail without returning partial secrets")
	}
	var auditPayloads string
	if err := pool.QueryRow(ctx, "SELECT coalesce(string_agg(payload::text,','),'') FROM inference_access_key_audit_events WHERE tenant_id=$1", tenant).Scan(&auditPayloads); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditPayloads, created.Secret) || strings.Contains(auditPayloads, rotated.Secret) || strings.Contains(auditPayloads, ciphertext) {
		t.Fatal("audit must not contain plaintext or ciphertext")
	}
}
