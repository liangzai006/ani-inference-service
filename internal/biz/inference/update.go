package inference

import (
	"context"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
)

type UpdateInput struct {
	TenantID, RequestID, Actor, ServiceID, RequestHash string
	ExpectedGeneration                                 int64
	ModelVersionID                                     string
	ArtifactProvider, ArtifactRef, ArtifactSHA256      string
	ModelCapabilities                                  []string
	ImageRef, ServedModelName, EngineRuntime           string
	RuntimeProvider                                    string
	EngineCommand                                      string
	Resources                                          resources.Normalized
	GPUPlan                                            *gpu.Plan
	GPUPlanDigest                                      string
	Replicas, WorkerReplicas                           int32
	RuntimeMode                                        string
	Endpoint                                           *EndpointSpec
}

type UpdateUseCase interface {
	Update(context.Context, UpdateInput) (*inferencev1.OperationResponse, error)
}
