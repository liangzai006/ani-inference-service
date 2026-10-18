package gpu

import (
	"errors"
	"testing"
)

func TestValidateRefundUsesFourGovernanceFields(t *testing.T) {
	original := RefundContext{ResourceID: "10000000-0000-4000-8000-000000000014", OriginalCreateOperationID: "10000000-0000-4000-8000-000000000015", DeleteOperationID: "10000000-0000-4000-8000-000000000016", OwnerService: "ani-inference"}
	n := ReleaseNotification{ReleaseEventID: "10000000-0000-4000-8000-000000000017", CreateOperationID: original.OriginalCreateOperationID, ResourceID: original.ResourceID, Reason: ResourceReleased}
	if err := ValidateRefund(n, original); err != nil {
		t.Fatal(err)
	}
	n.CreateOperationID = original.DeleteOperationID
	if !errors.Is(ValidateRefund(n, original), ErrInvalidRefund) {
		t.Fatal("mismatched operation accepted")
	}
}

func TestValidateRefundRejectsMismatchedIdentityAndOwner(t *testing.T) {
	base, original := validRelease()
	for _, test := range []struct {
		name  string
		alter func(*ReleaseNotification, *RefundContext)
	}{
		{"wrong-create", func(n *ReleaseNotification, o *RefundContext) { n.CreateOperationID = o.DeleteOperationID }},
		{"wrong-resource", func(n *ReleaseNotification, o *RefundContext) { n.ResourceID = o.TenantID }},
		{"wrong-owner", func(_ *ReleaseNotification, o *RefundContext) { o.OwnerService = "ani-model" }},
		{"no-delete", func(_ *ReleaseNotification, o *RefundContext) { o.DeleteOperationID = "" }},
		{"ordinary-exit", func(n *ReleaseNotification, _ *RefundContext) { n.Reason = "EXITED" }},
		{"bad-event-id", func(n *ReleaseNotification, _ *RefundContext) { n.ReleaseEventID = "event" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, o := base, original
			test.alter(&n, &o)
			if !errors.Is(ValidateRefund(n, o), ErrInvalidRefund) {
				t.Fatal("invalid release identity accepted")
			}
		})
	}
}

func validRelease() (ReleaseNotification, RefundContext) {
	original := RefundContext{TenantID: "10000000-0000-4000-8000-000000000013", ResourceID: "10000000-0000-4000-8000-000000000014", OriginalCreateOperationID: "10000000-0000-4000-8000-000000000015", DeleteOperationID: "10000000-0000-4000-8000-000000000016", OwnerService: "ani-inference"}
	return ReleaseNotification{ReleaseEventID: "10000000-0000-4000-8000-000000000017", CreateOperationID: original.OriginalCreateOperationID, ResourceID: original.ResourceID, Reason: ResourceReleased}, original
}

func TestValidateRefundAcceptsAbortedCleanup(t *testing.T) {
	n, original := validRelease()
	n.Reason = AbortedCleaned
	if err := ValidateRefund(n, original); err != nil {
		t.Fatal(err)
	}
}
