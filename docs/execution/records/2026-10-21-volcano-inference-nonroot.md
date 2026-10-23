# Volcano 与非 root 推理实测

日期：2026-10-21（Asia/Shanghai）。目标上下文
`kubernetes-admin@kubekey`，API Server `https://192.168.102.68:6443`。

## 已有实施与本轮复核

已有安全实现已提交并部署，本轮核查时 HEAD 为 `a2397ac`，控制面镜像为
`docker.changqingyun.cn/ani/inference-service:nonroot-20261021-r4`。
本轮继续核对代码、补做独立真实任务验证并记录最终结果；未重复安装平台。

- `security.go` 为 Deployment、LWS、KServe predictor/leader/worker 和物化 Job
  设置 UID/GID/fsGroup `65534`、`runAsNonRoot: true`、`RuntimeDefault` seccomp、
  `allowPrivilegeEscalation: false` 和 `capabilities.drop: [ALL]`。
- 推理 Pod、物化 Job 关闭 ServiceAccount token 自动挂载；控制面保留 API 凭据。
- 选择镜像中存在的 `nobody`，解决 Python 用户解析问题；为其 home `/nonexistent`
  挂载可写 emptyDir，保留只读模型目录、可写缓存与共享内存。启动命令保持请求原值。
- Model 仓库中早期设计副本的 UID `65532` 已纠正为最终实施的 `65534`。

## Volcano

- Helm release `volcano` 为 `deployed`，chart/app 版本均为 `1.12.1`。
- admission、controllers、scheduler 三个 Deployment 均为 `1/1` Ready。
- PodGroup/Queue CRD、默认队列及 admission EndpointSlice 存在。
  Chart 实际创建多个 `volcano-admission-service-*-validate` webhook，
  没有名为 `volcano-admission-service` 的 ValidatingWebhookConfiguration；计划命令已纠正。
- 本轮创建 `ani-volcano-nonroot-recheck` Volcano Job，使用 `default` 队列和
  `schedulerName: volcano`。事件的 `reportingComponent/source.component` 均为
  `volcano`，成功调度到 `ani-k8s-worker-1`，Job `Completed`、Pod `Succeeded`。
  实际日志：`volcano-scheduled uid=65534 gid=65534`。探针已删除。
- 配置实际名为 `volcano-scheduler-configmap`；未创建 Governance 约定的别名。
- 三个节点无 GPU 可分配资源。本轮证明 Volcano 的 CPU 调度与 admission 可用，
  不代表 GPU 推理或多节点 LWS 端到端验证通过。

## 既有联调证据复核

之前的服务 `ad09fb4d-f8ce-4f3c-8ab2-487037979dad` 已有实际 vLLM CPU 运行证据：
Pod 的运行时 `status.containerStatuses[].user.linux.uid/gid` 为 `65534`，
物化 Job 成功；Higress completion 返回 `Paris. The capital of Italy is Rome...`。
本轮直接查询 Governance，删除 operation `4d0a5107-ca53-48f9-beeb-d4572465d7ce`
已于 `2026-10-21T02:58:50.982860Z` 成为 `succeeded/complete`，列表为 `items: []`。
这补齐了旧 `/tmp/nonroot-delete-operation.json` 仍停留在 running 的中间快照。

## 本轮独立 CPU 联调

服务：`nonroot-recheck-110511`，ID：`b0faff3a-7684-4dad-bd9d-044e2ea550b5`。
创建操作：`777fcc04-7e48-4fd6-ae1d-3c4f0b13e965`。
通过 Governance `192.168.102.68:32248/api/v1/inference/services` 创建，返回
HTTP `202 Accepted`。使用已有真实 Model 版本
`9369a759-f1a4-48fd-9c48-1f727ba7459e`、固定 digest 的 CPU vLLM 镜像，
资源为 3 CPU / 8 GiB，独立 served model name 为 `nonroot-recheck-110511`。

- 创建 operation 在 `2026-10-21T03:10:31.069391Z` 完成为 `succeeded`。
- 物化 Job `ani-model-af08b4f6017c4aa3c9f0da54-fetch` 30 秒内完成；
  predictor `1/1 Running`，零重启。两类 Pod 的实际运行 UID/GID 均为 65534。
- 在 predictor 内执行 Python 验证：`CapEff=0000000000000000`、`NoNewPrivs=1`、
  `Seccomp=2`；`/tmp`、`/nonexistent`、`/dev/shm` 可写，ServiceAccount token 不存在。
- 首轮复核的 CPU 推理 Pod 的 `schedulerName` 为 `default-scheduler`。这是修改前的
  CPU/GPU 调度边界；独立 Volcano 探针成功不等于该推理 Pod 使用了 Volcano。
  用户随后明确要求 CPU/GPU 均使用 Volcano，后续实现与验证另列如下。
- Governance 详情/列表返回 HTTP 200，服务 `ready/published`。
- Higress `192.168.102.68:30090/v1/completions` 使用现有 `x-api-key`，
  仅从 JSON `model` 路由（未手动设置模型 Header），返回 HTTP 200，模型名匹配，
  输出 `Paris. The capital of Italy is Rome. The capital of Spain is Madrid.`，
  `prompt_tokens=5`、`completion_tokens=16`。
- 首次误用 Governance Bearer token 请求 Higress 返回 401；改用该入口既有 API key
  后成功，未修改任何鉴权策略。认证值不记录到仓库。
- 带有效 API key 向 Higress 请求 `/api/v1/inference/services` 返回 404；
  同一路径在 Governance 返回正常业务列表，两个入口独立验证。
- 服务详情的 `health` 仍为 `unknown`，与 ready/实际生成结果分开记录；
  本轮未修改该已有观测语义。

删除 operation：`9eac3c0c-2d1a-45e4-aada-479a42d9ccfc`。
最终在 `2026-10-21T03:14:20.978857Z` 成为 `succeeded/complete`；
Governance 列表为空，该服务的 CR、KServe、Deployment、ReplicaSet、Pod、Service、
Job、PVC、下载 Secret 均为零。Higress 模型 HTTPRoute 已删除。

## CPU/GPU 统一 Volcano 的后续修改

用户在本轮明确要求“CPU 和 GPU 推理都使用 Volcano”。据此：

- 新增 Kubernetes adapter 调度函数，所有推理计算 Pod 显式设置 `schedulerName: volcano`。
- CPU KServe predictor 和旧 Deployment/LWS 写入 `scheduling.volcano.sh/queue-name: default`；
  CPU LLMI 的 template/worker 传递 schedulerName，队列使用 Volcano 内置默认值。
  不给 LLMI PodSpec 添加不被 v0.16 CRD 支持的 annotations 字段。
- GPU 继续投影有效 Accelerator execution 的队列、节点、runtime class、设备资源和注解；
  拒绝与 Volcano 冲突的 scheduler，不覆盖或改写冻结计划，GPU LWS 保持已有显式不支持。
- 模型物化 Job 和服务控制面仍使用默认调度器；它们不是推理计算 Pod。
- 增加 CPU 多种运行时调度、GPU 调度冲突及 CPU predictor 调度字段丢失时拒绝 readiness 的测试。
- 修改后全量 `go test -count=1 ./...`、vet、build、diff check 通过。
- 新镜像 `docker.changqingyun.cn/ani/inference-service:volcano-nonroot-20261021-r6`，
  registry digest `sha256:2a8f035c0173a54ff97eda6d213f9bd916422bd39dc864c2ecd3d450d8aadfdc`。
- 升级前与已安装 Helm manifest 对比，只修改控制面镜像；保留现有 values，
  临时部署 Chart 使用原有证书模板，避免把工作区未完成的证书期限修改带入本次部署。

后续原始证据目录：`/tmp/ani-inference-volcano-e2e-20261021/`。

Helm revision 37 已部署并 Ready。经 Governance 新建服务
`volcano-nonroot-cpu-20261021`，ID `6008e639-2a00-4d30-80a5-aace7a5cfa1e`，
创建 operation `aa42561c-5fae-4cee-8261-42282aebeba4`。
实际 predictor Pod `volcano-nonroot-cpu-20261021-predictor-64f4794fbf-vgxl4`
为 `schedulerName: volcano`、queue annotation `default`；Scheduled event 的
`reportingComponent` 和 `source.component` 均为 `volcano`，分配到 `ani-k8s-worker-2`。
控制器创建的 `podgroup-b45991b4-5b51-4a26-a2fd-ee91563e0ccc`
使用 `default` 队列、`minMember: 1`，进入 Running。
这是推理 Pod 本身的调度证据，不是独立探针。

- 创建 operation 于 `2026-10-21T03:27:33.216283Z` 成为 `succeeded/complete`；
  predictor Pod `1/1 Running`，零重启。
- 物化 Job 成功。下载和推理容器的实际 UID/GID 均为 65534；
  predictor 内再次验证 `CapEff=0`、`NoNewPrivs=1`、`Seccomp=2`，
  缓存目录可写且没有 ServiceAccount token。
- Governance 列表/详情 HTTP 200，服务 `ready/published`；Higress 使用现有
  API key，仅靠 JSON model 路由，`/v1/completions` 返回 HTTP 200，
  completion ID `cmpl-1f847572-4d95-493e-b650-865fdba134f0`，模型为
  `volcano-nonroot-cpu-20261021`，生成文本：
  `Paris. The capital of Italy is Rome. The capital of Spain is Madrid.`
- Higress 的 `/api/v1/inference/services` 在有效 API key 下仍返回 404，
  与 Governance 管理入口单独验证。
- 删除 operation `f164d36c-539e-4275-8164-4316df7bc187` 于
  `2026-10-21T03:30:13.106195Z` 成为 `succeeded/complete`；
  Governance 列表 HTTP 200，`items: []`、`total: 0`。
- 按 service-id 实际查询，ANI CR、KServe CR、Deployment、ReplicaSet、Pod、Service、
  下载 Job、PVC、下载 Secret 均无残留；Volcano PodGroup 列表为空。
  Higress 模型 HTTPRoute 已消失，只保留固定 bootstrap 与原有默认入口。
- 最终 Volcano 三个 Deployment 和 Inference 控制面均 `1/1` Ready。
  验证服务已删除，后续通过 Governance 新建的 CPU 推理会使用当前已部署的 Volcano 调度逻辑。

## 验证边界

本轮完成单 Pod CPU vLLM 的真实 Volcano 调度、非 root 运行、模型物化、Governance
生命周期、Higress 生成与删除清理。GPU 没有实体设备，仅通过执行计划投影与拒绝错误
调度器的测试；GPU LWS 仍是既有显式不支持路径。CPU LLMI/LWS 有渲染测试，未作
多节点引擎实跑。`health=unknown` 的已有观测语义未在本轮改变。

本轮原始证据目录：`/tmp/ani-inference-real-e2e-20261021-recheck/`。
认证会话仅保存在该目录的权限受限临时文件中，不加入仓库。

## 本地检查

- `go test -count=1 ./...`：通过。
- `go vet ./...`、`go build ./...`、`git diff --check`：通过。
- 带必填 Higress 地址/Secret 名称的 `helm lint`、`helm template`：通过。
- Inference 仓库无 Makefile，不能声称执行过 `make verify`；采用上述仓库检查。
- 未配置本地 PostgreSQL/envtest 环境的测试不替代真实依赖测试；真实业务链路单列。
- 原有证书 values/templates 未提交或覆盖。
