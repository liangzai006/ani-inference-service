package accesskey

import (
	"context"
	"errors"
	"strings"
	"time"
)

type PublishedModel struct {
	ID      string
	Created time.Time
}

// ModelDiscoveryPolicy uses the same current credential, policy, publication
// and rate buckets as calls, without requiring an individual model to be named.
type ModelDiscoveryPolicy interface {
	DiscoverAccess(context.Context, Record, time.Time) (CheckResult, []PublishedModel, error)
}

func (s *Service) ListModels(ctx context.Context, apiKey string) (CheckResult, []PublishedModel, error) {
	decision, err := s.authenticate(ctx, strings.TrimSpace(apiKey))
	if err != nil || !decision.Allowed {
		return decision, nil, err
	}
	policy, ok := s.Policy.(ModelDiscoveryPolicy)
	if !ok {
		return CheckResult{}, nil, errors.New("inference model discovery is not configured")
	}
	return policy.DiscoverAccess(ctx, decision.Record, s.now())
}
