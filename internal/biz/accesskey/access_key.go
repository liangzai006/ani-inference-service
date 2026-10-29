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
	ErrSecretUnavailable = errors.New("inference access key secret is unavailable")
)

type Record struct {
	TenantID             string
	ID                   string
	Name                 string
	KeyPrefix            string
	SecretHash           string
	HashVersion          string
	PolicyID             string
	SecretCiphertext     string `json:"-"`
	Secret               string `json:"-"`
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
	PolicyID          string
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
	Limit           int
	Offset          int
	ServedModelName string
}

type Repository interface {
	Create(ctx context.Context, input CreatePersistInput) (PersistResult, error)
	List(ctx context.Context, tenantID string, options ListOptions) ([]Record, int64, error)
	Get(ctx context.Context, tenantID, id string) (Record, error)
	GetByPrefix(ctx context.Context, prefix string) (Record, error)
	ArchiveSecret(ctx context.Context, record Record, ciphertext string) error
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
	KeyPrefix        string
	SecretHash       string
	HashVersion      string
	SecretCiphertext string
	Idempotency      string
	Actor            string
	RequestID        string
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

// AccessPolicy resolves trusted publication facts and atomically enforces the
// policy bound to a key. Production composition must supply it.
type AccessPolicy interface {
	Resolve(context.Context, string) (string, error)
	ConsumeAccess(context.Context, Record, string, string, time.Time, int64) (CheckResult, error)
}

type Service struct {
	Policy     AccessPolicy
	Repository Repository
	Now        func() time.Time
	Cipher     *SecretCipher
}

func NewService(repository Repository, cipher *SecretCipher) *Service {
	return &Service{Repository: repository, Cipher: cipher, Now: time.Now}
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
	policyID, err := uuid.Parse(strings.TrimSpace(input.PolicyID))
	if err != nil || policyID == uuid.Nil || policyID.String() != input.PolicyID || len(input.AllowedServiceIDs) != 0 || len(input.AllowedModelNames) != 0 || input.RequestsPerSecond != 0 || input.RequestsPerMinute != 0 || input.RequestsPerHour != 0 || (input.ExpiresAt != nil && !input.ExpiresAt.After(s.now())) {
		return CreateResult{}, fmt.Errorf("%w: a policy ID is required; scope and limits belong to the policy", ErrInvalidInput)
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
		KeyPrefix: prefix, SecretHash: hash, HashVersion: hashVersion, PolicyID: policyID.String(),
		ExpiresAt: input.ExpiresAt,
		CreatedBy: input.CreatedBy, CreateIdempotencyKey: input.IdempotencyKey, Version: 1,
	}
	record.SecretCiphertext, err = s.Cipher.seal(record, secret)
	if err != nil {
		return CreateResult{}, err
	}
	created, err := s.Repository.Create(ctx, CreatePersistInput{Record: record, RequestID: strings.TrimSpace(input.RequestID)})
	if err != nil {
		return CreateResult{}, err
	}
	if created.Replayed {
		if created.Record.Name != input.Name || created.Record.PolicyID != policyID.String() || !sameExpiry(created.Record.ExpiresAt, input.ExpiresAt) {
			return CreateResult{}, ErrIdempotencyReplay
		}
		return CreateResult{Record: created.Record, Replayed: true}, nil
	}
	return CreateResult{Record: created.Record, Secret: secret}, nil
}

func sameExpiry(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
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
	options.ServedModelName = strings.TrimSpace(options.ServedModelName)
	if strings.ContainsRune(options.ServedModelName, '\x00') {
		return nil, 0, fmt.Errorf("%w: invalid model name", ErrInvalidInput)
	}
	items, total, err := s.Repository.List(ctx, strings.TrimSpace(tenantID), options)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		if items[i].SecretCiphertext == "" {
			continue // A legacy hash has no recoverable value until used or rotated.
		}
		items[i].Secret, err = s.Cipher.open(items[i])
		if err != nil {
			return nil, 0, err
		}
	}
	return items, total, nil
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (Record, error) {
	if s == nil || s.Repository == nil {
		return Record{}, errors.New("inference access-key repository is not configured")
	}
	record, err := s.Repository.Get(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(id))
	if err != nil || record.SecretCiphertext == "" {
		return record, err
	}
	record.Secret, err = s.Cipher.open(record)
	if err != nil {
		return Record{}, err
	}
	return record, nil
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
	ciphertext, err := s.Cipher.seal(Record{TenantID: strings.TrimSpace(tenantID), ID: strings.TrimSpace(id), KeyPrefix: prefix, SecretHash: hash}, secret)
	if err != nil {
		return RotateResult{}, err
	}
	result, err := s.Repository.Rotate(ctx, strings.TrimSpace(tenantID), strings.TrimSpace(id), RotatePersistInput{KeyPrefix: prefix, SecretHash: hash, HashVersion: hashVersion, SecretCiphertext: ciphertext, Idempotency: strings.TrimSpace(idempotencyKey), Actor: strings.TrimSpace(actor), RequestID: strings.TrimSpace(requestID)})
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
	if s.Policy == nil {
		return CheckResult{}, errors.New("inference access policy is not configured")
	}
	resolved, err := s.Policy.Resolve(ctx, strings.TrimSpace(model))
	if err != nil {
		return CheckResult{}, err
	}
	if resolved == "" || (strings.TrimSpace(serviceID) != "" && strings.TrimSpace(serviceID) != resolved) {
		return CheckResult{Record: record, DenialCode: "scope_denied", PolicyVersion: record.Version}, nil
	}
	serviceID = resolved
	if cost < 1 {
		cost = 1
	}
	result, err := s.Policy.ConsumeAccess(ctx, record, serviceID, strings.TrimSpace(model), now, cost)
	if err != nil || !result.Allowed {
		return result, err
	}
	// Only an authenticated and authorized legacy credential is archived.
	// The repository fences this write against a concurrent rotate/revoke.
	if record.SecretCiphertext == "" {
		ciphertext, err := s.Cipher.seal(record, apiKey)
		if err != nil {
			return CheckResult{}, err
		}
		if err := s.Repository.ArchiveSecret(ctx, record, ciphertext); err != nil {
			return CheckResult{}, err
		}
	}
	return result, nil
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
