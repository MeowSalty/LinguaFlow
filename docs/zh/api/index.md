# API 参考

LinguaFlow 提供 RESTful API，便于与外部系统集成。个人使用 Web / CLI 即可，不必先读本页。

## 基础信息

| 项目                                 | 说明                                                   |
| ------------------------------------ | ------------------------------------------------------ |
| Base URL（本地模式）                 | `http://127.0.0.1:18080/api/v1`                        |
| Base URL（服务器模式 / Docker 默认） | `http://localhost:8080/api/v1`                         |
| 认证方式                             | 本地模式通常无需认证；服务器模式为 Bearer Token（JWT） |
| 内容类型                             | `application/json`                                     |

::: tip 认证

- **本地模式**：免登录，适合本机调用
- **服务器模式（预览）**：需要 JWT；多用户能力仍在完善  
  :::

下文示例默认使用 **本地模式** 地址。若使用 Docker / `serve`，请把主机与端口换成 `8080`，并在需要时加上 `Authorization` 头。

## 完整 API 文档

完整的交互式 API 文档请访问：

<!-- 链接目标 redoc/index.html 位于 public/ 静态目录，VitePress 不会为它加 base 前缀，
     因此必须用相对路径，带 base 与不带 base 部署时才能都正确解析 -->
**[LinguaFlow API 文档](../../redoc/index.html){target="_blank"}**

<!-- PLACEHOLDER_QUICK_REF -->

## 常用场景（curl）

以下仅作集成入门；字段以 OpenAPI / Redoc 为准。

### 1. 探测运行模式

```bash
curl -s http://127.0.0.1:18080/api/v1/mode
```

用于前端或脚本判断本地 / 服务器模式。

### 2. 列出项目

```bash
curl -s http://127.0.0.1:18080/api/v1/projects
```

### 3. 服务器模式登录（预览）

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "password"}'
```

将返回中的 access token 用于后续请求：

```bash
curl -s http://localhost:8080/api/v1/projects \
  -H "Authorization: Bearer <token>"
```

### 4. 创建项目（示意）

具体 JSON 字段以 OpenAPI 为准，常见需要名称与语言方向，例如：

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/projects \
  -H "Content-Type: application/json" \
  -d '{"name":"demo","source_lang":"en","target_lang":"zh-Hans"}'
```

### 5. 列出 AI 后端

```bash
curl -s http://127.0.0.1:18080/api/v1/backends
```

### 6. 探测可用模型列表

使用当场提供的凭据向服务商拉取模型列表（**不落库**），用于填写创建后端时的 `options.model`：

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/backends/models \
  -H "Content-Type: application/json" \
  -d '{"type":"openai","api_key":"sk-...","base_url":""}'
```

成功时返回 `items` 数组，每项含 `id`（可直接写入 `model`）与 `name`。`type` 为 `openai` / `anthropic` / `google`；`base_url` 可选。

::: tip 创建后端时
`CreateBackendRequest` / `UpdateBackendRequest` 的 `options` 与其中的 `model` 均为 **必填**，不再有隐式默认模型名。
:::

上传资源、创建作业等涉及 multipart 或较长请求体，建议直接对照 **Redoc** 中的对应接口与示例。

### 7. 即时翻译（单段，不落库）

把单段文本交给某份执行计划，同步返回译文、质量标记、各轮摘要、批次诊断与用量。译文**不落库**，适合零散翻译或脚本集成。

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/quick-translate \
  -H "Content-Type: application/json" \
  -d '{
    "source_text": "Hello, world.",
    "source_lang": "auto",
    "target_lang": "zh",
    "execution_plan_id": 1
  }'
```

常用可选字段：

| 字段                 | 说明                                                                       |
| -------------------- | -------------------------------------------------------------------------- |
| `project_id`         | 可选。提供时复用该项目的术语表与语言配置，并校验访问权                      |
| `glossary`           | 内联临时术语表数组（`source`/`target` 必填，可选 `forbidden`/`mandatory` 等）。项目场景下叠加在项目术语表之上 |

响应中的 `round_summary[].status` 可能为 `success` / `partial` / `failed` / `skipped`（多轮计划后续轮次因 `segment_filter` 跳过）。并发与超时由服务端 `quick_translate` 配置控制，见 [配置文件与环境变量 · 即时翻译](/zh/guide/configuration#server-quick-translate-—-即时翻译)。

### 8. 单段预览（试译 / 修订，不落库）

对项目内某段落同步执行一次单段预览：**试译**对原文重跑流水线产出全新译文；**修订**以段落上 `pending` 语义 issue 为目标对现有译文做定点最小修订。二者不创建作业，只有显式调 `apply` 才写回段落。

```bash
# 试译预览：对单段原文用某执行计划跑一遍
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/42/translation-preview \
  -H "Content-Type: application/json" \
  -d '{"execution_plan_id": 1}'

# 修订预览：对已有译文按 pending 语义 issue 定点修订
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/42/revision-preview \
  -H "Content-Type: application/json" \
  -d '{"execution_plan_id": 1, "issue_codes": ["mistranslation", "omission"]}'
```

要点：

- 试译要求计划含至少一个 `translate` 轮；修订要求计划含 `revise` 轮或至少一个 `translate` 轮（后者时合成默认修订轮）
- 修订的 `issue_codes` 仅 8 个语义白名单、可选；与段落实有 `pending` 语义 issue 取交集，交集为空返回 409
- 修订在译文有实质变化时返回短期签名 `apply_token`，用与试译相同的 `.../translation-preview/apply` 端点写回（409 基线变化、410 过期）
- 与即时翻译类似，模型已启动后的成功/部分成功/失败统一 HTTP 200 并由 `status` 区分；并发满返回 429 带 `Retry-After`

字段全集见 [翻译配置 · 参考 · 单段预览](/zh/guide/translation-config-reference#单段预览-preview)。

### 9. 任务事件历史（分页）

作业执行事件（批次翻译、状态变化、质检等）除通过 SSE 实时推送外，也支持按 `seq` 游标的 REST 分页查询，便于外部监控或回放。

```bash
# 反向分页（取最近的一页，向上翻更早的事件）
curl -s "http://127.0.0.1:18080/api/v1/jobs/42/events?limit=50"

# 用返回的 next_before_seq 继续向上翻（取更早的）
curl -s "http://127.0.0.1:18080/api/v1/jobs/42/events?limit=50&before_seq=1234"

# 正向分页（从某个 seq 之后向后翻）
curl -s "http://127.0.0.1:18080/api/v1/jobs/42/events?limit=50&after_seq=1000"
```

| 参数         | 说明                                              |
| ------------ | ------------------------------------------------- |
| `after_seq`  | 正向游标：返回 seq **大于**此值的事件，升序排列    |
| `before_seq` | 反向游标：返回 seq **小于**此值的事件，降序排列    |
| `limit`      | 每页条数，`1`–`100`                                |

响应中的 `next_after_seq` / `next_before_seq` 为下一次翻页游标；`0` 表示无更多数据。字段全集见 Redoc 中 `JobEvent` / `JobEventListResponse`。

::: tip SSE 与 REST 怎么配合
SSE 负责「实时 + 最近窗口补进」，REST 历史端点负责全量分页。新连接默认只补最近窗口，更早历史走本端点。回放窗口大小由 `server.sse` 配置，见 [配置文件与环境变量 · 实时事件流](/zh/guide/configuration#server-sse-—-实时事件流)。
:::

SSE 也可直接通过 OpenAPI 收录的 `GET /jobs/{jobId}/stream` 端点订阅：原生 `EventSource` 无法设置 `Authorization` 头时，可用 `access_token` 查询参数携带令牌；断线重连用 `Last-Event-ID` 请求头或 `lastEventId` 查询参数传入上次收到的 seq，从该位置续传。生命周期事件含 `job_paused` / `job_resumed`（任务暂停/恢复完成时发布）。

### 10. 任务暂停 / 恢复 / 重试（断点续跑）

任务支持暂停与恢复，失败/取消的任务可重试并从断点继续（已完成轮次与段落直接跳过；显式手选段落的任务首个翻译轮会重译选中段落）：

```bash
# 暂停：优雅排空——停止派发新批次，等在途请求返回后冻结任务
curl -s -X POST http://127.0.0.1:18080/api/v1/jobs/42/pause

# 恢复：从轮次断点继续
curl -s -X POST http://127.0.0.1:18080/api/v1/jobs/42/resume

# 重试：失败或已取消的任务从断点续跑
curl -s -X POST http://127.0.0.1:18080/api/v1/jobs/42/retry
```

任务状态枚举为 `pending` / `running` / `paused` / `completed` / `failed` / `cancelled`；主进度字段为 `progress_completed` / `progress_total`（工作量口径，单位「段 × 轮」）。状态前置校验不满足时返回 409（如恢复一个未暂停的任务）。产品侧操作见 [项目管理 · 暂停与恢复](/zh/guide/projects#暂停与恢复)。

### 11. 活动与审计日志（服务器模式）

服务器模式记录两类活动视图，结构一致（`Activity`：`action` / `resource_type` / `message` / `metadata` 等）：

```bash
# 个人活动：当前用户可见范围内（自己 / 所在组织 / 项目）
curl -s http://localhost:8080/api/v1/activity \
  -H "Authorization: Bearer <token>"

# 全局审计日志：需管理员权限
curl -s "http://localhost:8080/api/v1/admin/audit-logs?limit=50" \
  -H "Authorization: Bearer <admin-token>"
```

两者均为基于 `id` 的反向游标分页（`limit` `1`–`100`，默认 `50`）：把当前页最后一条的 `id` 作为下一页 `cursor` 传入。

```bash
# 翻到更早的一页
curl -s "http://localhost:8080/api/v1/admin/audit-logs?limit=50&cursor=12345" \
  -H "Authorization: Bearer <admin-token>"
```

后端记录的 `action` 值、资源类型与触发场景对照见 [管理员后台 · 动作类型](/zh/guide/admin#动作类型)。管理员还提供用户管理、系统统计与设置接口，完整列表见 [管理员后台 · API 速览](/zh/guide/admin#api-速览)。

### 12. 质量问题裁决（驳回 / 撤销）

对某段译文上的单条质量问题下裁决：`dismissed` 判定为不是问题，`pending` 撤销裁决、恢复未决。操作可逆，返回更新后的段落。

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/42/issues/disposition \
  -H "Content-Type: application/json" \
  -d '{
    "code": "source_residual",
    "matched_text": "カタログ",
    "disposition": "dismissed",
    "note": "专有名词，有意保留"
  }'
```

| 字段            | 说明                                                       |
| --------------- | ---------------------------------------------------------- |
| `code`          | 问题代码                                                   |
| `matched_text`  | 问题指纹的 `matched_text`（无 `span` 的 issue 传空串）    |
| `disposition`   | `dismissed` = 判定不是问题；`pending` = 撤销裁决、改回未决 |
| `note`          | 裁决说明（可选），如「专有名词，有意保留」                |

`QualityIssue` 响应含 `disposition`（必填，`pending` / `dismissed`）、`decided_by`（裁决者 user_id，`null` 表示由 LLM 裁决）、`decided_at`（裁决时间）与 `note`（裁决说明）字段。已 `dismissed` 的问题不计入段落列表的质量筛选与统计。产品侧操作见 [翻译审校 · 质量问题裁决](/zh/guide/review#质量问题裁决)。

### 13. 段落搜索替换（search-replace）

对某个资源下的段落**译文**批量查找替换（原文不变），分三步：先预览影响范围，再应用写回，最后可按 `operation_id` 撤销。产品侧操作见 [翻译审校 · 搜索替换](/zh/guide/review#搜索替换)。

```bash
# 1. 预览：只读，不落库，返回受影响段落数、命中总数与替换前后对照样本
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/search-replace/preview \
  -H "Content-Type: application/json" \
  -d '{"find": "color", "replace_with": "colour"}'

# 2. 应用：写回译文，返回 operation_id（供撤销使用）
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/search-replace/apply \
  -H "Content-Type: application/json" \
  -d '{"find": "color", "replace_with": "colour"}'

# 3. 撤销最近一次替换（再次调用相当于重做）
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/resources/1/segments/search-replace/<operationId>/undo
```

预览与应用共用同一套请求字段：

| 字段             | 类型   | 默认        | 说明                                                         |
| ---------------- | ------ | ----------- | ------------------------------------------------------------ |
| `find`           | string | 必填        | 查找内容                                                     |
| `replace_with`   | string | 必填        | 替换文本；空串表示删除匹配内容                               |
| `match_mode`     | string | `substring` | `substring` 或 `regex`（RE2 语法，`replace_with` 支持 `$1` 捕获引用） |
| `case_sensitive` | bool   | `true`      | 是否区分大小写                                               |
| `whole_word`     | bool   | `false`     | 全字匹配，仅 `substring` 模式生效                            |

预览额外支持 `status` / `quality_issues` 等段落过滤参数与 `max_results`（`1`–`100`，默认 `20`）样本数上限；响应含 `matched_segment_count`（受影响段落数）、`total_replacements`（命中总次数）与样本数组（每项含 `before` / `after` 替换前后对照）。应用响应含 `operation_id`、`applied_count`、`skipped_count` 与跳过明细（如译文已不含匹配内容、替换后译文为空）。

要点：

- 被替换的段落状态置为 `edited`，并重新执行规则质检
- 撤销按 `operation_id` 回滚；替换后又被人工编辑过的段落会跳过（`target_diverged`）
- 替换历史超过保留期返回 404，全部段落均已变更、无可撤销内容返回 409；保留时长由 `server.revision_retention` 控制（默认 90 天）

另：段落列表端点 `GET /projects/{projectId}/resources/{resourceId}/segments` 的搜索还支持 `search_field`（`source` / `target` / `both`，默认 `both`）、`case_sensitive`（默认 `true`）与 `include_total`（默认 `false`，为 `true` 时响应附带满足过滤条件的 `total` 总数）查询参数。

### 14. QA 重检（qa-recheck）

用指定执行配置**当前**的 QA 配置对既有译文重跑确定性 QA 与文档级检查，同步返回统计结果（不创建任务）：只更新 `quality_issues`，不改译文与段落状态；同指纹问题继承既有人工裁决。产品侧操作见 [翻译审校 · QA 重检](/zh/guide/review#qa-重检)。

```bash
# 对资源 1、2 重跑 QA（使用执行配置 3 当前的 QA 设置）
curl -s -X POST http://127.0.0.1:18080/api/v1/projects/1/qa-recheck \
  -H "Content-Type: application/json" \
  -d '{"profile_id": 3, "resource_ids": [1, 2]}'
```

| 字段                | 类型     | 说明                                                                                              |
| ------------------- | -------- | ------------------------------------------------------------------------------------------------- |
| `profile_id`        | int      | 必填。执行配置 ID，取其**当前**的 QA 配置、阈值与保护规则；该配置未启用 QA 时返回 400              |
| `resource_ids`      | []int    | 资源范围。三者（含下两行）皆空时选项目内全部资源                                                  |
| `segment_ids`       | []int    | 限定具体段落；与 `segment_group_keys` 互斥                                                        |
| `segment_group_keys`| []string | 按章节分组键选段（仅 EPUB 等多章节资源，传 `meta.epub_file` 值）；优先级：分组键 > 段落 > 资源      |

选中资源上存在运行中任务时跳过该资源并在响应的 `resources_skipped_busy` 中报告（含占用它的任务 ID），避免与作业写入互相覆盖。响应为统计摘要（`QaRecheckResult`）：重检段落/资源数、新增（`issues_new`）与清除（`issues_cleared`）问题数、继承裁决数（`dispositions_inherited`）、无译文/并发修改跳过数与按资源明细。字段全集见 Redoc 中 `QaRecheckRequest` / `QaRecheckResult`。

## 错误码

| 状态码 | 说明                                       |
| ------ | ------------------------------------------ |
| 200    | 成功                                       |
| 201    | 创建成功                                   |
| 400    | 请求参数错误                               |
| 401    | 未认证                                     |
| 403    | 无权限                                     |
| 404    | 资源不存在                                 |
| 409    | 冲突（如重复资源、状态前置校验不满足）     |
| 422    | 语义/校验错误                              |
| 429    | 并发/限流（部分端点带 `Retry-After` 头）   |
| 500    | 服务器内部错误                             |

### Problem 响应与错误 type

错误响应为 RFC 9457 Problem JSON，`type` 字段是 `urn:linguaflow:<slug>` 形式的 URN，可按 type 编程区分错误类别（如 `urn:linguaflow:not-found`、`urn:linguaflow:invalid-input`、`urn:linguaflow:conflict`）。

常见细分 type：

| 场景             | type                                        |
| ---------------- | ------------------------------------------- |
| 未提供认证凭据   | `urn:linguaflow:token-missing`              |
| Token 过期       | `urn:linguaflow:token-expired`              |
| Token 无效       | `urn:linguaflow:token-invalid`              |
| 刷新令牌吊销     | `urn:linguaflow:refresh-token-revoked`      |
| 账号禁用         | `urn:linguaflow:user-inactive`              |
| 登录密码错误     | `urn:linguaflow:invalid-credentials`        |
| 取消/重试状态不符 | `urn:linguaflow:conflict`                  |

::: tip 任务取消 / 重试的前置校验
`CancelJob` / `RetryJob` 增加了状态前置校验，不满足时返回 409 Conflict（`type: urn:linguaflow:conflict`）：取消非可取消状态的任务（「任务当前状态不可取消」）、重试未失败的任务（「任务未失败，无法重试」）、无可重试失败资源（「没有可重试的失败资源」）。
:::

## OpenAPI 规范

完整的 OpenAPI 3.0 规范文件：

- 多文件规范：仓库内 `api/openapi/` 目录
- 合并后规范：`api/openapi/openapi-3.0.yaml`（亦可能由文档站 `public/openapi/` 提供）

::: tip 自动生成
前端 TypeScript 类型和后端 Go 代码均基于 OpenAPI 规范自动生成。集成时请以规范与 Redoc 为权威来源。
:::

## 相关文档

- [快速开始 · Web](/zh/guide/getting-started) — 界面流程
- [快速开始 · CLI](/zh/guide/cli-quickstart) — 不经过 HTTP 的批处理
- [使用模式](/zh/guide/modes) — 本地 / 服务器与认证差异
