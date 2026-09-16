package inference

import (
	"context"
	"errors"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
)

var ErrInvalidPageToken = errors.New("invalid page token")

type ReadUseCase interface {
	GetService(context.Context, string, string) (*inferencev1.InferenceService, error)
	ListServices(context.Context, string, int32, string) ([]*inferencev1.InferenceService, string, error)
	GetOperation(context.Context, string, string) (*inferencev1.Operation, error)
	ListOperations(context.Context, string, string, int32, string) ([]*inferencev1.Operation, string, error)
}
