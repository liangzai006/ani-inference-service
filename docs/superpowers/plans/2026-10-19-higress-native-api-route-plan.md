# Higress Native API Route Management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ANI's Kubernetes `HTTPRoute` model publication with Higress Console native route and service-source API calls while preserving model routing, rate limiting, and lifecycle convergence.

**Architecture:** Keep `publication.Port` and `publication.EndpointResolver` unchanged. Replace the infrastructure implementation behind them with a Higress Console API adapter using Basic Auth, deterministic native route/service-source names, and a Kubernetes sink Service for the model-router bootstrap route. Model routes point directly to the KServe predictor Service DNS; the Service Source remains managed metadata/bootstrap state. Keep the existing Kubernetes WASM rate-limit patcher, but make its ownership lookup query Higress native routes instead of `HTTPRoute` objects.

**Tech Stack:** Go standard `net/http`, Kubernetes controller-runtime client, Higress Console 2.2.4 REST API, Helm, Go tests, `helm template`.

## Global Constraints

- Higress handles only AI inference paths such as `/v1/completions`; Governance `/api/v1/...` routes remain outside Higress.
- Do not maintain native routes and model `HTTPRoute` objects simultaneously.
- Do not place Higress Console credentials in source, Helm values, logs, or generated manifests.
- Preserve the externally supplied complete engine command; this change does not alter model runtime commands.
- Preserve existing rate-limit semantics: 10 QPS per served model and ownership cleanup after withdrawal.
- Do not modify unrelated pre-existing worktree changes.

---

### Task 1: Add the Higress Console API client

**Files:**
- Create: `internal/data/kubernetes/higress_console_client.go`
- Create: `internal/data/kubernetes/higress_console_client_test.go`

**Interfaces:**
- Produces `higressConsoleClient` with `GetRoute`, `PutRoute`, `DeleteRoute`, `ListRoutes`, `PutServiceSource`, `DeleteServiceSource`.
- The client uses `http.Client`, `baseURL`, `username`, and `password`; every request uses HTTP Basic Auth and JSON when a body exists.
- A `404` from delete is success; non-2xx responses return an error containing method, path, and status without credentials or response secrets.

- [ ] **Step 1: Write failing HTTP contract tests**

Use `httptest.Server` to assert Basic Auth, JSON request bodies, status handling, and idempotent delete:

```go
func TestHigressConsoleClientPutRoute(t *testing.T) {
	var gotAuth string
	var gotRoute map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPut || r.URL.Path != "/v1/routes/ani-route" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRoute); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	err := client.PutRoute(context.Background(), "ani-route", map[string]any{"name": "ani-route"})
	if err != nil { t.Fatal(err) }
	if gotAuth != "Basic "+base64.StdEncoding.EncodeToString([]byte("admin:secret")) { t.Fatalf("auth = %q", gotAuth) }
	if gotRoute["name"] != "ani-route" { t.Fatalf("route = %#v", gotRoute) }
}

func TestHigressConsoleClientDelete404IsSuccess(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := newHigressConsoleClient(server.URL, "admin", "secret", server.Client())
	if err := client.DeleteRoute(context.Background(), "missing"); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/data/kubernetes -run HigressConsoleClient -count=1`

Expected: compile failure because the client and methods do not exist.

- [ ] **Step 3: Implement the minimal client**

Define typed route/service-source payload aliases only at the adapter boundary:

```go
type higressConsoleClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func newHigressConsoleClient(baseURL, username, password string, httpClient *http.Client) *higressConsoleClient
func (c *higressConsoleClient) request(ctx context.Context, method, path string, body any) ([]byte, int, error)
func (c *higressConsoleClient) PutRoute(ctx context.Context, name string, route map[string]any) error
func (c *higressConsoleClient) ListRoutes(ctx context.Context) ([]map[string]any, error)
func (c *higressConsoleClient) DeleteRoute(ctx context.Context, name string) error
func (c *higressConsoleClient) PutServiceSource(ctx context.Context, name string, source map[string]any) error
func (c *higressConsoleClient) DeleteServiceSource(ctx context.Context, name string) error
```

Use `PUT /v1/routes/{name}` and `PUT /v1/service-sources/{name}` so retries converge. Do not log request bodies that contain credentials or arbitrary plugin configuration.

- [ ] **Step 4: Run the focused tests and verify success**

Run: `go test ./internal/data/kubernetes -run HigressConsoleClient -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the client**

```bash
git add internal/data/kubernetes/higress_console_client.go internal/data/kubernetes/higress_console_client_test.go
git commit -m "feat: add Higress console route client"
```

### Task 2: Replace HTTPRoute publication with native Higress routes

**Files:**
- Modify: `internal/data/kubernetes/http_route_publisher.go`
- Modify: `internal/data/kubernetes/higress_rate_limit.go`
- Modify: `internal/data/kubernetes/http_route_publisher_test.go`

**Interfaces:**
- Keep the existing `HTTPRoutePublisher` type name temporarily to avoid changing the business port wiring; its implementation will call `higressConsoleClient`.
- `Publish`, `Withdraw`, `ConfirmPublished`, `ConfirmWithdrawn`, and `Endpoint` keep their existing signatures.

- [ ] **Step 1: Add failing native payload tests**

Assert the generated route payload contains the exact native API shape:

```go
func TestNativeRoutePayloadUsesModelHeaderAndServiceSource(t *testing.T) {
	route := nativeRoutePayload("route-1", "/v1/completions", "qwen", "source-1", 100)
	if route["name"] != "route-1" { t.Fatal("missing route name") }
	path := route["path"].(map[string]any)
	if path["matchType"] != "PRE" || path["matchValue"] != "/v1/completions" { t.Fatalf("path = %#v", path) }
	headers := route["headers"].([]map[string]any)
	if headers[0]["key"] != "x-higress-llm-model" || headers[0]["matchType"] != "EQUAL" || headers[0]["matchValue"] != "qwen" { t.Fatalf("headers = %#v", headers) }
	services := route["services"].([]map[string]any)
	if services[0]["name"] != "source-1" || services[0]["port"] != 100 { t.Fatalf("services = %#v", services) }
}
```

- [ ] **Step 2: Run the focused tests and verify failure**

Run: `go test ./internal/data/kubernetes -run 'NativeRoute|HTTPRoutePublisher' -count=1`

Expected: the native payload test fails because the publisher still builds a Gateway API object.

- [ ] **Step 3: Implement deterministic native names and payloads**

Add helpers with these signatures:

```go
func nativeRouteName(pub publication.Publication) string
func nativeServiceSourceName(pub publication.Publication) string
func nativeRoutePayload(name, path, model, source string, port int) map[string]any
func nativeServiceSourcePayload(name, address string, port int) map[string]any
```

Use the runtime Service DNS name formatted as `fmt.Sprintf("%s.%s.svc.cluster.local", backendName, desired.Namespace)` and the KServe service port already selected by the existing provider logic. Preserve publication ownership in deterministic names and `customLabels` where the Console API supports it.

- [ ] **Step 4: Implement publish/withdraw convergence**

`Publish` must:

1. Resolve the current runtime exactly as today.
2. Validate `served_model_name`, backend name, namespace, and port.
3. `PUT` Service Source.
4. `PUT` model route.
5. Ensure the rate-limit model key and owner use the native route name.

If route creation fails after Service Source creation, delete that Service Source and return the error. `Withdraw` deletes the route first, then the Service Source. `ConfirmPublished` queries `GET /v1/routes/{name}`; `ConfirmWithdrawn` queries the same route and returns true only after it is gone.

- [ ] **Step 5: Move rate-limit ownership lookup to native routes**

Replace `hasModelRoute`'s `HTTPRouteList` query with `ListRoutes` and inspect each route's `headers` array for:

```json
{"key":"x-higress-llm-model","matchValue":servedModelName}
```

Keep the existing optimistic-lock update of `ani-request-rate-limit`; only the route existence lookup changes.

- [ ] **Step 6: Run focused tests and verify success**

Run: `go test ./internal/data/kubernetes -run 'NativeRoute|HTTPRoutePublisher|RateLimit' -count=1`

Expected: PASS with no `HTTPRoute` objects required by the publisher tests.

- [ ] **Step 7: Commit the publisher migration**

```bash
git add internal/data/kubernetes/http_route_publisher.go internal/data/kubernetes/higress_rate_limit.go internal/data/kubernetes/http_route_publisher_test.go
git commit -m "feat: publish inference routes through Higress API"
```

### Task 3: Wire credentials and remove the model HTTPRoute chart resource

**Files:**
- Modify: `cmd/ani-inference-service/main.go`
- Modify: `charts/ani-inference/values.yaml`
- Modify: `charts/ani-inference/templates/deployment.yaml`
- Modify: `charts/ani-inference/templates/higress-model-router-route.yaml`
- Modify: `charts/ani-inference/templates/higress-rbac.yaml`
- Modify: `charts/ani-inference/README.md`
- Modify: `charts/ani-inference/templates/NOTES.txt`

**Interfaces:**
- Add `ANI_HIGRESS_CONSOLE_URL`, `ANI_HIGRESS_CONSOLE_USERNAME`, and `ANI_HIGRESS_CONSOLE_PASSWORD` environment inputs.
- Keep `ANI_HIGRESS_PUBLIC_BASE_URL`, `ANI_HIGRESS_PATH_PREFIX`, and rate-limit settings.
- Export `NewHigressConsoleClient` for the composition root and add
  `EnsureBootstrapRoute` for the fixed model-router entry route.

- [ ] **Step 1: Add Helm rendering tests/fixtures for Secret references**

Render the chart with:

```bash
helm template ani-inference charts/ani-inference \
  --namespace ani-system \
  --set higress.publicBaseURL=http://192.168.102.68:30090 \
  --set higress.consoleURL=http://higress-console.higress-system.svc.cluster.local:8080 \
  --set higress.consoleCredentialsSecret.name=ani-higress-console-credentials
```

The output must contain `secretKeyRef` for username/password and must not contain literal credential values.

- [ ] **Step 2: Add the native API settings to values and deployment**

Add:

```yaml
higress:
  consoleURL: ""
  consoleCredentialsSecret:
    name: ""
    usernameKey: username
    passwordKey: password
```

Inject the values with `secretKeyRef`; fail Helm rendering if the secret name is empty while native routing is enabled.

- [ ] **Step 3: Remove the model-specific HTTPRoute template**

Delete `charts/ani-inference/templates/higress-model-router-route.yaml` from the chart. Add a
ClusterIP sink Service with no selector and a port 80; it has no endpoints and only prevents
unknown models from falling through to a real model. At process startup, call
`EnsureBootstrapRoute` to create/update a native `/v1/completions` route backed by a native
Service Source pointing to that sink Service. Remove `gatewayName`, `gatewayNamespace`, and
model bootstrap comments only where they are no longer consumed. Keep public URL and path
configuration for endpoint responses.

- [ ] **Step 4: Update RBAC and documentation**

Remove the chart's model `HTTPRoute` RBAC assumptions. Keep only the `WasmPlugin` permissions needed by rate-limit reconciliation. Document the Console API Secret and the route list endpoint in `README.md` and `NOTES.txt`.

- [ ] **Step 5: Run chart verification**

Run: `helm lint charts/ani-inference` and the exact `helm template` command above.

Expected: lint succeeds, no model `HTTPRoute` is rendered, and credentials are referenced only through Secret keys.

- [ ] **Step 6: Commit chart wiring**

```bash
git add cmd/ani-inference-service/main.go charts/ani-inference
git commit -m "feat: configure Higress native route credentials"
```

### Task 4: Replace old tests and validate the deployed route lifecycle

**Files:**
- Modify: `internal/data/kubernetes/kserve_live_test.go`
- Modify: `docs/execution/status.md`
- Create: `docs/execution/records/2026-10-19-higress-native-api-validation.md`

- [ ] **Step 1: Update live test setup**

The live test must query `GET /v1/routes/{name}` through the Console API and verify route payload, rather than looking for a Kubernetes `HTTPRoute` parent condition.

- [ ] **Step 2: Run repository checks**

Run:

```bash
go test ./internal/data/kubernetes ./internal/biz/inference ./cmd/ani-inference-service
go test ./...
helm lint charts/ani-inference
```

Expected: all selected tests pass. Any pre-existing unrelated failure must be recorded with its exact output.

- [ ] **Step 3: Deploy only the changed Inference chart**

Use Helm with the new environment's existing database, Model TLS, server TLS, and Higress Console credentials Secret. Do not run the install script and do not change Governance or Model deployments.

- [ ] **Step 4: Validate the native route lifecycle**

Record:

```bash
kubectl -n higress-system get svc higress-console
curl -u "$HIGRESS_USER:$HIGRESS_PASSWORD" \
  "http://127.0.0.1:18081/v1/routes"
kubectl -n ani-system get svc,pod
curl -H "x-api-key: $INFERENCE_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"'"$SERVED_MODEL_NAME"'","prompt":"你好","max_tokens":8}' \
  http://192.168.102.68:30090/v1/completions
```

Verify the route is visible through the Console API, the backend is the correct predictor Service, and the gateway returns a model response. Delete the inference service and verify both the native route and Service Source disappear; then verify a same-name recreation does not reuse stale routing.

- [ ] **Step 5: Commit validation evidence**

```bash
git add internal/data/kubernetes/kserve_live_test.go docs/execution/status.md docs/execution/records/2026-10-19-higress-native-api-validation.md
git commit -m "test: validate Higress native inference routes"
```

## Plan self-review

- The design's native route, Service Source, credentials, rate-limit, cleanup, and live validation requirements are covered by Tasks 1–4.
- No task changes Governance, Model, IAM, or engine command handling.
- The plan keeps the existing publication business port and avoids a broad package refactor.
- The native API's required backend is handled by the sink Service in the design; its exact creation is verified during Task 3/4 before enabling production traffic.
