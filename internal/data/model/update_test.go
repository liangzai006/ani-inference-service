package model

import (
	"context"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

type updateCapture struct {
	in     inference.UpdateInput
	called bool
}

func (c *updateCapture) Update(_ context.Context, in inference.UpdateInput) (*inferencev1.OperationResponse, error) {
	c.in = in
	c.called = true
	return &inferencev1.OperationResponse{}, nil
}

func TestUpdateResolvesModelVersionBeforePersistingSpec(t *testing.T) {
	api := &modelAPIFake{version: readyVersion()}
	next := &updateCapture{}
	in := inference.UpdateInput{TenantID: "tenant", ModelVersionID: "version-id", RequestID: "switch-request"}
	if _, err := NewUpdateUseCase(NewClient(api), next).Update(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !next.called || next.in.ModelVersionID != in.ModelVersionID || next.in.ArtifactRef != api.version.StoragePath || next.in.ArtifactSHA256 != api.version.ChecksumSha256 || next.in.EngineRuntime != "" || next.in.EngineCommand != "" {
		t.Fatalf("persisted input=%+v", next.in)
	}
}

func TestUpdateRejectsUnreadyModelBeforeWriting(t *testing.T) {
	v := readyVersion()
	v.Status = "pending"
	next := &updateCapture{}
	_, err := NewUpdateUseCase(NewClient(&modelAPIFake{version: v}), next).Update(context.Background(), inference.UpdateInput{TenantID: "tenant", ModelVersionID: v.Id})
	if err == nil || next.called {
		t.Fatal("pending model reached persistence")
	}
}

func TestUpdatePreservesCallerEngineConfiguration(t *testing.T) {
	next := &updateCapture{}
	in := inference.UpdateInput{
		TenantID:       "tenant",
		ModelVersionID: "version-id",
		EngineRuntime:  "custom-runtime",
		EngineCommand:  "custom-server --model /models --max-model-len 8192 --served-model-name custom-qwen",
	}
	if _, err := NewUpdateUseCase(NewClient(&modelAPIFake{version: readyVersion()}), next).Update(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !next.called || next.in.EngineRuntime != in.EngineRuntime || next.in.EngineCommand != in.EngineCommand {
		t.Fatalf("caller engine configuration was changed: %+v", next.in)
	}
}

type updateReplayCapture struct {
	updateCapture
	response *inferencev1.OperationResponse
}

func (c *updateReplayCapture) PrepareUpdate(context.Context, inference.UpdateInput) (*inferencev1.OperationResponse, bool, error) {
	return c.response, true, nil
}
func TestUpdatePersistentReplayPrecedesModel(t *testing.T) {
	api := &modelAPIFake{}
	expected := &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: "accepted-update"}}
	next := &updateReplayCapture{response: expected}
	out, err := NewUpdateUseCase(NewClient(api), next).Update(context.Background(), inference.UpdateInput{ModelVersionID: "original"})
	if err != nil || out != expected || api.request != nil || next.called {
		t.Fatalf("update replay visited Model: out=%v err=%v request=%v", out, err, api.request)
	}
}
func TestEngineOnlyUpdateDoesNotReadModel(t *testing.T) {
	api := &modelAPIFake{}
	next := &updateCapture{}
	_, err := NewUpdateUseCase(NewClient(api), next).Update(context.Background(), inference.UpdateInput{UpdateMask: []string{"engine"}, EngineRuntime: "vllm", ImageRef: "image:v1", EngineCommand: "serve /models"})
	if err != nil || !next.called || api.request != nil {
		t.Fatalf("engine-only update visited Model: err=%v request=%v called=%v", err, api.request, next.called)
	}
}
