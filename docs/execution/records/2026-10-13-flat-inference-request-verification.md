# 扁平推理请求验证记录（2026-10-13）

本记录对应 `2026-10-13-flat-inference-request` 计划，验证范围是 Model → Gov →
Inference → KServe 的新接口契约。命令字段在业务接口中是一个完整字符串；Kubernetes
适配层固定使用 `/bin/sh -c` 包装，不解析、拼接或追加引擎参数。

## 已通过

- Model：`make verify` 通过。Model 版本的镜像和完整启动命令可以保存、读取并参与
  幂等指纹计算。
- Inference：`go test ./...`、`go vet ./...`、`go build ./...`、`sqlc generate`、
  `git diff --check` 和 `./charts/verify.sh` 通过。渲染测试覆盖 KServe shell wrapper、
  CPU/内存、实例数、served model、LWS worker shape 和 GPU 管理字段。
- Gov：仓库中的布局检查、入口检查、代码测试和生成检查已执行；扁平创建请求以及
  Model Ready 门禁的定向测试通过。
- Inference API：`bash scripts/generate-api.sh lint` 和
  `bash scripts/generate-api.sh` 通过。脚本从 `go.mod` 锁定的 Accelerator 模块构造
  临时 Buf workspace，执行 lint/生成，并校验生成结果。
- Model 已通过固定版本的 `govulncheck v1.7.0` 和 `gitleaks v8.30.1` 工具检查；
  Inference 现在也提供 `bash scripts/security-audit.sh`，自动在仓库本地安装并使用
  同样的两个版本。
- 新集群只读预检：当前 kube context 是 `kubernetes-admin@kubekey`，三个节点均为
  `Ready`，KServe `InferenceService` CRD 和 LWS CRD 存在，`ani-system` 中 Model、
  Inference、Governance Deployment 均为 `Ready`。

## 未通过或未验证

- Inference 根目录的直接 `buf lint` / `buf generate` 仍不会自动加载外部导入
  `accelerator/integration/v1/owner_gpu_attachment.proto`；请使用上面的仓库脚本。
- Gov `make verify-ci`：业务测试执行到最后，但安全审计门禁仍不能全绿。Model 和
  Inference 已将 `golang.org/x/net` 升级到 `v0.60.0`、OpenTelemetry 升级到
  `v1.45.0`，这两类代码调用漏洞已消除。当前本机仍使用 Go `1.26.8`，因此
  标准库漏洞要等本机/构建环境升级到 `1.26.9` 后才能消除；`x/crypto/openpgp`
  只有模块级告警、业务依赖实际只使用 `argon2`，且上游没有修复版本。
- 集群没有任何节点上报 `nvidia.com/gpu`，因此 GPU、Accelerator 和多机多卡为
  `not_verified`。
- 本次只做了集群预检，没有在集群创建新的推理实例。Model 导入端点、Gov 认证凭据、
  Higress 公网入口和实际 KServe CPU 推理调用未在本次门禁中执行，因此不能据此声称
  Gov → Model → Inference → Higress 的完整数据面链路已经通过。

## 重跑命令

```bash
# Model
cd /root/kubercon/ani-model-service
make verify

# Governance（需要同级 ani-model-service、ani-inference-service）
cd /root/kubercon/ani-governance
make verify-ci

# Inference（自动带入锁定版本的 Accelerator proto）
cd /root/kubercon/ani-inference-service
bash scripts/generate-api.sh lint
bash scripts/generate-api.sh
sqlc generate
go test ./...
go vet ./...
go build ./...
git diff --check
./charts/verify.sh

# 只读集群预检
kubectl config current-context
kubectl get nodes -o wide
kubectl get crd inferenceservices.serving.kserve.io leaderworkersets.leaderworkerset.x-k8s.io
kubectl get deploy,pod -n ani-system
```

真实 CPU 冒烟需要在确认 Model 版本为 `ready`、Gov 认证和 Higress 入口可用后，使用
Helm 或 `kubectl` 创建一条测试推理服务；验证 KServe 对象、Pod 的 `/bin/sh -c` 与完整
命令字符串后，再通过 Gov 删除该服务。不要使用测试安装脚本。
