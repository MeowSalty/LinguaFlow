# 后端测试与 CI 分层

PR 运行完整普通 Go 测试（`./...`、`-count=1`），保留 PostgreSQL 16 集成、格式、vet、安全扫描和对应所有权的 OpenAPI/ent 生成一致性检查。后端交叉编译 Linux、Windows、macOS 的 amd64。前端保留 lint、完整类型检查、单元测试和构建。

main 运行同一常规测试集的全量 race 与 atomic coverage，并编译上述三个 OS 的 amd64/arm64 六目标。PR 不生成 coverage；Go 测试编译缓存按普通/race 模式区分，测试结果仍由 `-count=1` 强制重新执行。并发改动应在合并前主动运行相关包的 race：PR 普通测试不能检测竞态，ARM 编译错误也可能在合并后才发现。

tag 发布保留发布前 race 检查、六目标二进制及前端嵌入流程；发布 Release 后仍构建并推送 linux/amd64、linux/arm64、linux/arm/v7 镜像。Docker 用原生 builder 交叉编译，arm/v7 映射为 `GOARM=7`，运行镜像的目标平台保持不变。

## 本地命令

在仓库根目录运行，使用已安装的 Go 和 Task。限制自动下载工具链可设置 `GOTOOLCHAIN=local`。

下面示例使用 sh 语法。PowerShell 中若 Task 的 pnpm shim 消费了参数分隔符，将 `--` 写成 `'--'`，例如 `task backend:test '--' -count=1 -timeout=20m ./...`。

```sh
# 与 PR 相同的完整普通测试
task backend:test -- -count=1 -timeout=20m ./...

# 与 main 相同的竞态及覆盖率检测
task backend:test:race -- -count=1 -timeout=20m -coverprofile=coverage.out -covermode=atomic ./...

# 并发改动合并前的定向检查（按实际改动选择包）
task backend:test:race -- -count=1 -timeout=20m ./internal/service ./internal/worker

# 显式容量验收：只运行两个满规模场景
task backend:test:capacity

# 容量入口也允许指定测试/包，示例为历史大规模原子清理
task backend:test:capacity -- -run '^TestTaskHistoryLargeAtomicDelete$' ./internal/service
```

`backend:test:capacity` 设置 `LINGUAFLOW_CAPACITY_TESTS=1`，默认只运行 `TestTaskHistoryLargeAtomicDelete` 和 `TestPersistenceSharedConcurrencyCapacityObservations`；只有值为 `1` 才启用满规模场景。默认 PR/main/tag 测试保留小规模真实取消、回滚和并发业务合同，满规模夹具通过这个显式入口验收。该入口不同于已有的 `backend:test:persistence:capacity` Node 观测报告，后者继续独立使用。

测试使用独立临时目录及数据库命名空间，容量验收需要足够的磁盘和内存。不要指向归属不明的共享数据库。需要 PostgreSQL 集成时，将 `LINGUAFLOW_TEST_POSTGRES_DSN` 设置为专用测试库连接字符串；未设置时相应 PostgreSQL 用例会跳过，不能据此宣称集成验证完成。CI 使用 workflow 的专用 PostgreSQL service。

后端、前端和 API workflows 继续按各自输入路径触发，并排除这些子树的纯 Markdown 文档；混合源码变更仍触发。2026-11-14 核验时 main ruleset 没有 required status checks。未来若把这些条件触发的 checks 设为必需，应同步检查纯文档 PR 的规则，避免因 workflow 跳过而停留 Pending。
