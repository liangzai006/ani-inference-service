package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
)

// Hold the first two lookups after their empty reads, so both deliveries reach
// the INSERT with no existing key visible. No production behavior is replaced.
type accessKeyCreateBarrier struct {
	lookups atomic.Int32
	release chan struct{}
}

type accessKeyCreateQuery struct{}

func (b *accessKeyCreateBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, accessKeyCreateQuery{}, strings.Contains(data.SQL, "-- name: GetInferenceAccessKeyByCreateIdempotency"))
}

func (b *accessKeyCreateBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if lookup, _ := ctx.Value(accessKeyCreateQuery{}).(bool); lookup && data.Err == nil {
		n := b.lookups.Add(1)
		if n == 2 {
			close(b.release)
		}
		if n <= 2 {
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		}
	}
}

func TestInferenceAccessKeyConcurrentCreateIntegration(t *testing.T) {
	for _, isolation := range []string{"read committed", "repeatable read"} {
		t.Run(isolation, func(t *testing.T) { testInferenceAccessKeyConcurrentCreate(t, isolation) })
	}
}

func testInferenceAccessKeyConcurrentCreate(t *testing.T, isolation string) {
	t.Helper()
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("set INFERENCE_PG_DSN to run PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = isolation
	config.ConnConfig.Tracer = &accessKeyCreateBarrier{release: make(chan struct{})}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tenant, otherTenant := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM inference_access_keys WHERE tenant_id=$1 OR tenant_id=$2`, tenant, otherTenant); err != nil {
			t.Errorf("cleanup concurrent keys: %v", err)
		}
	})
	repository := NewRepository(pool)
	service := accessKeyTestService(t, repository)
	input := accesskey.CreateInput{
		TenantID: tenant, Name: "concurrent-key", AllowedModelNames: []string{"model-a"},
		CreatedBy: "concurrent-test", IdempotencyKey: "same-key",
	}
	type outcome struct {
		result accesskey.CreateResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := service.Create(ctx, input)
			outcomes <- outcome{result, err}
		}()
	}
	var created, replayed accesskey.CreateResult
	for i := 0; i < 2; i++ {
		out := <-outcomes
		if out.err != nil {
			t.Errorf("concurrent create failed instead of replaying: %v", out.err)
			continue
		}
		if out.result.Replayed {
			replayed = out.result
		} else {
			created = out.result
		}
	}
	if t.Failed() {
		return
	}
	if created.Secret == "" || replayed.Secret != "" || !replayed.Replayed || created.Record.ID != replayed.Record.ID {
		t.Fatal("expected one key, one secret delivery and one replay of the same key")
	}
	var count, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_keys WHERE tenant_id=$1`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_key_audit_events WHERE tenant_id=$1`, tenant).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if count != 1 || audits != 1 {
		t.Fatalf("concurrent delivery duplicated durable state: keys=%d audits=%d", count, audits)
	}

	// The conflict target must not consume another tenant's idempotency key.
	input.TenantID = otherTenant
	other, err := service.Create(ctx, input)
	if err != nil || other.Replayed || other.Secret == "" || other.Record.ID == created.Record.ID {
		t.Fatalf("tenant-scoped idempotency failed: err=%v", err)
	}
	if _, err := service.Get(ctx, otherTenant, created.Record.ID); !errors.Is(err, accesskey.ErrNotFound) {
		t.Fatalf("cross-tenant key lookup was not rejected: %v", err)
	}

	// Only the tenant/idempotency conflict may replay. A prefix collision must
	// keep its original database error and leave no row or audit behind.
	collision := created.Record
	collision.ID = uuid.NewString()
	collision.CreateIdempotencyKey = "different-key"
	if collision.AllowedServiceIDs == nil {
		collision.AllowedServiceIDs = []string{}
	}
	_, err = repository.Create(ctx, accesskey.CreatePersistInput{Record: collision})
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23505" {
		t.Fatalf("unrelated uniqueness error was swallowed: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_keys WHERE tenant_id=$1`, tenant).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inference_access_key_audit_events WHERE tenant_id=$1`, tenant).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if count != 1 || audits != 1 {
		t.Fatalf("failed insert left partial state: keys=%d audits=%d", count, audits)
	}
}
