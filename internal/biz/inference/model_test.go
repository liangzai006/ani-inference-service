package inference

import "testing"

func TestDesiredStateValuesAreStable(t *testing.T) {
	if DesiredRunning != "running" || DesiredStopped != "stopped" || DesiredDeleted != "deleted" {
		t.Fatal("desired state wire values changed")
	}
}

func TestOperationPhaseValuesAreStable(t *testing.T) {
	if OperationPending != "pending" || OperationRunning != "running" || OperationSucceeded != "succeeded" || OperationFailed != "failed" {
		t.Fatal("operation phase wire values changed")
	}
}
