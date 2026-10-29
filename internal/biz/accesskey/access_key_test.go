package accesskey

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRepository struct {
	byID     map[string]Record
	byPrefix map[string]Record
	limited  bool
}

const testPolicyID = "11111111-1111-4111-8111-111111111111"

type fakeAccessPolicy struct{ repo *fakeRepository }

func (f fakeAccessPolicy) Resolve(_ context.Context, model string) (string, error) {
	if model == "" {
		return "", nil
	}
	return "service-a", nil
}
func (f fakeAccessPolicy) ConsumeAccess(ctx context.Context, record Record, _ string, model string, now time.Time, cost int64) (CheckResult, error) {
	if model != "model-a" && model != "model" {
		return CheckResult{Record: record, DenialCode: "scope_denied"}, nil
	}
	allowed, retry, err := f.repo.Consume(ctx, record, now, cost)
	result := CheckResult{Record: record, Allowed: allowed, RetryAfter: retry}
	if !allowed {
		result.DenialCode = "rate_limited"
	}
	return result, err
}
func fakeService(t *testing.T, repo *fakeRepository) *Service {
	svc := NewService(repo, testCipher(t))
	svc.Policy = fakeAccessPolicy{repo: repo}
	return svc
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{byID: map[string]Record{}, byPrefix: map[string]Record{}}
}

func (f *fakeRepository) Create(_ context.Context, input CreatePersistInput) (PersistResult, error) {
	if input.Record.CreateIdempotencyKey != "" {
		for _, existing := range f.byID {
			if existing.TenantID == input.Record.TenantID && existing.CreateIdempotencyKey == input.Record.CreateIdempotencyKey {
				return PersistResult{Record: existing, Replayed: true}, nil
			}
		}
	}
	input.Record.CreatedAt = time.Now().UTC()
	f.byID[input.Record.ID] = input.Record
	f.byPrefix[input.Record.KeyPrefix] = input.Record
	return PersistResult{Record: input.Record}, nil
}

func (f *fakeRepository) List(_ context.Context, tenantID string, options ListOptions) ([]Record, int64, error) {
	items := make([]Record, 0)
	for _, item := range f.byID {
		if item.TenantID == tenantID {
			items = append(items, item)
		}
	}
	return items, int64(len(items)), nil
}

func (f *fakeRepository) Get(_ context.Context, tenantID, id string) (Record, error) {
	item, ok := f.byID[id]
	if !ok || item.TenantID != tenantID {
		return Record{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeRepository) GetByPrefix(_ context.Context, prefix string) (Record, error) {
	item, ok := f.byPrefix[prefix]
	if !ok {
		return Record{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeRepository) Revoke(_ context.Context, tenantID, id, _, _ string) (Record, error) {
	item, err := f.Get(context.Background(), tenantID, id)
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	item.RevokedAt = &now
	item.Version++
	f.byID[id], f.byPrefix[item.KeyPrefix] = item, item
	return item, nil
}

func (f *fakeRepository) Rotate(_ context.Context, tenantID, id string, input RotatePersistInput) (PersistResult, error) {
	item, err := f.Get(context.Background(), tenantID, id)
	if err != nil {
		return PersistResult{}, err
	}
	if input.Idempotency != "" && item.RotationIdempotency == input.Idempotency {
		return PersistResult{Record: item, Replayed: true}, nil
	}
	delete(f.byPrefix, item.KeyPrefix)
	item.KeyPrefix, item.SecretHash, item.HashVersion = input.KeyPrefix, input.SecretHash, input.HashVersion
	item.SecretCiphertext = input.SecretCiphertext
	item.RotationIdempotency, item.Version = input.Idempotency, item.Version+1
	f.byID[id], f.byPrefix[item.KeyPrefix] = item, item
	return PersistResult{Record: item}, nil
}

func (f *fakeRepository) Consume(_ context.Context, record Record, _ time.Time, _ int64) (bool, time.Duration, error) {
	if f.limited {
		return false, time.Second, nil
	}
	item := f.byID[record.ID]
	now := time.Now().UTC()
	item.LastUsedAt = &now
	f.byID[record.ID], f.byPrefix[item.KeyPrefix] = item, item
	return true, 0, nil
}

func (f *fakeRepository) ArchiveSecret(_ context.Context, record Record, ciphertext string) error {
	item, ok := f.byID[record.ID]
	if ok && item.TenantID == record.TenantID && item.Version == record.Version && item.SecretHash == record.SecretHash && item.SecretCiphertext == "" && item.RevokedAt == nil {
		item.SecretCiphertext = ciphertext
		f.byID[item.ID], f.byPrefix[item.KeyPrefix] = item, item
	}
	return nil
}

func testCipher(t *testing.T) *SecretCipher {
	t.Helper()
	cipher, err := NewSecretCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestServiceCreateCheckAndRevoke(t *testing.T) {
	repo := newFakeRepository()
	svc := fakeService(t, repo)
	svc.Now = func() time.Time { return time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC) }
	created, err := svc.Create(context.Background(), CreateInput{
		TenantID: "tenant-1", Name: "client", PolicyID: testPolicyID,
		IdempotencyKey: "create-1", CreatedBy: "governance:user:7",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Secret == "" || created.Record.SecretHash == created.Secret || created.Record.KeyPrefix == created.Secret {
		t.Fatalf("secret handling leaked or omitted hash: %+v", created)
	}
	allowed, err := svc.Check(context.Background(), created.Secret, "", "model-a", 1)
	if err != nil || !allowed.Allowed || allowed.Record.TenantID != "tenant-1" {
		t.Fatalf("Check(valid) = %+v, %v", allowed, err)
	}
	denied, err := svc.Check(context.Background(), created.Secret, "", "model-b", 1)
	if err != nil || denied.Allowed || denied.DenialCode != "scope_denied" {
		t.Fatalf("Check(scope) = %+v, %v", denied, err)
	}
	bad, err := svc.Check(context.Background(), "iak_invalid", "", "model-a", 1)
	if err != nil || bad.Allowed || bad.DenialCode != "invalid_key" {
		t.Fatalf("Check(invalid) = %+v, %v", bad, err)
	}
	if _, err := svc.Revoke(context.Background(), "tenant-1", created.Record.ID, "actor", "revoke-1"); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	revoked, err := svc.Check(context.Background(), created.Secret, "", "model-a", 1)
	if err != nil || revoked.Allowed || revoked.DenialCode != "revoked" {
		t.Fatalf("Check(revoked) = %+v, %v", revoked, err)
	}
}

func TestServiceCreateIdempotencyDoesNotReturnSecretAgain(t *testing.T) {
	repo := newFakeRepository()
	svc := fakeService(t, repo)
	input := CreateInput{TenantID: "tenant-1", Name: "client", PolicyID: testPolicyID, CreatedBy: "actor", IdempotencyKey: "same"}
	first, err := svc.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.Secret != "" || second.Record.ID != first.Record.ID {
		t.Fatalf("replay = %+v; want metadata only", second)
	}
	input.PolicyID = "22222222-2222-4222-8222-222222222222"
	if _, err := svc.Create(context.Background(), input); !errors.Is(err, ErrIdempotencyReplay) {
		t.Fatalf("changing policy on replay must be rejected: %v", err)
	}
}

func TestServiceCheckRateLimit(t *testing.T) {
	repo := newFakeRepository()
	svc := fakeService(t, repo)
	created, err := svc.Create(context.Background(), CreateInput{TenantID: "tenant-1", Name: "client", PolicyID: testPolicyID, CreatedBy: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	repo.limited = true
	result, err := svc.Check(context.Background(), created.Secret, "", "model-a", 1)
	if err != nil || result.Allowed || result.DenialCode != "rate_limited" || result.RetryAfter <= 0 {
		t.Fatalf("Check(rate) = %+v, %v", result, err)
	}
}

func TestServiceRejectsUnscopedKey(t *testing.T) {
	_, err := NewService(newFakeRepository(), testCipher(t)).Create(context.Background(), CreateInput{TenantID: "tenant", Name: "client", CreatedBy: "actor"})
	if err == nil || !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create(unscoped) error = %v", err)
	}
}
