// Package accesspolicy defines reusable policies for inference credentials.
package accesspolicy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	accesskey "github.com/liangzai006/ani-inference-service/internal/biz/accesskey"
)

var ErrInvalidInput = errors.New("invalid inference access policy")
var ErrNotFound = errors.New("inference access policy not found")
var ErrInUse = errors.New("inference access policy is bound to a key")

type Repository interface {
	CreateAccessPolicy(context.Context, Policy, string, string) (Policy, error)
	GetAccessPolicy(context.Context, string, string) (Policy, error)
	ListAccessPolicies(context.Context, string, int, int) ([]Policy, int64, error)
	UpdateAccessPolicy(context.Context, Policy, string, string) (Policy, error)
	DeleteAccessPolicy(context.Context, string, string, string, string) error
	BindAccessPolicy(context.Context, string, string, string, string, string) (accesskey.Record, error)
}

// Policy describes the model names and request limits granted to a key.
// A zero limit means unlimited. An empty model list grants no model access.
type Policy struct {
	ID                string
	TenantID          string
	Name              string
	AllowedModelNames []string
	RequestsPerSecond int32
	RequestsPerMinute int32
	RequestsPerHour   int32
	Version           int64
}

// Normalize validates and canonicalizes the editable policy fields.
func (p Policy) Normalize() (Policy, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return Policy{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if p.RequestsPerSecond < 0 || p.RequestsPerMinute < 0 || p.RequestsPerHour < 0 {
		return Policy{}, fmt.Errorf("%w: rate limits cannot be negative", ErrInvalidInput)
	}
	models := make(map[string]struct{}, len(p.AllowedModelNames))
	for _, model := range p.AllowedModelNames {
		model = strings.TrimSpace(model)
		if model == "" {
			return Policy{}, fmt.Errorf("%w: model names cannot be empty", ErrInvalidInput)
		}
		models[model] = struct{}{}
	}
	p.AllowedModelNames = make([]string, 0, len(models))
	for model := range models {
		p.AllowedModelNames = append(p.AllowedModelNames, model)
	}
	sort.Strings(p.AllowedModelNames)
	return p, nil
}

func (p Policy) AllowsModel(model string) bool {
	for _, allowed := range p.AllowedModelNames {
		if allowed == model {
			return true
		}
	}
	return false
}
