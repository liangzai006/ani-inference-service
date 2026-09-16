# 2026-09-12 model materialization metrics

## 已实现

- `Runner` 在 `materialize_model` 步骤围绕 `ModelPort.EnsureModel` 记录每次尝试时延。
- 新增低基数 OTel 指标：`ani_model_materialization_duration_seconds` 直方图，以及
  `ani_model_materialization_error_total{error_class}` 计数器。
- `error_class` 只允许 `context`、`provider_missing`、`not_ready` 和
  `provider_or_domain`；不写入 tenant、service、operation、model 或 request 标签。
- provider 返回错误和未建立 readiness 的结果都会计为错误；成功建立 readiness 只记录时延。

## 验证与限制

- `GOCACHE=/tmp/ani-go-cache go test ./internal/biz/inference ./internal/biz/work -count=1` 通过。
- 指标描述的是本地 `EnsureModel` provider 尝试，不代表真实 Model/Storage provider 已接入。
- 真实模型加载时延、错误 taxonomy 和 provider 端到端数据仍为 `not_verified`。
