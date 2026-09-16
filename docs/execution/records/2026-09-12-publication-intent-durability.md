# 2026-09-12：publication 意图先于 provider 调用持久化

`withdraw_publication` 和 `publish` 步骤现在会先以 `withdrawing`/`publishing` 状态写入 PostgreSQL，再调用远程 publication provider。provider 失败或进程在调用边界崩溃时，下一次 worker 仍能从同一 durable step 和 publication 状态恢复；确认成功后再写 `withdrawn`/`published`。写回继续使用 operation lease fencing。

验证：Runner 定向测试覆盖意图写入顺序、全量 Go 测试、`go vet`、`go build` 和本机 PostgreSQL 包测试通过。真实 publication provider 和数据面确认仍为 `not_verified`。
