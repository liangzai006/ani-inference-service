// Package servicepolicy owns policies bound directly to inference services.
package servicepolicy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

var ErrNotFound = errors.New("service policy not found")
var ErrInvalidInput = errors.New("invalid service policy")

type RateLimit struct {
	RequestsPerMinute int32 `json:"requests_per_minute"`
	RequestsPerHour   int32 `json:"requests_per_hour"`
}
type Policy struct {
	ResourceID     string               `json:"resource_id"`
	AllowedTenants []string             `json:"allowed_tenants"`
	RateLimits     map[string]RateLimit `json:"rate_limits"`
	UpdatedAt      time.Time            `json:"-"`
	Version        int64                `json:"-"`
}
type Repository interface {
	GetServicePolicy(context.Context, string, string) (Policy, error)
	UpdateServicePolicy(context.Context, string, string, string, Policy) (Policy, error)
}
type Service struct{ Repository Repository }

func ValidID(id string) bool {
	value, err := uuid.Parse(id)
	return err == nil && value != uuid.Nil && value.String() == id
}
func (s *Service) Get(ctx context.Context, tenant, resource string) (Policy, error) {
	if !ValidID(tenant) || !ValidID(resource) {
		return Policy{}, ErrInvalidInput
	}
	return s.Repository.GetServicePolicy(ctx, tenant, resource)
}

// Update fully replaces the bound policy. An empty allowlist explicitly denies
// everyone; a missing policy instead defaults to the owning tenant only.
func (s *Service) Update(ctx context.Context, tenant, actor, requestID string, policy Policy) (Policy, error) {
	if !ValidID(tenant) || !ValidID(policy.ResourceID) || actor == "" || requestID == "" {
		return Policy{}, ErrInvalidInput
	}
	seen := make(map[string]bool)
	allowed := make([]string, 0, len(policy.AllowedTenants))
	for _, id := range policy.AllowedTenants {
		if !ValidID(id) {
			return Policy{}, fmt.Errorf("%w: allowed tenants must be canonical UUIDs", ErrInvalidInput)
		}
		if !seen[id] {
			allowed = append(allowed, id)
			seen[id] = true
		}
	}
	sort.Strings(allowed)
	policy.AllowedTenants = allowed
	for id, limit := range policy.RateLimits {
		if !seen[id] || limit.RequestsPerMinute < 0 || limit.RequestsPerHour < 0 {
			return Policy{}, fmt.Errorf("%w: limits require an allowed tenant and nonnegative values", ErrInvalidInput)
		}
	}
	return s.Repository.UpdateServicePolicy(ctx, tenant, actor, requestID, policy)
}
func (p Policy) Allows(tenant string) bool {
	for _, id := range p.AllowedTenants {
		if id == tenant {
			return true
		}
	}
	return false
}
