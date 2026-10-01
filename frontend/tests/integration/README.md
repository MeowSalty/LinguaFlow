# 配置架构集成测试

此目录是长期维护的 Vitest 集成测试，用真实后端验证注册政策、组织授权、Profile 合并、凭据生命周期及任务快照。它与单元测试、Playwright 页面测试分别运行。

## 运行

在仓库根目录执行：

```powershell
task frontend:test:configuration-integration

# 可以独立运行一个场景，CLI_ARGS 直接传递给 Vitest。
task frontend:test:configuration-integration 'CLI_ARGS=-t=RPM'
```

需要 Node/pnpm、Task、Go 和完整的独立后端 checkout。默认使用相邻的 `LinguaFlow-backend`；可通过环境变量指定其他路径（CI 中也使用此变量）：

```powershell
$env:LINGUAFLOW_TEST_BACKEND = 'C:/Code_Working/LinguaFlow-backend'
task frontend:test:configuration-integration
```

测试套件先在指定 checkout 调用 `task backend:build`。不会修改后端源文件；构建产物写到后端自身的 `backend/bin`。该 checkout 必须支持当前配置及凭据 API；实际提交会写入报告。

## 组织与隔离

- `configuration.test.mjs`：10 个具名 Vitest 测试。保留 JavaScript 与 Node 严格断言，无需将已有脚本全部改写为 TypeScript。
- `fixtures/configuration-backend.mjs`：可复用的后端、模拟上游及测试数据工具。
- `../../vitest.integration.config.ts`：独立发现规则、超时及报告配置。
- 每个测试独立创建数据库、keyring、身份、凭据、HTTP 上游及后端进程。用例之间不共享业务数据，也不依赖执行顺序。
- 正常结束或断言失败均通过 fixture 的 `finally` 释放请求、停止自己启动的进程并关闭 HTTP 服务；请求与轮询响应 Vitest 的取消信号。
- 当前配置串行运行，禁止自动重试。普通 `task frontend:test` 仍只发现单元测试，不会构建或启动后端。

## 报告与维护

Vitest 输出逐项结果及 `tests/artifacts/configuration-integration/vitest-report.json`。每个测试还生成独立产物目录，包含 `report.json`（后端提交、运行模式、结果和已知限制）、`backend.log` 与隔离数据。产物按现有 Git 规则忽略，源码应长期保留。

修改凭据生命周期、授权、配置合并或任务运行逻辑时运行此套件。新增回归应使用独立 fixture，不依赖其他测试先创建的资产。测试未通过时，先查看 Vitest 的断言和对应目录日志。

当前上游为本地可控 OpenAI-compatible HTTP fixture，不覆盖外部模型服务兼容性。已验证后端 `df59969a` 的限制仍保留：撤销凭据或删除 Backend 后恢复被阻止，但返回通用 HTTP 500；测试同时核实后端拒绝原因和没有新增上游请求。
