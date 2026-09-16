package inference

import "time"

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
	DesiredDeleted DesiredState = "deleted"
)

type OperationPhase string

const (
	OperationPending   OperationPhase = "pending"
	OperationRunning   OperationPhase = "running"
	OperationSucceeded OperationPhase = "succeeded"
	OperationFailed    OperationPhase = "failed"
)

type Service struct {
	TenantID, ID, Name string
	DesiredState       DesiredState
	DesiredGeneration  int64
	CurrentOperationID string
	DeletedAt          *time.Time
}

type Operation struct {
	TenantID, ID, ServiceID, Kind, Step string
	Phase                               OperationPhase
	TargetGeneration                    int64
	RequestHash                         string
}
