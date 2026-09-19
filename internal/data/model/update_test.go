package model

import (
	"context"
	"testing"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
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
	if !next.called || next.in.ModelVersionID != in.ModelVersionID || next.in.ArtifactRef != api.version.StoragePath || next.in.ArtifactSHA256 != api.version.ChecksumSha256 || next.in.EngineRuntime != "vllm" || len(next.in.CommandArgv) != 2 {
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
