package inference

import (
	"strings"
	"testing"
)

func TestValidateOperationStepTransition(t *testing.T) {
	tests := []struct {
		name             string
		kind             string
		fromPhase        OperationPhase
		fromStep, toStep string
		toPhase          OperationPhase
		wantErr          bool
	}{
		{name: "claim pending", kind: "create", fromPhase: OperationPending, fromStep: "admission", toPhase: OperationRunning, toStep: "admission"},
		{name: "advance", kind: "create", fromPhase: OperationRunning, fromStep: "admission", toPhase: OperationRunning, toStep: "reserve_quota"},
		{name: "terminal success", kind: "stop", fromPhase: OperationRunning, fromStep: "release_quota", toPhase: OperationSucceeded, toStep: "complete"},
		{name: "failure", kind: "update", fromPhase: OperationRunning, fromStep: "apply_runtime", toPhase: OperationFailed, toStep: "complete"},
		{name: "skip runtime", kind: "create", fromPhase: OperationRunning, fromStep: "reserve_quota", toPhase: OperationRunning, toStep: "publish", wantErr: true},
		{name: "bad terminal step", kind: "create", fromPhase: OperationRunning, fromStep: "publish", toPhase: OperationSucceeded, toStep: "publish", wantErr: true},
		{name: "restart after withdrawal", kind: "restart", fromPhase: OperationRunning, fromStep: "withdraw_publication", toPhase: OperationRunning, toStep: "apply_runtime", wantErr: true},
		{name: "stop cannot apply runtime", kind: "stop", fromPhase: OperationRunning, fromStep: "withdraw_publication", toPhase: OperationRunning, toStep: "apply_runtime", wantErr: true},
		{name: "stop cannot release before absence", kind: "stop", fromPhase: OperationRunning, fromStep: "withdraw_publication", toPhase: OperationRunning, toStep: "release_quota", wantErr: true},
		{name: "delete cannot release before absence", kind: "delete", fromPhase: OperationRunning, fromStep: "withdraw_publication", toPhase: OperationRunning, toStep: "release_quota", wantErr: true},
		{name: "update cannot reserve before removal", kind: "update", fromPhase: OperationRunning, fromStep: "withdraw_publication", toPhase: OperationRunning, toStep: "reserve_quota", wantErr: true},
		{name: "unknown kind claim", kind: "other", fromPhase: OperationPending, fromStep: "admission", toPhase: OperationRunning, toStep: "admission", wantErr: true},
		{name: "unknown kind failure", kind: "other", fromPhase: OperationRunning, fromStep: "admission", toPhase: OperationFailed, toStep: "complete", wantErr: true},
		{name: "unknown kind success", kind: "other", fromPhase: OperationRunning, fromStep: "publish", toPhase: OperationSucceeded, toStep: "complete", wantErr: true},
		{name: "unknown step claim", kind: "create", fromPhase: OperationPending, fromStep: "other", toPhase: OperationRunning, toStep: "other", wantErr: true},
		{name: "kind step mismatch", kind: "stop", fromPhase: OperationRunning, fromStep: "apply_runtime", toPhase: OperationRunning, toStep: "observe_runtime", wantErr: true},
		{name: "kind success mismatch", kind: "delete", fromPhase: OperationRunning, fromStep: "publish", toPhase: OperationSucceeded, toStep: "complete", wantErr: true},
		{name: "pending must claim before advance", kind: "create", fromPhase: OperationPending, fromStep: "admission", toPhase: OperationRunning, toStep: "reserve_quota", wantErr: true},
		{name: "running to pending requires retry", kind: "create", fromPhase: OperationRunning, fromStep: "reserve_quota", toPhase: OperationPending, toStep: "reserve_quota", wantErr: true},
		{name: "terminal cannot restart", kind: "create", fromPhase: OperationSucceeded, fromStep: "complete", toPhase: OperationRunning, toStep: "admission", wantErr: true},
		{name: "restart cannot skip absence", kind: "restart", fromPhase: OperationRunning, fromStep: "delete_runtime", toPhase: OperationRunning, toStep: "reserve_quota", wantErr: true},
		{name: "restart must reserve after absence", kind: "restart", fromPhase: OperationRunning, fromStep: "observe_absence", toPhase: OperationRunning, toStep: "apply_cr", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOperationStepTransition(tt.kind, tt.fromPhase, tt.fromStep, tt.toPhase, tt.toStep)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestOperationPathsRequireRemovalAndAbsenceBeforeQuotaChanges(t *testing.T) {
	// These explicit acceptance paths describe the required lifecycle order;
	// executing an edge still requires the provider's confirmed result.
	paths := map[string]string{
		"create":  "admission reserve_quota apply_cr materialize_model apply_runtime observe_runtime publish complete",
		"start":   "reserve_quota apply_cr materialize_model apply_runtime observe_runtime publish complete",
		"update":  "withdraw_publication delete_runtime observe_absence release_previous_quota reserve_quota apply_cr materialize_model apply_runtime observe_runtime publish complete",
		"restart": "withdraw_publication delete_runtime observe_absence release_previous_quota reserve_quota apply_cr materialize_model apply_runtime observe_runtime publish complete",
		"stop":    "withdraw_publication delete_runtime observe_absence release_quota complete",
		"delete":  "withdraw_publication delete_runtime observe_absence delete_cr release_quota complete",
	}
	for kind, path := range paths {
		t.Run(kind, func(t *testing.T) {
			steps := strings.Fields(path)
			for i, step := range steps[:len(steps)-1] {
				if err := ValidateOperationStepTransition(kind, OperationPending, step, OperationRunning, step); err != nil {
					t.Fatalf("claim %s: %v", step, err)
				}
				nextPhase := OperationRunning
				if steps[i+1] == "complete" {
					nextPhase = OperationSucceeded
				}
				if err := ValidateOperationStepTransition(kind, OperationRunning, step, nextPhase, steps[i+1]); err != nil {
					t.Fatalf("advance %s -> %s: %v", step, steps[i+1], err)
				}
			}
		})
	}
}
