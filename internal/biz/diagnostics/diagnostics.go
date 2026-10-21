// Package diagnostics defines the tenant-scoped runtime diagnostics port.
package diagnostics

import (
	"context"
	"errors"
	"time"
)

var ErrUnavailable = errors.New("runtime diagnostics unavailable")
var ErrNotFound = errors.New("runtime instance not found")
var ErrLimit = errors.New("runtime diagnostics limit exceeded")

type Scope struct {
	TenantID, ServiceID string
	Generation          int64
}
type Instance struct {
	Name, UID, Phase string
	Containers       []string
}
type LogsQuery struct {
	Instance, Container string
	Since, Until        time.Time
	TailLines           int64
	Follow              bool
}
type LogLine struct {
	Timestamp time.Time
	Message   string
}
type EventsQuery struct {
	Since, Until  time.Time
	Type          string
	Offset, Limit int
}
type Event struct {
	ID, Type, Reason, Message, Instance string
	Timestamp                           time.Time
	Count                               int64
}

// Reader must bind every result to persisted runtime identities, not a caller's selector.
type Reader interface {
	Instances(context.Context, Scope) ([]Instance, error)
	Logs(context.Context, Scope, LogsQuery, func(LogLine) error) (truncated bool, err error)
	Events(context.Context, Scope, EventsQuery) ([]Event, int64, error)
}
