package accesskey

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSecretCanBeReadAfterServiceRestartAndRotation(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepository()
	svc := NewService(repo, testCipher(t))
	created, err := svc.Create(ctx, CreateInput{TenantID: "tenant-a", Name: "app", AllowedModelNames: []string{"model"}, CreatedBy: "user", IdempotencyKey: "create"})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := repo.Get(ctx, "tenant-a", created.Record.ID)
	if stored.Secret != "" || stored.SecretCiphertext == "" || strings.Contains(stored.SecretCiphertext, created.Secret) {
		t.Fatal("repository must persist ciphertext, not plaintext")
	}
	svc = NewService(repo, testCipher(t))
	for i := 0; i < 2; i++ {
		detail, err := svc.Get(ctx, "tenant-a", created.Record.ID)
		if err != nil || detail.Secret != created.Secret {
			t.Fatalf("get after restart: err=%v", err)
		}
		items, total, err := svc.List(ctx, "tenant-a", ListOptions{})
		if err != nil || total != 1 || len(items) != 1 || items[0].Secret != created.Secret {
			t.Fatalf("list after restart: err=%v", err)
		}
	}
	if _, err := svc.Get(ctx, "tenant-b", created.Record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get: %v", err)
	}
	if items, total, err := svc.List(ctx, "tenant-b", ListOptions{}); err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("cross-tenant list: %v", err)
	}
	rotated, err := svc.Rotate(ctx, "tenant-a", created.Record.ID, "rotate", "user", "request")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := svc.Get(ctx, "tenant-a", created.Record.ID)
	if err != nil || detail.Secret != rotated.Secret || detail.Secret == created.Secret {
		t.Fatalf("get must return only the current credential: %v", err)
	}
	replay, err := svc.Rotate(ctx, "tenant-a", created.Record.ID, "rotate", "user", "replay")
	if err != nil || !replay.Replayed || replay.Secret != "" || replay.Record.Secret != "" {
		t.Fatalf("rotation replay must not leak plaintext: %v", err)
	}
	if old, err := svc.Check(ctx, created.Secret, "", "model", 1); err != nil || old.Allowed {
		t.Fatalf("old credential must fail: %v", err)
	}
}

func TestLegacySecretArchivedOnlyAfterAllowedCheck(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepository()
	svc := NewService(repo, testCipher(t))
	created, err := svc.Create(ctx, CreateInput{TenantID: "tenant-a", Name: "legacy", AllowedModelNames: []string{"model"}, CreatedBy: "user"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := created.Record
	legacy.SecretCiphertext = "" // The pre-migration durable format.
	repo.byID[legacy.ID], repo.byPrefix[legacy.KeyPrefix] = legacy, legacy
	detail, err := svc.Get(ctx, legacy.TenantID, legacy.ID)
	if err != nil || detail.Secret != "" {
		t.Fatalf("a hash cannot be reversed: %v", err)
	}
	for _, denied := range []struct{ secret, model string }{
		{legacy.KeyPrefix + strings.Repeat("0", 31), "model"},
		{created.Secret, "foreign-model"},
	} {
		result, err := svc.Check(ctx, denied.secret, "", denied.model, 1)
		if err != nil || result.Allowed || repo.byID[legacy.ID].SecretCiphertext != "" {
			t.Fatalf("denied check must not archive: %v", err)
		}
	}
	repo.limited = true
	if result, err := svc.Check(ctx, created.Secret, "", "model", 1); err != nil || result.Allowed || repo.byID[legacy.ID].SecretCiphertext != "" {
		t.Fatalf("rate-limited check must not archive: %v", err)
	}
	repo.limited = false
	if result, err := svc.Check(ctx, created.Secret, "", "model", 1); err != nil || !result.Allowed {
		t.Fatalf("valid legacy credential: %v", err)
	}
	detail, err = svc.Get(ctx, legacy.TenantID, legacy.ID)
	if err != nil || detail.Secret != created.Secret || detail.Version != legacy.Version || detail.SecretHash != legacy.SecretHash || detail.KeyPrefix != legacy.KeyPrefix {
		t.Fatalf("archive must preserve the original credential and version: %v", err)
	}
}

func TestSecretCipherFailsClosedOnTamperingAndWrongKey(t *testing.T) {
	cipher := testCipher(t)
	record := Record{TenantID: "tenant-a", ID: "key-a", KeyPrefix: "iak_000000000000", SecretHash: "hash"}
	secret := record.KeyPrefix + strings.Repeat("0", 31)
	var err error
	record.SecretCiphertext, err = cipher.seal(record, secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Record){
		func(r *Record) { r.TenantID = "tenant-b" },
		func(r *Record) { r.ID = "key-b" },
		func(r *Record) { r.KeyPrefix = "iak_111111111111" },
		func(r *Record) { r.SecretHash = "other-hash" },
		func(r *Record) { r.SecretCiphertext = "v1:invalid" },
	} {
		changed := record
		change(&changed)
		if plain, err := cipher.open(changed); !errors.Is(err, ErrSecretUnavailable) || plain != "" {
			t.Fatal("tampered record must not disclose a credential")
		}
	}
	other, _ := NewSecretCipher([]byte(strings.Repeat("1", 32)))
	if plain, err := other.open(record); !errors.Is(err, ErrSecretUnavailable) || plain != "" {
		t.Fatal("wrong master key must fail closed")
	}
	repo := newFakeRepository()
	repo.byID[record.ID] = record
	if items, _, err := NewService(repo, other).List(context.Background(), record.TenantID, ListOptions{}); !errors.Is(err, ErrSecretUnavailable) || items != nil {
		t.Fatal("a decryption failure must fail the whole page")
	}
}
