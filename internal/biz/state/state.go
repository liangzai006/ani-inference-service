// Package state contains lifecycle decisions shared by API admission and the
// Kubernetes worker. It has no transport or infrastructure dependencies.
package state

import "fmt"

type OperationKind string

const (
	OperationStop   OperationKind = "stop"
	OperationDelete OperationKind = "delete"
)

type RuntimePhase string

const (
	RuntimeReady   RuntimePhase = "ready"
	RuntimeStopped RuntimePhase = "stopped"
)

type PublicationPhase string

const (
	PublicationPublished PublicationPhase = "published"
	PublicationWithdrawn PublicationPhase = "withdrawn"
)

type QuotaState string

const QuotaConfirmed QuotaState = "confirmed"

func ValidateTransition(kind OperationKind, runtime RuntimePhase, publication PublicationPhase, quota QuotaState) error {
	if (kind == OperationStop || kind == OperationDelete) && publication != PublicationWithdrawn {
		return fmt.Errorf("%s requires withdrawn publication", kind)
	}
	_ = runtime
	_ = quota
	return nil
}

func CanReleaseQuota(runtime RuntimePhase, publication PublicationPhase) bool {
	return runtime == RuntimeStopped && publication == PublicationWithdrawn
}
