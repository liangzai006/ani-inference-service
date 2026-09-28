package model

import (
	"context"
	"slices"
	"testing"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
)

type createCapture struct {
	in     inference.CreateInput
	called bool
}

func (c *createCapture) Create(_ context.Context, in inference.CreateInput) (*inferencev1.OperationResponse, error) {
	c.in = in
	c.called = true
	return &inferencev1.OperationResponse{}, nil
}
func TestCreateResolvesVersionBeforePersistingSpec(t *testing.T) {
	api := &modelAPIFake{version: readyVersion()}
	next := &createCapture{}
	in := inference.CreateInput{TenantID: "tenant", ModelVersionID: "version-id", RequestID: "distinct-operation-request", EngineRuntime: "custom", CommandArgv: []string{"custom-server", "serve"}}
	_, err := NewCreateUseCase(NewClient(api), next).Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !next.called || api.request.GetModelVersionId() != in.ModelVersionID || next.in.ArtifactRef != api.version.StoragePath || next.in.ArtifactSHA256 != api.version.ChecksumSha256 || next.in.EngineRuntime != in.EngineRuntime || !slices.Equal(next.in.CommandArgv, in.CommandArgv) || next.in.RequestID != in.RequestID {
		t.Fatalf("persisted input=%+v", next.in)
	}
}

func TestCreateRequiresCallerEngineConfiguration(t *testing.T) {
	next := &createCapture{}
	_, err := NewCreateUseCase(NewClient(&modelAPIFake{version: readyVersion()}), next).Create(context.Background(), inference.CreateInput{TenantID: "tenant", ModelVersionID: "version-id"})
	if err == nil || next.called {
		t.Fatalf("missing caller engine accepted: err=%v called=%v", err, next.called)
	}
}
func TestCreateRejectsUnreadyModelBeforeWriting(t *testing.T) {
	v := readyVersion()
	v.Status = "pending"
	next := &createCapture{}
	_, err := NewCreateUseCase(NewClient(&modelAPIFake{version: v}), next).Create(context.Background(), inference.CreateInput{TenantID: "tenant", ModelVersionID: v.Id})
	if err == nil || next.called {
		t.Fatal("pending model reached persistence")
	}
}
func TestCreateRejectsConflictingArtifact(t *testing.T) {
	for _, in := range []inference.CreateInput{{ArtifactRef: "wrong"}, {ArtifactSHA256: "wrong"}} {
		in.TenantID, in.ModelVersionID = "tenant", "version-id"
		next := &createCapture{}
		if _, err := NewCreateUseCase(NewClient(&modelAPIFake{version: readyVersion()}), next).Create(context.Background(), in); err == nil || next.called {
			t.Fatal("conflicting artifact reached persistence")
		}
	}
}

func TestCreatePreservesCallerEngineConfiguration(t *testing.T) {
	next := &createCapture{}
	in := inference.CreateInput{
		TenantID:       "tenant",
		ModelVersionID: "version-id",
		EngineRuntime:  "custom-runtime",
		CommandArgv:    []string{"custom-server", "--model", "/models", "--max-model-len", "8192", "--served-model-name", "custom-qwen"},
	}
	if _, err := NewCreateUseCase(NewClient(&modelAPIFake{version: readyVersion()}), next).Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !next.called || next.in.EngineRuntime != in.EngineRuntime || !slices.Equal(next.in.CommandArgv, in.CommandArgv) {
		t.Fatalf("caller engine configuration was changed: %+v", next.in)
	}
}
