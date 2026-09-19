package service

import (
	"context"
	"errors"
	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type referenceReader struct {
	tenant string
	ids    []string
	active bool
	err    error
	calls  int
}

func (r *referenceReader) HasActiveModelVersionReferences(_ context.Context, tenant string, ids []string) (bool, error) {
	r.tenant, r.ids = tenant, ids
	r.calls++
	return r.active, r.err
}
func TestModelReferencesRequireTrustedTenantAndVersionUUIDs(t *testing.T) {
	r := &referenceReader{active: true}
	s := NewModelReferenceServer(r)
	id := "11111111-1111-4111-8111-111111111111"
	req := &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: []string{id}}
	if _, err := s.CheckModelVersionReferences(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing principal: %v", err)
	}
	for _, ids := range [][]string{nil, {"operation-id"}, make([]string, 257)} {
		_, err := s.CheckModelVersionReferences(WithTenantID(context.Background(), "tenant-a"), &inferencev1.CheckModelVersionReferencesRequest{ModelVersionIds: ids})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bad selectors: %v", err)
		}
	}
	if r.calls != 0 {
		t.Fatal("invalid request reached store")
	}
	got, err := s.CheckModelVersionReferences(WithTenantID(context.Background(), "tenant-a"), req)
	if err != nil || !got.GetHasActiveReferences() || r.tenant != "tenant-a" || len(r.ids) != 1 || r.ids[0] != id {
		t.Fatalf("response=%v err=%v reader=%+v", got, err, r)
	}
	r.err = errors.New("database unavailable")
	if _, err = s.CheckModelVersionReferences(WithTenantID(context.Background(), "tenant-a"), req); status.Code(err) != codes.Unavailable {
		t.Fatalf("store failure: %v", err)
	}
	if _, err = NewModelReferenceServer(nil).CheckModelVersionReferences(WithTenantID(context.Background(), "tenant-a"), req); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing store: %v", err)
	}
}
