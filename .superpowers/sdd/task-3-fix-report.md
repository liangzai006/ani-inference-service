# Task 3 修复报告

## 修复内容

- `CountServices` 与 `ListServicesPage` 的 capability predicate 对快照 JSON 数组元素使用 `lower(trim(c.value))`，再与服务层已规范化的小写参数做精确匹配。
- 两个查询继续使用相同的租户、状态、当前 desired generation 快照和筛选条件，筛选仍发生在分页前。
- 回归 fixture 使用 `" Vision "`，覆盖存量值的大小写和首尾空白规范化；请求值仍为 `vision`，并保持 `total == 2`、`Offset == 1` 的断言。
- 通过 sqlc 固定版本（v1.31.1）重新生成 `internal/data/postgres/inference.sql.go`。

## 验证

- `go test ./internal/data/postgres -run '^TestReadUseCasePageFiltersBeforeCountAndOffset$' -count=1 -v`：测试因未设置 `INFERENCE_PG_DSN` 按约定跳过。
- `go test ./internal/service ./internal/data/postgres -count=1`：通过。
- `git diff --cached --check`：通过。

配置 `INFERENCE_PG_DSN` 后，应运行上述聚焦测试以执行 PostgreSQL 回归断言。
