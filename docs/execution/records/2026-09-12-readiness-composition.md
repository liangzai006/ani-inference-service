# 2026-09-12 Readiness composition

The composition root now distinguishes the dependency-free layout process from
the Kubernetes background composition. The former remains process-ready for
health/lifecycle tests while exposing no business use cases. A composition that
starts durable Kubernetes work is not marked business-ready until external
quota/publication/model providers are wired; those providers are currently
absent, so `/readyz` stays `503 NOT_READY` in that mode rather than claiming a
complete serving path.

This is a composition gate only. It does not replace dependency-specific
readiness checks for PostgreSQL, controller-runtime cache, engine health or the
data plane; those remain required before production cutover.
