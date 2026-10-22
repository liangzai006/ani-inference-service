// Package accesskey owns Inference data-plane credential behavior. It does not
// know about gRPC, PostgreSQL, Kubernetes, or Governance's control-plane keys.
package accesskey

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

const (
	secretPrefix = "iak_"
	hashVersion  = "argon2id-v1"
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

var (
	ErrNotFound          = errors.New("inference access key not found")
	ErrInvalidInput      = errors.New("invalid inference access key input")
	ErrAlreadyRevoked    = errors.New("inference access key is already revoked")
	ErrExpired           = errors.New("inference access key is expired")
	ErrInvalidSecret     = errors.New("inference access key is invalid")
	ErrScopeDenied       = errors.New("inference access key scope denied")
	ErrRateLimited       = errors.New("inference access key rate limit exceeded")
	ErrIdempotencyReplay = errors.New("inference access key idempotency replay")
)

type Record struct {
	TenantID             string
	ID                   string
	Name                 string
	KeyPrefix            string
	SecretHash           string
	HashVersion          string
	AllowedServiceIDs    []string
	AllowedModelNames    []string
	RequestsPerSecond    int32
	RequestsPerMinute    int32
	RequestsPerHour      int32
	ExpiresAt            *time.Time
	RevokedAt            *time.Time
	CreatedBy            string
	CreatedAt            time.Time
	LastUsedAt           *time.Time
	Version              int64
	CreateIdempotencyKey string
	RotationIdempotency  string
}

type CreateInput struct {
	TenantID          string
	RequestID         string
	Name              string
	AllowedServiceIDs []string
	AllowedModelNames []string
	RequestsPerSecond int32
	RequestsPerMinute int32
	RequestsPerHour   int32
	ExpiresAt         *time.Time
	CreatedBy         string
	IdempotencyKey    string
}

type ListOptions struct {
	Limit  int
	Offset int
}

type Repository interface {
	Create(ctx context.Context, input CreatePersistInput) (PersistResult, error)
	List(ctx context.Context, tenantID string, options ListOptions) ([]Record, int64, error)
	Get(ctx context.Context, tenantID, id string) (Record, error)
	GetByPrefix(ctx context.Context, prefix string) (Record, error)
	Revoke(ctx context.Context, tenantID, id, actor, requestID string) (Record, error)
	Rotate(ctx context.Context, tenantID, id string, input RotatePersistInput) (PersistResult, error)
	Consume(ctx context.Context, record Record, now time.Time, cost int64) (bool, time.Duration, error)
}

type CreatePersistInput struct {
	Record    Record
	RequestID string
}

type PersistResult struct {
	Record   Record
	Replayed bool
}

type RotatePersistInput struct {
	KeyPrefix   string
	SecretHash  string
	HashVersion string
	Idempotency string
	Actor       string
	RequestID   string
}

type CreateResult struct {
	Record   Record
	Secret   string
	Replayed bool
}

type RotateResult struct {
	Record   Record
	Secret   string
	Replayed bool
}

type CheckResult struct {
	Record        Record
	Allowed       bool
	DenialCode    string
	RetryAfter    time.Duration
	PolicyVersion int64
}

type Service struct {
	Repository Repository
	Now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{Repository: repository, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	if s == nil || s.Repository == nil {
		return CreateResult{}, errors.New("inference access-key repository is not configured")
	}
	input.Name = strings.TrimSpace(input.Name)
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.TenantID == "" || input.Name == "" || input.CreatedBy == "" {
		return CreateResult{}, fmt.Errorf("%w: tenant, name and actor are required", ErrInvalidInput)
	}
	if err := validateScopeAndLimits(input.AllowedServiceIDs, input.AllowedModelNames, input.RequestsPerSecond, input.RequestsPerMinute, input.RequestsPerHour, input.ExpiresAt, s.now()); err != nil {
		return CreateResult{}, err
	}
	secret, prefix, err := newSecret()
	if err != nil {
		return CreateResult{}, err
	}
	hash, err := hashSecret(secret)
	if err != nil {
		return CreateResult{}, err
	}
	record := Record{
		TenantID: input.TenantID, ID: uuid.NewString(), Name: input.Name,
		KeyPrefix: prefix, SecretHash: hash, HashVersion: hashVersion,
		AllowedServiceIDs: normalizeStrings(input.AllowedServiceIDs),
		AllowedModelNames: normalizeStrings(input.AllowedModelNames),
		RequestsPerSecond: input.RequestsPerSecond, RequestsPerMinute: input.RequestsPerMinute,
		RequestsPerHour: input.RequestsPerHour, ExpiresAt: input.ExpiresAt,
		CreatedBy: input.CreatedBy, CreateIdempotencyKey: input.IdempotencyKey, Version: 1,
	}
	created, err := s.Repository.Create(ctx, CreatePersistInput{Record: record, RequestID: strings.TrimSpace(input.RequestID)})
	if err != nil {
		return CreateResult{}, err
	}
	if created.Replayed {
		return CreateResult{Record: created.Record, Replayed: true}, nil
	}
	return CreateResult{Record: created.Record, Secret: secret}, nil
}

func (s *Service) List(ctx context.Context, tenantID string, options ListOptions) ([]Record, int64, error) {
	if s == nil || s.Repository == nil {
		return nil, 0, errors.New("inference access-key repository is not configured")
	}
	if options.Limit == 0 {
		options.Limit = 20
	}
	if options.Limit < 1 || options.Limit > 100 || options.Offset < 0 {
		return nil, 0, fmt.Errorf("%w: invalid access-key page", ErrInvalidInput)
	}
	return s.Repository.List(ctx, strings.TrimSpace(tenantID), options)
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (Record, error) {
	if s == nil || s.Repository == nil {
		return Record{}, errors.New("inference access-key repository is not configured")
	}
	return s.Repository.Get(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(id))
}

func (s *Service) Revoke(ctx context.Context, tenantID, id, actor, requestID string) (Record, error) {
	if s == nil || s.Repository == nil {
		return Record{}, errors.New("inference access-key repository is not configured")
	}
	return s.Repository.Revoke(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(id), strings.TrimSpace(actor), strings.TrimSpace(requestID))
}

func (s *Service) Rotate(ctx context.Context, tenantID, id, idempotencyKey, actor, requestID string) (RotateResult, error) {
	if s == nil || s.Repository == nil {
		return RotateResult{}, errors.New("inference access-key repository is not configured")
	}
	secret, prefix, err := newSecret()
	if err != nil {
		return RotateResult{}, err
	}
	hash, err := hashSecret(secret)
	if err != nil {
		return RotateResult{}, err
	}
	result, err := s.Repository.Rotate(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(id), RotatePersistInput{KeyPrefix: prefix, SecretHash: hash, HashVersion: hashVersion, Idempotency: strings.TrimSpace(idempotencyKey), Actor: strings.TrimSpace(actor), RequestID: strings.TrimSpace(requestID)})
	if err != nil {
		return RotateResult{}, err
	}
	if result.Replayed {
		return RotateResult{Record: result.Record, Replayed: true}, nil
	}
	return RotateResult{Record: result.Record, Secret: secret}, nil
}

func (s *Service) Check(ctx context.Context, apiKey, serviceID, model string, cost int64) (CheckResult, error) {
	if s == nil || s.Repository == nil {
		return CheckResult{}, errors.New("inference access-key repository is not configured")
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || !strings.HasPrefix(apiKey, secretPrefix) {
		return CheckResult{Allowed: false, DenialCode: "invalid_key"}, nil
	}
	prefix := keyPrefix(apiKey)
	record, err := s.Repository.GetByPrefix(ctx, prefix)
	if errors.Is(err, ErrNotFound) {
		return CheckResult{Allowed: false, DenialCode: "invalid_key"}, nil
	}
	if err != nil {
		return CheckResult{}, err
	}
	if !verifySecret(apiKey, record.SecretHash) {
		return CheckResult{Allowed: false, DenialCode: "invalid_key"}, nil
	}
	now := s.now()
	if record.RevokedAt != nil {
		return CheckResult{Record: record, Allowed: false, DenialCode: "revoked", PolicyVersion: record.Version}, nil
	}
	if record.ExpiresAt != nil && !record.ExpiresAt.After(now) {
		return CheckResult{Record: record, Allowed: false, DenialCode: "expired", PolicyVersion: record.Version}, nil
	}
	if !record.Allows(strings.TrimSpace(serviceID), strings.TrimSpace(model)) {
		return CheckResult{Record: record, Allowed: false, DenialCode: "scope_denied", PolicyVersion: record.Version}, nil
	}
	if cost < 1 {
		cost = 1
	}
	allowed, retryAfter, err := s.Repository.Consume(ctx, record, now, cost)
	if err != nil {
		return CheckResult{}, err
	}
	if !allowed {
		return CheckResult{Record: record, Allowed: false, DenialCode: "rate_limited", RetryAfter: retryAfter, PolicyVersion: record.Version}, nil
	}
	return CheckResult{Record: record, Allowed: true, PolicyVersion: record.Version}, nil
}

func (r Record) Allows(serviceID, model string) bool {
	if len(r.AllowedServiceIDs) > 0 && !contains(r.AllowedServiceIDs, serviceID) {
		return false
	}
	if len(r.AllowedModelNames) > 0 && !contains(r.AllowedModelNames, model) {
		return false
	}
	return len(r.AllowedServiceIDs) > 0 || len(r.AllowedModelNames) > 0
}

func validateScopeAndLimits(services, models []string, qps, rpm, rph int32, expires *time.Time, now time.Time) error {
	services = normalizeStrings(services)
	models = normalizeStrings(models)
	if len(services) == 0 && len(models) == 0 {
		return fmt.Errorf("%w: at least one service or model scope is required", ErrInvalidInput)
	}
	for _, serviceID := range services {
		if _, err := uuid.Parse(serviceID); err != nil {
			return fmt.Errorf("%w: allowed_service_ids must contain UUIDs", ErrInvalidInput)
		}
	}
	if qps < 0 || rpm < 0 || rph < 0 {
		return fmt.Errorf("%w: rate limits cannot be negative", ErrInvalidInput)
	}
	if expires != nil && !expires.After(now) {
		return fmt.Errorf("%w: expires_at must be in the future", ErrInvalidInput)
	}
	return nil
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func newSecret() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate inference access key: %w", err)
	}
	secret := secretPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return secret, keyPrefix(secret), nil
}

func keyPrefix(secret string) string {
	if len(secret) <= 16 {
		return secret
	}
	return secret[:16]
}

func hashSecret(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate access-key hash salt: %w", err)
	}
	derived := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return strings.Join([]string{hashVersion, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derived)}, "$"), nil
}

func verifySecret(secret, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != hashVersion {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(salt) != argonSaltLen {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(want) != argonKeyLen {
		return false
	}
	got := argon2.IDKey([]byte(secret), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}
