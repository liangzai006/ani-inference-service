# Task 2 report

- Commit: `c4baf01` (`feat: publish inference routes through Higress API`)
- Files changed:
  - `internal/data/kubernetes/http_route_publisher.go`
  - `internal/data/kubernetes/higress_rate_limit.go`
  - `internal/data/kubernetes/http_route_publisher_test.go`
  - `internal/data/kubernetes/higress_console_client.go`
  - `internal/data/kubernetes/higress_console_client_test.go`

Implementation uses native Higress `/v1/service-sources` and `/v1/routes` PUT/GET/DELETE calls, deterministic route and service-source names, DNS service-source payloads, native model-header route payloads, publication labels, rollback of a service source when route creation fails, and native route-list ownership checks for rate-limit cleanup. The existing Gateway API path remains available when the Console client is unset for compatibility until Task 3 wiring supplies it.

Validation:

- `go test ./internal/data/kubernetes -run 'NativeRoute|HTTPRoutePublisher|RateLimit|HigressConsoleClient' -count=1` — PASS
- `go test ./internal/data/kubernetes -count=1` — PASS
- `go test ./...` — PASS
- `make verify` — unavailable: repository has no `verify` target (`make: *** No rule to make target 'verify'. Stop.`)

Concern: the native lifecycle depends on Task 3 wiring `HTTPRoutePublisher.Console`; without that field being populated, compatibility behavior still uses Kubernetes HTTPRoutes.

## Follow-up schema correction

- Fix commit: `ced6366ab68e546e0c2b462c568a8fc8e23987f6` (`fix: use Higress service source schema`)
- Verified against Higress Console 2.2.4 source: ServiceSource now sends `type: dns`, `domain`, integer `port`, and `protocol: http`; the unsupported `address`, `DNS`, and `HTTP` forms are removed.
- Route payload remains `path.matchType: PRE`, `headers[].key: x-higress-llm-model` with `matchType: EQUAL`, and `services[].name`/`port`/`weight`.
- `ConfirmPublished` uses `GET /v1/routes/{name}` and reports true only when the successful response contains route data; typed 404 remains pending.

Additional verification:

- `go test ./internal/data/kubernetes -run 'NativeRoute|HTTPRoutePublisher|RateLimit|HigressConsoleClient' -count=1` — PASS
- `go test ./internal/data/kubernetes -count=1` — PASS
- `go test ./...` — PASS
