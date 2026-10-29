//go:build policybridge

package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
	"github.com/liangzai006/ani-inference-service/internal/conf"
	"github.com/liangzai006/ani-inference-service/internal/data/postgres"
	"github.com/liangzai006/ani-inference-service/internal/server"
	"github.com/liangzai006/ani-inference-service/internal/service"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Opt-in cross-repository fixture: the receiver, mTLS middleware, key service,
// repository and publication facts are the real Inference implementations.
// The caller supplies a dedicated migrated PostgreSQL instance and test PKI.
func TestInferenceAccessPolicyBridgeFixture(t *testing.T) {
	if os.Getenv("ANI_POLICY_BRIDGE_ENABLE") != "1" {
		t.Fatal("ANI_POLICY_BRIDGE_ENABLE=1 required for this explicit fixture")
	}
	dsn, tenant, model, ready := os.Getenv("ANI_POLICY_BRIDGE_DSN"), os.Getenv("ANI_POLICY_BRIDGE_TENANT"), os.Getenv("ANI_POLICY_BRIDGE_MODEL"), os.Getenv("ANI_POLICY_BRIDGE_READY")
	if dsn == "" || ready == "" || strings.TrimSpace(model) == "" || os.Getenv("ANI_POLICY_BRIDGE_DEDICATED_DB") != "1" {
		t.Fatal("dedicated DSN, model and ready path required")
	}
	tenantID, err := uuid.Parse(tenant)
	if err != nil || tenantID == uuid.Nil {
		t.Fatal("valid test tenant required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := postgres.NewRepository(pool)
	sid := uuid.New()
	_, err = repo.CreateService(ctx, postgres.CreateAggregateInput{TenantID: tenant, ServiceID: sid.String(), Name: model, ServedModelName: model, ModelVersionID: uuid.NewString(), RequestHash: "cross-repo", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM inference_access_keys WHERE tenant_id=$1",
			"DELETE FROM inference_access_policies WHERE tenant_id=$1",
			"DELETE FROM inference_access_policy_audit_events WHERE tenant_id=$1",
			"UPDATE inference_services SET current_operation_id=NULL WHERE tenant_id=$1",
			"DELETE FROM inference_audit_events WHERE tenant_id=$1",
			"DELETE FROM inference_idempotency_requests WHERE tenant_id=$1",
			"DELETE FROM inference_quota_reservations WHERE tenant_id=$1",
			"DELETE FROM inference_publications WHERE tenant_id=$1",
			"DELETE FROM inference_observation_events WHERE tenant_id=$1",
			"DELETE FROM inference_runtime_bindings WHERE tenant_id=$1",
			"DELETE FROM inference_resource_work WHERE tenant_id=$1",
			"DELETE FROM inference_runtime WHERE tenant_id=$1",
			"DELETE FROM inference_specs WHERE tenant_id=$1",
			"DELETE FROM inference_operations WHERE tenant_id=$1",
			"DELETE FROM inference_services WHERE tenant_id=$1",
		} {
			if _, err := pool.Exec(context.Background(), query, tenant); err != nil {
				t.Error(err)
			}
		}
	})
	err = postgres.New(pool).UpsertPublication(ctx, postgres.UpsertPublicationParams{TenantID: pgtype.UUID{Bytes: tenantID, Valid: true}, ServiceID: pgtype.UUID{Bytes: sid, Valid: true}, Generation: 1, DesiredPhase: "published", ObservedPhase: "published"})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := accesskey.NewSecretCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := accesskey.NewService(repo, cipher)
	keys.Policy = repo
	config := &conf.Server_GRPC{Network: "tcp", Addr: "127.0.0.1:0", Timeout: durationpb.New(3 * time.Second)}
	grpcServer, err := configuredInferenceGRPCServer(config, RPCPolicyMiddleware(InferenceServicePolicy()), server.DirectTenantMiddleware())
	if err != nil {
		t.Fatal(err)
	}
	inferencev1.RegisterInferenceAccessKeyServiceServer(grpcServer, service.NewInferenceAccessKeyServer(keys))
	address := startReceiverTestServer(t, grpcServer)
	if err = os.WriteFile(ready, []byte(address), 0600); err != nil {
		t.Fatal(err)
	}
	// The parent signals completion in the task-private temporary directory.
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready + ".stop"); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("parent did not stop the cross-repository fixture")
}
