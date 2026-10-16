package inference

import (
	"context"
	"errors"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"time"
)

var ErrInvalidPageToken = errors.New("invalid page token")

type ReadUseCase interface {
	GetService(context.Context, string, string) (*inferencev1.InferenceService, error)
	ListServicesPage(context.Context, string, ListOptions) ([]*inferencev1.InferenceService, int64, error)
	GetOperation(context.Context, string, string) (*inferencev1.Operation, error)
	ListOperationsPage(context.Context, string, string, ListOptions) ([]*inferencev1.Operation, int64, error)
}

// ListOptions is the tenant-scoped, deterministic page query shared by the
// service and PostgreSQL adapter. Offset is zero based and Limit is bounded by
// the transport layer before it reaches storage.
type ListOptions struct {
	Limit      int32
	Offset     int64
	Status     string
	Capability string
	// Filters are intentionally kept here so count and list use the same
	// predicate shape when the query grows.
	BeforeCreatedAt time.Time
	BeforeID        string
}
