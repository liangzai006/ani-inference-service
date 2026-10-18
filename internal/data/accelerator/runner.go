package accelerator

import (
	"context"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
)

// ResolveGPU adapts the durable inference operation context to the
// transport-neutral accelerator resolver. It is only called by the runner for
// generations that explicitly contain resource.gpu.
func (c *Client) ResolveGPU(ctx context.Context, op inferencebiz.OperationContext) (*gpu.Snapshot, error) {
	return c.Resolve(ctx, gpu.ResolveInput{
		TenantID:  op.TenantID,
		RequestID: op.RequestID,
		Actor:     op.Actor,
		Request:   op.GPURequest,
	})
}

var _ inferencebiz.GPUResolver = (*Client)(nil)
