package server

import "sync/atomic"

// Readiness records only the local process lifecycle. A service with external
// dependencies must add domain-specific readiness checks at composition time.
type Readiness struct {
	ready atomic.Bool
}

func NewReadiness() *Readiness {
	return &Readiness{}
}

func (r *Readiness) Set(ready bool) {
	r.ready.Store(ready)
}

func (r *Readiness) Ready() bool {
	return r.ready.Load()
}
