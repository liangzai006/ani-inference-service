package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	inference "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	kube "github.com/liangzai006/ani-inference-service/internal/data/kubernetes"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type placementVerifierStub struct {
	want kube.TenantNamespace
	err  error
}

type staticNamespaceResolver struct{ ns kube.TenantNamespace }

func (s staticNamespaceResolver) Resolve(context.Context, string) (kube.TenantNamespace, error) {
	return s.ns, nil
}

type namespaceCaptureClient struct {
	client.Client
	namespace string
}

var errNamespaceCaptured = errors.New("namespace captured")

func (c *namespaceCaptureClient) Patch(_ context.Context, obj client.Object, _ client.Patch, _ ...client.PatchOption) error {
	c.namespace = obj.GetNamespace()
	return errNamespaceCaptured
}

func TestCreateFlowsFromNamespaceResolutionToCRProjection(t *testing.T) {
	ctx, pool, tenant, _ := lifecycleDatabase(t)
	repo := NewRepository(pool)
	durable := NewCreateUseCase(repo)
	pinned := kube.TenantNamespace{Name: "platform-returned-namespace", UID: "ns-uid"}
	create := &kube.NamespaceCreate{Resolver: staticNamespaceResolver{ns: pinned}, Preflight: durable, Next: durable}
	response, err := create.Create(ctx, inference.CreateInput{TenantID: tenant.String(), Name: "create-flow", ModelVersionID: uuid.NewString(), RuntimeMode: "deployment", RuntimeProvider: "deployment", Replicas: 1, RequestHash: "request"})
	if err != nil {
		t.Fatal(err)
	}
	serviceID := response.GetResource().GetId()
	source := NewRuntimeSource(pool, "ani-system", "cluster-a", "legacy-uid")
	source.NamespaceVerifier = placementVerifierStub{want: pinned}
	scheme := runtime.NewScheme()
	if err := kube.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kc := fake.NewClientBuilder().WithScheme(scheme).Build()
	capture := &namespaceCaptureClient{Client: kc}
	executor := &kube.RuntimeExecutor{Client: kc, CRClient: capture, APIReader: kc, Source: source}
	err = executor.ApplyCR(ctx, inference.OperationContext{TenantID: tenant.String(), ServiceID: serviceID, TargetGeneration: 1, LeaseToken: "lease"})
	if !errors.Is(err, errNamespaceCaptured) || capture.namespace != pinned.Name {
		t.Fatalf("CR projection namespace = %q, err = %v", capture.namespace, err)
	}
}

func (v placementVerifierStub) Verify(_ context.Context, _ string, got kube.TenantNamespace) error {
	if got != v.want {
		return errors.New("unexpected namespace identity")
	}
	return v.err
}

func TestCreateAtomicallyPinsNamespacePlacement(t *testing.T) {
	ctx, pool, tenant, service := lifecycleDatabase(t)
	r := NewRepository(pool)
	in := CreateAggregateInput{TenantID: tenant.String(), ServiceID: service.String(), Name: service.String(), ModelVersionID: uuid.NewString(), RequestHash: "create", RuntimeNamespace: "tenant-namespace"}
	if _, err := r.CreateService(ctx, in); err == nil {
		t.Fatal("accepted missing namespace UID")
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM inference_services WHERE tenant_id=$1 AND id=$2)", tenant, service).Scan(&exists); err != nil || exists {
		t.Fatalf("service escaped failed transaction: %v, %v", exists, err)
	}
	in.RuntimeNamespaceUID = "ns-uid"
	if _, err := r.CreateService(ctx, in); err != nil {
		t.Fatal(err)
	}
	want := kube.TenantNamespace{Name: in.RuntimeNamespace, UID: in.RuntimeNamespaceUID}
	got, err := r.GetNamespacePlacement(ctx, tenant.String(), service.String())
	if err != nil || got != want {
		t.Fatalf("placement: %+v, %v", got, err)
	}
	if _, err := r.GetNamespacePlacement(ctx, uuid.NewString(), service.String()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant placement read: %v", err)
	}
	source := NewRuntimeSource(pool, "ani-system", "cluster-a", "legacy-uid")
	source.NamespaceVerifier = placementVerifierStub{want: want}
	if namespace, err := source.NamespaceForService(ctx, tenant.String(), service.String()); err != nil || namespace != want.Name {
		t.Fatalf("runtime namespace: %q, %v", namespace, err)
	}
	source.NamespaceVerifier = placementVerifierStub{want: want, err: errors.New("UID changed")}
	if _, err := source.NamespaceForService(ctx, tenant.String(), service.String()); err == nil {
		t.Fatal("changed namespace accepted")
	}
}
