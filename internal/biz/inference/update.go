package inference

import (
	"context"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

type UpdateInput struct {
	TenantID, RequestID, Actor, ServiceID, RequestHash string
	ExpectedGeneration                                 int64
	ModelVersionID                                     string
	ArtifactProvider, ArtifactRef, ArtifactSHA256      string
	ImageRef, ServedModelName, EngineRuntime           string
	CommandArgv                                        []string
	Resources                                          resources.Normalized
	Replicas, WorkerReplicas                           int32
	RuntimeMode                                        string
	Endpoint                                           *EndpointSpec
}

type UpdateUseCase interface {
	Update(context.Context, UpdateInput) (*inferencev1.OperationResponse, error)
}
