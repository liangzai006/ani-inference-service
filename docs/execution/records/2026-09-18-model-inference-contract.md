
## 2026-09-19 D generic runtime and scale completion

- Removed the first-slice Deployment replica=1 admission and renderer restriction; Deployment replicas are now any positive value.
- Reduced the distributed LeaderWorkerSet floor to one worker and kept resource aggregation aligned with the rendered group size.
- Model materialization no longer requires vLLM or Deployment. Verified Model tar artifacts can mount into Deployment and LeaderWorkerSet templates; vLLM offline environment variables remain conditional.
- PVC request size now derives from the verified ModelVersion size with a two-times extraction headroom floor of 1Gi, instead of a universal 1Gi claim.
- Focused tests and full `go test -p 2 ./... -count=1`, `go vet -p 2 ./...`, and `go build -p 2 ./...` passed. Kubernetes lifecycle/recovery acceptance remains pending.
