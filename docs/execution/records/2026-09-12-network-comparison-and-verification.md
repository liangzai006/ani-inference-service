# 2026-09-12 Network 对照与 Inference 回归验证

## 对照结论

Network 的观察方案采用 `client-go dynamic + SharedIndexInformer`：Watch 只通知
PostgreSQL，持久 worker 执行 Observe/Ensure/Delete，周期性完整 List 负责恢复和最终
校验；它不使用 controller-runtime。Inference 拥有推理服务运行时生命周期，因此使用
controller-runtime 管理自己的 InferenceService CR 和 Deployment/Service/Pod/Job/LWS
事件，同时仍把 PostgreSQL `resource_work` 作为可靠队列。两者共享事件唤醒、持久工作、
幂等观察和 List 恢复原则，但 Inference 额外需要 generation、publication 撤销、配额
预留、模型就绪、调用健康和 runtime UID/resourceVersion fencing。

## 本轮验证

在当前工作树执行：

```text
GOCACHE=/tmp/ani-kube-cache go test -p 1 ./... -count=1
GOCACHE=/tmp/ani-kube-cache go vet -p 1 ./...
GOCACHE=/tmp/ani-kube-cache go build -p 1 ./...
```

三项均退出码 0。该证据覆盖源码编译、单元测试和本地 fake/领域路径，不覆盖真实
quota/Model/Storage/IAM/data-plane provider、真实 Pod/GPU、多节点 LWS 或进程级
operation crash recovery；这些继续标为 `not_verified`。本轮未修改旧 ANI、前端、集群
或远程仓库。

## 隔离 Kubernetes 全量回归

在开发机使用本机 envtest assets 和官方 LWS v0.10.0 CRD 执行：

```text
KUBEBUILDER_ASSETS=/root/.local/share/kubebuilder-envtest/k8s/1.36.2-linux-amd64 \
LWS_CRD_PATH=/root/.config/.gvm/go/gopath/pkg/mod/sigs.k8s.io/lws@v0.10.0/config/crd/bases \
go test -p 1 ./internal/data/kubernetes -count=1 -v
```

退出码 0。测试覆盖 controller-runtime CR/runtime watch、Deployment/Service/LWS typed
SSA 冲突、Service terminating/absence、UID/resourceVersion fencing、状态投影并发、
官方 LWS controller 创建 StatefulSet、Pod worker readiness 和 Job wake-only 映射。
这是隔离 API server/etcd 证据，不代表真实集群中的 Pod/GPU 调度、多节点 LWS、引擎
调用或进程级 operation 重启恢复已经完成。

## 当前版本 PostgreSQL 集成回归

将当前 `internal/data/postgres` 测试编译为静态二进制，在 healthy 的
`recycling-postgres` 容器内通过 Unix socket 设置：

```text
INFERENCE_PG_DSN=postgresql:///ani_inference?host=/var/run/postgresql&user=recycling_user
```

全量集成测试退出码 0。真实覆盖包括事务、租户隔离分页、配额资源快照、operation
step CAS、跨代 publication/replacement、runtime binding UID/resourceVersion、状态
投影控制身份、Update generation CAS、resource_work lease、启动恢复和 repair。此次
没有执行迁移、清库或集群操作；外部 provider 和正式进程崩溃恢复仍是 `not_verified`。
