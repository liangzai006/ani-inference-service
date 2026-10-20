package model

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/inference"
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
	in := inference.CreateInput{TenantID: "tenant", ModelVersionID: "version-id", RequestID: "distinct-operation-request", ImageRef: "custom:v1", EngineRuntime: "custom", EngineCommand: "custom-server serve"}
	_, err := NewCreateUseCase(NewClient(api), next).Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !next.called || api.request.GetModelVersionId() != in.ModelVersionID || next.in.ArtifactRef != api.version.StoragePath || next.in.ArtifactSHA256 != api.version.ChecksumSha256 || next.in.EngineRuntime != in.EngineRuntime || next.in.EngineCommand != in.EngineCommand || next.in.RequestID != in.RequestID {
		t.Fatalf("persisted input=%+v", next.in)
	}
}

func TestCreateRequiresCallerEngineConfiguration(t *testing.T) {
	for name, in := range map[string]inference.CreateInput{
		"missing type":    {TenantID: "tenant", ModelVersionID: "version-id", ImageRef: "engine:v1", EngineCommand: "vllm serve /models"},
		"missing image":   {TenantID: "tenant", ModelVersionID: "version-id", EngineRuntime: "vllm", EngineCommand: "vllm serve /models"},
		"missing command": {TenantID: "tenant", ModelVersionID: "version-id", EngineRuntime: "vllm", ImageRef: "engine:v1"},
		"blank image":     {TenantID: "tenant", ModelVersionID: "version-id", EngineRuntime: "vllm", ImageRef: "  ", EngineCommand: "vllm serve /models"},
	} {
		t.Run(name, func(t *testing.T) {
			next := &createCapture{}
			api := &modelAPIFake{version: readyVersion()}
			_, err := NewCreateUseCase(NewClient(api), next).Create(context.Background(), in)
			if err == nil || next.called || api.request != nil {
				t.Fatalf("missing caller engine accepted: err=%v called=%v model_request=%v", err, next.called, api.request)
			}
		})
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
		ImageRef:       "custom-runtime:v2",
		EngineRuntime:  "custom-runtime",
		EngineCommand:  "custom-server --model /models --max-model-len 8192 --served-model-name custom-qwen",
	}
	if _, err := NewCreateUseCase(NewClient(&modelAPIFake{version: readyVersion()}), next).Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !next.called || next.in.ImageRef != in.ImageRef || next.in.EngineRuntime != in.EngineRuntime || next.in.EngineCommand != in.EngineCommand {
		t.Fatalf("caller engine configuration was changed: %+v", next.in)
	}
}

type createReplayCapture struct {
	createCapture
	response *inferencev1.OperationResponse
}

func (c *createReplayCapture) PrepareCreate(context.Context, inference.CreateInput) (*inferencev1.OperationResponse, bool, error) {
	return c.response, true, nil
}
func TestCreatePersistentReplayPrecedesModel(t *testing.T) {
	api := &modelAPIFake{err: status.Error(codes.Unavailable, "Model unavailable")}
	expected := &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: "accepted"}}
	next := &createReplayCapture{response: expected}
	out, err := NewCreateUseCase(NewClient(api), next).Create(context.Background(), inference.CreateInput{ModelVersionID: "original"})
	if err != nil || out != expected || api.request != nil || next.called {
		t.Fatalf("durable replay visited Model or persisted again: out=%v err=%v request=%v called=%v", out, err, api.request, next.called)
	}
}
