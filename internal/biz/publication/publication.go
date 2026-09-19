// Package publication defines the narrow adapter owned by the Inference
// operation runner. The publication provider owns routing data; Inference
// only requests withdrawal/publication and records the provider's facts.
package publication

import "context"

type Publication struct {
	TenantID, ServiceID, OperationID string
	Generation                       int64
	// LeaseToken fences the local fact write. Publication providers must use
	// OperationID+Generation for their own idempotency contract.
	LeaseToken string
	// FenceOperationID is the current lifecycle operation. It differs from
	// OperationID when stop/restart withdraws an earlier publication.
	FenceOperationID string
	URL              string
	State            string
}

type Port interface {
	Withdraw(context.Context, Publication) error
	ConfirmWithdrawn(context.Context, Publication) (bool, error)
	Publish(context.Context, Publication) error
	ConfirmPublished(context.Context, Publication) (bool, error)
}

// EndpointResolver returns the address allocated by the publication authority
// after confirmation. The runner persists that observed address with its fence.
type EndpointResolver interface {
	Endpoint(context.Context, Publication) (string, error)
}
