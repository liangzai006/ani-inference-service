# 2026-09-12：gRPC 引擎与制品引用字段

为避免运行时 renderer 永远拿不到镜像和自定义 argv，`inference.v1` 增加了可选的 `ModelArtifact`（provider/reference/sha256）和 `EngineSpec`（type/image/command/args）。Create 请求会将这些字段规范化后落入 `inference_specs`；返回资源也携带同一组引用。字段仍是外部 Model/Engine 的引用，不代表 Inference 接管其目录或制品生命周期。

Buf 输出路径已修正为模块内的 `api/inference/v1`，使用开发机网络重新生成 protobuf。缺省制品或镜像仍会在后续 Model/engine provider 阶段 fail closed，不能宣称运行已就绪。
