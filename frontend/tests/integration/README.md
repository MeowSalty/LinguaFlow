# 配置与文件存储集成测试

此目录是长期维护的 Vitest 集成测试，用真实后端验证注册政策、组织授权、Profile 合并、凭据生命周期及任务快照。它与单元测试、Playwright 页面测试分别运行。

## 任务历史保留套件

`task frontend:test:task-history-integration` 使用相同的独立后端构建入口，单独运行 `task-history.test.mjs`。需要 Node 22.13+ 的 `node:sqlite`、Task 和 Go；默认运行五项真实 HTTP 场景，浏览器场景不在未准备预览服务时自动启动。数据库、配置、日志、报告和截图均在 `frontend/tests/artifacts/task-history-integration/` 的本例目录内。

五项场景覆盖管理员版本冲突、关闭时预览、个人与组织写权限、活动/暂停拒删、两类任务批量删除、项目成果/固定导出/独立用量保留及真实自动过期。自动过期只在本例后端停止后修改指定终态任务的两个时间字段；TM 保留检查显式种入一条既有记忆，不把它当作当前翻译流程产出的证明。全部数据路径均在访问前解析并核验位于本例隔离目录。

先构建前端并启动 `task frontend:preview`，再启用真实产品双用户场景：

```powershell
$env:LINGUAFLOW_TEST_PRODUCT_URL = 'http://127.0.0.1:4173'
task frontend:test:task-history-integration
Remove-Item Env:LINGUAFLOW_TEST_PRODUCT_URL
```

浏览器场景用两个独立 context 登录组织 owner/admin，经真实界面删除翻译及同步历史；另一用户通过终态详情复核收到真实 404 并收敛。请求只做同源转发，业务响应不 mock，不保存令牌、HAR 或 trace。运行报告明确记录结果；预览的 partial、维护屏障、数据库延迟及大规模压力仍需对应单元测试或额外部署证据。

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

## 独立文件存储套件

```powershell
task frontend:test:storage-integration
task frontend:test:storage-integration 'CLI_ARGS=-t=S2'
```

`vitest.storage-integration.config.ts` 仅发现 `storage*.test.mjs`；原配置套件仅发现 `configuration.test.mjs`，两者不会意外互相启动。复用上述独立后端 checkout 与构建流程。存储 fixture 显式启用 Local 存储，并把数据库、配置、keyring、对象、临时文件、缓存和日志全部放在 `frontend/tests/artifacts/storage-integration/` 下的独立目录，不使用部署中的真实存储根目录。

套件使用真实前端 `api/projects.ts` / `api/storage.ts` 请求封装及真实认证中间件；仅替换 Node 测试的国际化文案函数。十个场景独立创建身份、数据库、项目和后端进程：

| 场景 | 实际断言                                                                                                                               |
| ---- | -------------------------------------------------------------------------------------------------------------------------------------- |
| S1   | 管理员发现 Local 连接/空间、显式项目绑定、真实上传；诊断请求前后任务、项目及当前源版本不变                                             |
| S2   | 预览候选不发布；预览后保存译文使旧双代次提交返回 409；真实代理在提交成功后切断 TCP 响应，只凭原预览 task ID 恢复结果，确认没有重复提交 |
| S3   | 对已存在的源对象执行同字节修复，资源 ID、源版本 ID、哈希和版本数量不变                                                                 |
| S4   | ready 固定交付等于普通下载快照；修改译文后旧交付不变；重建得到新 artifact ID 但保持旧快照；不新建翻译任务                              |
| S5   | 启动真实无头 Chromium，使用内存认证和同源 HTTP 发送 File 预览与 Blob 修复内容；不手写 `Content-Length`，后端实际接受完整内容           |
| S6   | 普通 owner 在真实产品抽屉选择 File 并预览/提交；浏览器转发真实后端响应，未 mock 业务结果                                               |
| S7   | 普通用户、组织 owner/admin/member、外部用户和平台管理员的存储正负权限                                                                  |
| S8   | 真实 HTTP chunked 无长度请求返回 411，声明长度超预算返回 413                                                                           |
| S9   | 在隔离对象目录移走真实原件；拒绝同大小异字节，接受同字节修复且保持版本、代次及段落                                                     |
| S10  | 删除墓碑关联稳定任务 ID，清理从 pending 到 done，共享重建交付仍能下载                                                                  |

S5 需要已安装的 Playwright Chromium，可执行 `task frontend:test:install-browser`。浏览器不会保存认证令牌、HAR 或 trace。TCP 故障代理只转发当前隔离项目的一次提交，执行完成或失败均关闭自身服务；测试日志只记录状态与对象 ID，不输出令牌和密钥。

S6 还需要已构建前端及 4173 预览服务：先执行 `task frontend:build`，在另一终端执行 `task frontend:preview`。测试仅在临时浏览器 context 中保存隔离测试账号的令牌，关闭后销毁，不记录 HAR/trace。S9 的对象故障注入核查真实路径位于自己的 runDir 内；S10 仅修改自己隔离服务的清理延迟并重启该服务。

2026-10-04 已在独立后端 `b4f46422` 的 C01–C09 工作树合同上实跑十项通过。各场景报告自动记录实际后端 HEAD、二进制 SHA-256、前后端合同 SHA-256 和对象身份；汇总报告为 `tests/artifacts/storage-integration/vitest-report.json`。当前前后端 bundle 仅换行符不同。真实 S3/PostgreSQL、提供商撤销/迁出和清理阻塞、cutover 进程中断与备份 pin 仍需部署证据，详见 [本轮实施与验收](../../docs/design/file-storage-implementation-verification.md)。
