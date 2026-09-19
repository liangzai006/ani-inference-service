package inference

import "fmt"

// OperationStep names mirror the persisted CHECK constraint in
// inference_operations. Keeping them in the domain package prevents callers
// from scattering string literals while the database remains the durable
// source of the current state.
type OperationStep string

const (
	StepAdmission            OperationStep = "admission"
	StepReserveQuota         OperationStep = "reserve_quota"
	StepApplyCR              OperationStep = "apply_cr"
	StepMaterializeModel     OperationStep = "materialize_model"
	StepApplyRuntime         OperationStep = "apply_runtime"
	StepObserveRuntime       OperationStep = "observe_runtime"
	StepWithdrawPublication  OperationStep = "withdraw_publication"
	StepDeleteRuntime        OperationStep = "delete_runtime"
	StepObserveAbsence       OperationStep = "observe_absence"
	StepDeleteCR             OperationStep = "delete_cr"
	StepPublish              OperationStep = "publish"
	StepReleaseQuota         OperationStep = "release_quota"
	StepReleasePreviousQuota OperationStep = "release_previous_quota"
	StepComplete             OperationStep = "complete"
)

var operationPaths = map[string][]OperationStep{
	"create":  {StepAdmission, StepReserveQuota, StepApplyCR, StepMaterializeModel, StepApplyRuntime, StepObserveRuntime, StepPublish, StepComplete},
	"start":   {StepReserveQuota, StepApplyCR, StepMaterializeModel, StepApplyRuntime, StepObserveRuntime, StepPublish, StepComplete},
	"update":  {StepWithdrawPublication, StepDeleteRuntime, StepObserveAbsence, StepReleasePreviousQuota, StepReserveQuota, StepApplyCR, StepMaterializeModel, StepApplyRuntime, StepObserveRuntime, StepPublish, StepComplete},
	"restart": {StepWithdrawPublication, StepDeleteRuntime, StepObserveAbsence, StepReleasePreviousQuota, StepReserveQuota, StepApplyCR, StepMaterializeModel, StepApplyRuntime, StepObserveRuntime, StepPublish, StepComplete},
	"stop":    {StepWithdrawPublication, StepDeleteRuntime, StepObserveAbsence, StepReleaseQuota, StepComplete},
	"delete":  {StepWithdrawPublication, StepDeleteRuntime, StepObserveAbsence, StepDeleteCR, StepReleaseQuota, StepComplete},
}

func operationStepIndex(path []OperationStep, step string) int {
	for i, candidate := range path {
		if string(candidate) == step {
			return i
		}
	}
	return -1
}

// ValidateOperationStepTransition protects the local operation state machine
// from skipping a required persisted step. Providers (quota, publication and
// runtime) remain external ports: a valid transition only records that a
// provider result was handled; it does not claim that result was observed.
//
// An operation is claimed by changing pending -> running while retaining its
// step. Active work then advances one edge at a time. Running -> pending is
// intentionally invalid here and is available only through the repository's
// explicit retry operation, which records retry_at and attempt.
func ValidateOperationStepTransition(kind string, fromPhase OperationPhase, fromStep string, toPhase OperationPhase, toStep string) error {
	path, ok := operationPaths[kind]
	if !ok {
		return fmt.Errorf("unknown operation kind %q", kind)
	}
	if fromPhase != OperationPending && fromPhase != OperationRunning {
		return fmt.Errorf("operation phase %q is not active", fromPhase)
	}
	from := operationStepIndex(path, fromStep)
	if from < 0 || from >= len(path)-1 {
		return fmt.Errorf("step %q is not an active step for %s", fromStep, kind)
	}

	// Failures may be persisted from any active step, but always use the
	// terminal marker so a failed operation cannot be resumed as if successful.
	if toPhase == OperationFailed {
		if toStep != string(StepComplete) {
			return fmt.Errorf("failed operation must use %q step", StepComplete)
		}
		return nil
	}
	if toPhase == OperationSucceeded {
		if fromPhase != OperationRunning || toStep != string(StepComplete) || from != len(path)-2 {
			return fmt.Errorf("cannot succeed %s from %s/%s", kind, fromPhase, fromStep)
		}
		return nil
	}
	if toPhase == OperationPending {
		return fmt.Errorf("running operation cannot return to pending; use retry")
	}
	if toPhase != OperationRunning {
		return fmt.Errorf("unsupported target phase %q", toPhase)
	}
	to := operationStepIndex(path, toStep)
	if to < 0 || to >= len(path)-1 {
		return fmt.Errorf("step %q is not an active step for %s", toStep, kind)
	}
	if fromPhase == OperationPending {
		if from != to {
			return fmt.Errorf("pending operation must be claimed at step %q before advancing", fromStep)
		}
		return nil
	}
	if to != from+1 {
		return fmt.Errorf("operation %s must advance from %q to %q", kind, fromStep, path[from+1])
	}
	return nil
}
