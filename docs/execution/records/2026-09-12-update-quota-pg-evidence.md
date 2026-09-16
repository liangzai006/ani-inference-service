# 2026-09-12 PostgreSQL update quota evidence

The update acceptance integration test now verifies the persisted hand-off
needed by the operation runner:

- generation 2 is created with a pending reservation;
- the previous generation's confirmed reservation is discoverable through the
  tenant-scoped `GetPreviousQuotaReservation` query;
- stale expected generation remains rejected.

The test runs against the authorized local `recycling-postgres` database. It
does not call an external quota provider, so provider accounting and release
success remain `not_verified`.
