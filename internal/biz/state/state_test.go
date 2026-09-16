package state

import "testing"

func TestTransitionStopRequiresPublicationWithdrawal(t *testing.T) {
	if err := ValidateTransition(OperationStop, RuntimeReady, PublicationPublished, QuotaConfirmed); err == nil {
		t.Fatal("expected stop to wait for publication withdrawal")
	}
	if err := ValidateTransition(OperationStop, RuntimeReady, PublicationWithdrawn, QuotaConfirmed); err != nil {
		t.Fatalf("withdrawn publication should allow stop: %v", err)
	}
}

func TestCanReleaseQuotaOnlyAfterRuntimeGone(t *testing.T) {
	if CanReleaseQuota(RuntimeReady, PublicationWithdrawn) {
		t.Fatal("ready runtime must retain quota")
	}
	if !CanReleaseQuota(RuntimeStopped, PublicationWithdrawn) {
		t.Fatal("stopped runtime can release quota")
	}
}
