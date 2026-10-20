package inference

import (
	"context"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	"github.com/liangzai006/ani-inference-service/internal/biz/resources"
)

// CreateInput is the transport-independent command accepted after admission.
type CreateInput struct {
	TenantID, RequestID, Actor, Name, ModelVersionID string
	ArtifactProvider, ArtifactRef, ArtifactSHA256    string
	ModelCapabilities                                []string
	ImageRef, ServedModelName, EngineRuntime         string
	RuntimeProvider                                  string
	EngineCommand                                    string
	Resources                                        resources.Normalized
	GPUSnapshot                                      *gpu.Snapshot
	GPUSnapshotDigest                                string
	Replicas, WorkerReplicas                         int32
	RuntimeMode                                      string
	Endpoint                                         *EndpointSpec
	RequestHash                                      string
	Managed                                          *ManagedCommand
}

// ManagedCommand preserves the trusted command independently of later
// local generations. Payload is the complete transport command, Business is
// the canonical body without attachment/IDs, and Context is the original
// immutable ledger/plan reference used by the refund client.
type ManagedCommand struct {
	Context                     gpu.RefundContext
	ResourceClass               string
	CreateCommandDigest         string
	Actor                       string
	Payload, Business           []byte
	BusinessDigest, RequestHash string
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

// CreatePreflight checks durable replay and closing before external Model reads.
type CreatePreflight interface {
	PrepareCreate(context.Context, CreateInput) (*inferencev1.OperationResponse, bool, error)
}
