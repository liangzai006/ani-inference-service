package accelerator

import (
	"context"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
)

// ResolveGPU adapts the durable inference operation context to the
// transport-neutral accelerator resolver. It is only called by the runner for
// generations that explicitly contain resource.gpu.
func (c *Client) ResolveGPU(ctx context.Context, op inferencebiz.OperationContext) (*gpu.Plan, error) {
	return c.Resolve(ctx, gpu.ResolveInput{
		TenantID:  op.TenantID,
		RequestID: op.RequestID,
		Actor:     op.Actor,
		Request:   op.GPURequest,
	})
}

var _ inferencebiz.GPUResolver = (*Client)(nil)
