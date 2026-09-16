package inference

import (
	"context"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

// CreateInput is the transport-independent command accepted after admission.
type CreateInput struct {
	TenantID, RequestID, Actor, Name, ModelVersionID string
	ArtifactProvider, ArtifactRef, ArtifactSHA256    string
	ImageRef, ServedModelName, EngineRuntime         string
	CommandArgv                                      []string
	Resources                                        resources.Normalized
	Replicas, WorkerReplicas                         int32
	RuntimeMode                                      string
	Endpoint                                         *EndpointSpec
	RequestHash                                      string
}

type EndpointSpec struct {
	ContainerPort int32
	ServicePort   int32
	TargetPort    string
	Protocol      string
}

type CreateUseCase interface {
	Create(context.Context, CreateInput) (*inferencev1.OperationResponse, error)
}
