package postgres_test

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/data/postgres"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestModelReferencesGRPCPostgres(t *testing.T) {
	dsn := os.Getenv("INFERENCE_PG_DSN")
	if dsn == "" {
		t.Skip("INFERENCE_PG_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	tenant, other := uuid.NewString(), uuid.NewString()
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for i, state := range []string{"running", "stopped", "deleted", "deleted", "running"} {
		owner := tenant
		if i == 4 {
			owner = other
		}
		sid := uuid.NewString()
		var deleted *time.Time
		if i == 3 {
			now := time.Now()
			deleted = &now
		}
		if _, err = tx.Exec(ctx, `INSERT INTO inference_services(tenant_id,id,name,desired_state,desired_generation,deleted_at) VALUES($1,$2,$5,$3,2,$4)`, owner, sid, state, deleted, "reference-"+sid); err != nil {
			t.Fatal(err)
		}
		// Generation 1 remains protected even when generation 2 is desired.
		if _, err = tx.Exec(ctx, `INSERT INTO inference_specs(tenant_id,id,service_id,generation,model_version_id,artifact_provider,artifact_ref,artifact_sha256,image_ref,served_model_name,engine_runtime) VALUES($1,$2,$3,1,$4,'model','','','','','')`, owner, uuid.NewString(), sid, ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	token := "Bearer " + strings.Repeat("test-only-", 4)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (interface{}, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != token {
			return nil, status.Error(codes.Unauthenticated, "credential required")
		}
		if claims := md.Get("x-tenant-id"); len(claims) > 0 && (len(claims) != 1 || claims[0] != tenant) {
			return nil, status.Error(codes.PermissionDenied, "tenant mismatch")
		}
		return next(service.WithTenantID(ctx, tenant), req)
	}))
	inferencev1.RegisterModelReferenceServiceServer(server, service.NewModelReferenceServer(postgres.NewReadUseCase(tx)))
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///reference", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := inferencev1.NewModelReferenceServiceClient(conn)
	authctx := metadata.AppendToOutgoingContext(ctx, "authorization", token)
	for i, id := range ids {
		got, err := client.CheckModelVersionReferences(authctx, &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: []string{id}})
		want := i < 3
		if err != nil || got.GetHasActiveReferences() != want {
			t.Fatalf("case %d references=%v err=%v", i, got, err)
		}
	}
	_, err = client.CheckModelVersionReferences(metadata.AppendToOutgoingContext(authctx, "x-tenant-id", other), &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: ids[:1]})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("tenant claim accepted: %v", err)
	}
	_, err = client.CheckModelVersionReferences(metadata.AppendToOutgoingContext(ctx, "x-tenant-id", tenant), &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: ids[:1]})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("tenant header authenticated caller: %v", err)
	}
	got, err := client.CheckModelVersionReferences(authctx, &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: []string{uuid.NewString()}})
	if err != nil || got.GetHasActiveReferences() {
		t.Fatalf("missing reference=%v err=%v", got, err)
	}
}
