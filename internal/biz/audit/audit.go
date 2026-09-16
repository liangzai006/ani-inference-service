package audit

import "context"

type Event struct {
	TenantID, EventID, ServiceID, OperationID string
	Generation                                int64
	EventType, Actor, RequestID               string
	BeforeState, AfterState, Payload          []byte
}

type Sink interface {
	Append(context.Context, Event) error
}
