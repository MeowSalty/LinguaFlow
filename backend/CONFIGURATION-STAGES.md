# 配置架构分阶段交付记录

集成顺序与验收条件以 [configuration-architecture.md](docs/design/configuration-architecture.md) 为准。
每个阶段基于上一阶段构建；本记录仅列当前阶段实际完成的接线与验证，不把后续阶段能力视为已经交付。

## P0：契约清单

- 输入基线：`965f78e4`。
- 固定部署、初始化政策、共享执行模型、凭据和版本契约，以及 P0–P5 依赖与分支权限。
- 此阶段只增加设计与交付记录，不切换运行配置、API 或持久化格式。
- 验证：`task backend:build`、`task backend:lint` 通过；没有 Go 源码变更，不新增或重复运行行为测试。
- 后续范围：P1 独立解析/诊断；P2 初始化与启动；P3 凭据基础；P4 所有执行消费者与新快照同时切换；P5 生命周期管理。

## P1：独立部署解析与诊断

- 输入依赖：P0 契约。新增 `init --kind server`、`config check/explain --mode serve|local`。
- 严格部署文档、逐字段 env/显式 flag 覆盖、来源解释、敏感值脱敏、跨字段默认值、local 网络许可和只读实例密钥状态已可独立验证。
- keyring 文件格式、私有文件权限与原子发布属于配置依赖基础；此阶段没有持久化 provider 凭据或接入执行器。
- 新解析器使用独立的纯默认值与不修改输入的校验；现有 serve/local 启动函数在 P2 接入初始化事务时一次切换，旧启动配置函数随后删除。新部署文档不通过旧加载器转换。
- `init` 同时修复引用文件完整性和覆盖检查；翻译文档及执行语义仍在 P4 切换。
- 验证：`task backend:format`、`task backend:lint`、`task backend:build` 通过；通过 Task 运行 config、cli、credential、templates 包全部测试。
- Windows checkout 的 CRLF 模板已纳入 CLI 断言；没有将无关源码的换行状态噪声提交为内容差异。
- 尚未验收：新启动输入的实际运行消费者（P2/P4）、凭据仓储（P3）、前端配套及全阶段集成。
