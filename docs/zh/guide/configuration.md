# 配置文件与环境变量

本文档列出 **命令行参数、环境变量与配置文件字段**，适合查阅与部署。Web 端翻译流水线的界面说明见 [翻译配置 · 使用](/zh/guide/translation-config)；字段级 Web 参考见 [翻译配置 · 参考](/zh/guide/translation-config-reference)。

::: tip 第一次使用？

- Web：[快速开始 · Web](/zh/guide/getting-started)
- 命令行：[快速开始 · CLI](/zh/guide/cli-quickstart)
- 概念：[核心概念](/zh/guide/concepts)
  :::

LinguaFlow 使用两类**互相独立**的 YAML 配置文档：

| 文档 | kind / version | 生成方式 | 用途 |
| --- | --- | --- | --- |
| 翻译配置 | `kind: translation` · `version: 2` | `linguaflow init --kind translation` | `linguaflow translate` 命令行翻译 |
| 服务器部署文档 | `kind: server` · `version: 1` | `linguaflow init --kind server` | `linguaflow serve` / `local` 部署 |

两类文档**不能混用**，字段也互不通用。

## 配置优先级

```text
模式内置默认值 < 配置文件显式字段 < 环境变量 < 显式命令行参数
```

- 只写显式字段：未写的字段沿用内置默认值，显式写下的 `false`、`0`、`[]` 会被如实保留（不会被当成「未设置」）
- 部署文档只通过 `--config`（或 `LINGUAFLOW_SERVER_CONFIG`）选择，程序**不会**自动搜索目录、也不会读取 `.env` 文件
- 配置解析是**严格校验**的：未知字段、重复键、YAML 别名/锚点、null 值、多文档 YAML 都会直接报错，范围类错误不会静默钳制。写错配置会在启动时立即失败，而不是带病运行

## 环境变量

### 常用部署变量速查

服务器模式（`serve`）与本地模式（`local`）共用一套部署变量。完整的字段级对照见下方「服务器部署文档」一节——每个 `server.*` 字段都有一个同名大写蛇形的 `LINGUAFLOW_*` 环境变量（如 `server.data_dir` ↔ `LINGUAFLOW_DATA_DIR`）。

| 变量 | 用途 | 适用模式 |
| --- | --- | --- |
| `LINGUAFLOW_HOST` / `LINGUAFLOW_PORT` | 监听地址与端口 | serve / local |
| `LINGUAFLOW_DATA_DIR` | 数据目录 | serve / local |
| `LINGUAFLOW_JWT_SECRET` | JWT 签名密钥（serve 必需，≥32 字节；local 自动生成） | serve / local |
| `LINGUAFLOW_CREDENTIALS_MASTER_KEY` | AI 密钥的凭据加密密钥（serve 必需；与 `*_KEYRING_FILE` 互斥） | serve / local |
| `LINGUAFLOW_CREDENTIALS_KEYRING_FILE` | 凭据 keyring 文件路径（多密钥轮换场景） | serve / local |
| `LINGUAFLOW_BOOTSTRAP_REGISTRATION_ENABLED` | 首次初始化时是否开放注册（默认 `false`） | serve / local |
| `LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME` / `_EMAIL` / `_PASSWORD` | 首次初始化的管理员（serve 首次启动必需） | serve |
| `LINGUAFLOW_SERVE_UI` | `false` 时仅提供 API（也可用 `--no-ui`） | serve / local |
| `LINGUAFLOW_LOG_LEVEL` / `LINGUAFLOW_LOG_FORMAT` | 日志级别与格式 | serve / local |
| `LINGUAFLOW_TRANSLATION_CONFIG` | `translate` 命令的翻译配置路径（优先级低于 `--config`） | translate |

::: warning 旧变量已移除
旧版本的 `LINGUAFLOW_ADMIN_USERNAME` / `LINGUAFLOW_ADMIN_PASSWORD`（启动时建管理员）与 `LINGUAFLOW_REGISTRATION_ENABLED` / `LINGUAFLOW_REGISTRATION_AUTO_ADMIN` 已不再被读取，请迁移到上表的 `LINGUAFLOW_BOOTSTRAP_*` 变量。
:::

### 敏感值与 `_FILE` 文件注入

敏感环境变量（`LINGUAFLOW_JWT_SECRET`、`LINGUAFLOW_CREDENTIALS_MASTER_KEY`、`LINGUAFLOW_DATABASE_DSN`、`LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD`）都支持 `_FILE` 后缀形式，值改为指向包含密钥的**文件路径**：

```bash
# 容器部署配合 Docker secrets 的典型用法
LINGUAFLOW_JWT_SECRET_FILE=/run/secrets/jwt-secret
```

- 同一变量的直接值与 `_FILE` 形式**同时设置会报冲突**（包括显式空值）
- 文件末尾最多移除一个换行符（LF/CRLF），其余空白原样保留
- 错误信息不会打印文件内容

### 数据库环境变量（serve）

| 变量名 | 描述 | 默认值 |
| --------------------------------------- | --------------------------------------------------------------------------- | -------------------------- |
| `LINGUAFLOW_DATABASE_DRIVER` | 数据库驱动：`sqlite` \| `postgres` | `sqlite` |
| `LINGUAFLOW_DATABASE_DSN` | 数据库连接串（支持 `_FILE`）。`postgres` 必填；`sqlite` 为空时使用 `data_dir/linguaflow.db` | - |
| `LINGUAFLOW_DATABASE_MAX_OPEN_CONNS` | 最大打开连接数 | `sqlite=0` / `postgres=25` |
| `LINGUAFLOW_DATABASE_MAX_IDLE_CONNS` | 最大空闲连接数 | `sqlite=2` / `postgres=5` |
| `LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME` | 连接最大寿命，Go duration 格式（如 `30m`） | `postgres=30m` |

数据库也可以写在部署文档的 `server.database` 段（见下文），环境变量优先。本地模式始终 SQLite，`LINGUAFLOW_DATABASE_*` 变量被忽略。

示例：

```bash
export LINGUAFLOW_DATABASE_DRIVER=postgres
export LINGUAFLOW_DATABASE_DSN='postgres://user:pass@localhost:5432/linguaflow?sslmode=disable'
linguaflow serve --config server.yaml
```

::: tip 自动迁移与并发安全
启用 `auto_migrate`（默认开启）时，PostgreSQL 实例会通过 `pg_advisory_lock` 串行化 schema 迁移，多个 LinguaFlow 实例可同时连接同一数据库而不会在启动阶段产生冲突。
:::

### 配置文件中的环境变量引用

配置文件中的字符串值支持 `${VAR_NAME}` 与 `${VAR_NAME:-default}` 两种引用，在解析阶段展开：

```yaml
backends:
  openai-default:
    type: openai
    secret: ${OPENAI_API_KEY}
    options:
      base_url: ${CUSTOM_API_URL:-https://api.openai.com/v1}
```

- `${NAME}` 在变量**未设置或为空**时整体报错（不再静默展开为空串）
- `${NAME:-fallback}` 在变量缺失或为空时使用 `fallback`
- 引用在 YAML 解码后按字面替换，值中的冒号、引号、换行不会被解释为 YAML 结构
- 提示词模板正文是**字面内容**，其中的 `${...}` 不会被展开

## 翻译配置（CLI `translate`）

`linguaflow init --kind translation` 生成翻译配置及全部引用文件：

```text
<配置目录>/
├── linguaflow.yaml                      # 主配置文件（kind: translation / version: 2）
├── prompts/
│   ├── default_translation.tmpl         # 翻译提示词模板
│   └── default_bootstrap.tmpl           # 术语提取提示词模板
└── profiles/
    └── default.yaml                     # 默认翻译策略（schema_version: 1）
```

可通过 `--path` 指定主配置路径，`--force` 覆盖已有文件。`translate` 通过 `--config`（或 `LINGUAFLOW_TRANSLATION_CONFIG`）选择配置；两者都未提供时直接使用内置默认文档（要求设置 `OPENAI_API_KEY`）。

顶层仅支持以下键：`backends`、`translation_prompt_templates`、`bootstrap_prompt_templates`、`translation_profiles`、`execution`、`glossary`、`log`（以及 `source_lang` / `target_lang`）。写其他顶层键（如 `translation_memory`、`plugins`、`output`）会直接报错——输出路径用 `--output` 参数提供；翻译记忆、插件与质量检测等能力以 Web 端为准。

### 配置结构

```yaml
kind: translation
version: 2

# 语言设置
source_lang: auto
target_lang: zh

# AI 后端（map，key 为后端名称）
backends:
  openai-default:
    type: openai
    enabled: true
    rate_limit_per_minute: 0
    # 密钥独立于 options；options.api_key 已被拒绝
    secret: ${OPENAI_API_KEY}
    options:
      base_url: https://api.openai.com/v1
      model: gpt-4o-mini # 必填，无内置默认
      temperature: 0.2
      max_tokens: 0
      timeout: 60s
      response_format: json_schema
      # stream: false          # 兼容网关仅接受 stream:true 时开启
      # thinking_level: low    # 可选：off | minimal | low | medium | high；不设置 = 不传思考参数

# 翻译提示词模板（map，key 为模板名称）
translation_prompt_templates:
  通用提示词:
    # content: |  # 内联内容
    #   ...
    file: prompts/default_translation.tmpl # 或引用外部文件（与 content 二选一）

# 术语抽取提示词模板
bootstrap_prompt_templates:
  通用术语抽取:
    file: prompts/default_bootstrap.tmpl

# 翻译策略（map，key 为策略名称；外部文件必须带 schema_version: 1）
translation_profiles:
  通用策略:
    # file: profiles/default.yaml   # 或内联以下字段（二选一）
    protect:
      enabled: true
      rules: [code, link, placeholder, xml]
    ruby:
      enabled: true
      preserve_kinds: [creative]
    postprocess:
      enabled: true
      trim_spaces: true
    repair:
      enabled: true
      json_structural: true
      schema_aliases: true
      placeholder_normalize: true
      prompt_upgrade: true
    context:
      enabled: true
      before: 1
      after: 1
      max_chars: 0
    # qa 段仅接受默认值；CLI 不执行翻译质量检测，启用请用 Web 端
    # 术语提取也不在策略里（glossary.bootstrap 已移除），改在翻译轮次上配

# 执行计划（CLI：translate / extract / revise）
execution:
  # 计划级策略引用：translate 与 revise 轮统一使用该策略的行为预设。
  # 引用上方 translation_profiles 中的 key；未写时用内置默认策略，
  # 写了但名称不存在会直接报错（不再静默回退）。
  profile: 通用策略
  rounds:
    - mode: translate
      backend: openai-default
      translate:
        prompt: 通用提示词
        batch_size: 1
        max_words_per_batch: 0
        concurrency: 4
        fallback_shrink: 0.5
        # 内联术语提取（可选）：翻译响应中顺带抽新术语；
        # 顶层 glossary.enabled 与项目术语表开关为总开关
        inline_term_extraction:
          enabled: false
          max_terms_per_1000_words: 3
          min_source_len: 2
          conflict_strategy: rewrite-local
        retry:
          max_attempts: 3
          backoff_ms: 2000
          jitter: true
    # - mode: extract
    #   backend: openai-default
    #   extract:
    #     template: 通用术语抽取
    # - mode: revise          # LLM 修订轮，需配合 --revision-input
    #   backend: openai-default
    #   revise:
    #     batch_size: 10
    #     concurrency: 1
    #     segment_scope: with_issues

# 注音重试（可选）：注音失败时用指定后端重试
# execution:
#   ruby_retry:
#     enabled: true
#     backend: openai-default
#     max_attempts: 1

# 术语表
glossary:
  enabled: false
  path: ./glossary.csv
  save: true

# 日志
log:
  level: info
  format: text
```

### backends — AI 后端

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `type` | string | 后端类型：`openai` / `anthropic` / `google` |
| `enabled` | bool | 是否启用，默认 `true` |
| `secret` | string | 该后端的 API 密钥，**必填**；支持 `${ENV}` 引用 |
| `rate_limit_per_minute` | int | 每分钟请求限制，`0` 表示不限制 |
| `options` | map | 模型与请求参数，见下表 |

::: warning `options.api_key` 已被移除
密钥统一写在后端的 `secret` 字段；在 `options` 里写 `api_key` 会直接报错。旧的 `translation_memory`（翻译记忆）、`plugins`、`output` 等顶层配置同理，CLI 不再接受。
:::

**options 通用字段：**

| 字段 | 类型 | 说明 |
| ---------------- | ------ | ---------------------------------------------------------------------------------------------------- |
| `base_url` | string | 自定义 API 端点 |
| `model` | string | 使用的模型（必填） |
| `temperature` | float | 生成温度 |
| `max_tokens` | int | 最大 token 数，`0` 表示自动（Anthropic 默认常为 `8192`） |
| `timeout` | string | 请求超时时间，如 `60s` |
| `stream` | bool | 是否以流式发起请求（内部累积为完整响应），默认 `false`。`true` 适用于只接受 `stream:true` 的兼容网关 |
| `thinking_level` | string | 思考强度：不设置（默认，不传思考参数）/ `off`（显式关闭）/ `minimal` / `low` / `medium` / `high`，详见 [翻译配置 · 参考 · 思考强度](/zh/guide/translation-config-reference#思考强度-thinking-level) |

### translation_profiles — 翻译策略

控制翻译行为，使用 map 结构，key 为策略名称。可通过 `file` 字段引用外部文件（外部文件顶层必须带 `schema_version: 1`），或内联配置。策略文件支持的分段：`protect`、`ruby`、`postprocess`、`repair`、`context`、`qa`。显式写下的 `false` / `0` / `[]` 会被保留；写其他分段（如旧的顶层 `split`、`bootstrap`、`glossary`）会报错。

::: warning 术语提取已从策略移出
旧版策略里的 `glossary.bootstrap` 分段**已移除**，写入会因未知字段被严格校验拒绝。术语提取改在 `execution.rounds[].translate.inline_term_extraction` 上配置（见 [execution — 执行计划](#execution-—-执行计划) 的 [inline_term_extraction](#inline-term-extraction-—-内联术语提取)）。
:::

##### protect — 内容保护

| 字段 | 类型 | 默认值 | 说明 |
| -------- | -------- | -------------------------------- | ------------ |
| `enabled` | bool | `true` | 是否启用保护 |
| `rules` | []string | `[code, link, placeholder, xml]` | 保护规则列表 |

##### ruby — 注音标注

| 字段 | 类型 | 说明 |
| ---------------- | -------- | ---------------------------------------------------- |
| `enabled` | bool | 是否启用注音 |
| `preserve_kinds` | []string | 保留的注音类型：`phonetic` / `semantic` / `creative` |

注音失败后的重试不再配置在 `ruby` 段，改用计划级 `execution.ruby_retry`（见下文）。

##### postprocess — 后处理

| 字段 | 类型 | 默认值 | 说明 |
| ------------- | ---- | ------ | -------------- |
| `enabled` | bool | `true` | 是否启用后处理 |
| `trim_spaces` | bool | `true` | 去除多余空格 |

##### repair — 响应修复

| 字段 | 类型 | 默认值 | 说明 |
| ----------------------- | ---- | ------ | -------------------- |
| `enabled` | bool | `true` | 是否启用修复 |
| `json_structural` | bool | `true` | JSON 结构修复 |
| `schema_aliases` | bool | `true` | Schema 别名修复 |
| `placeholder_normalize` | bool | `true` | 占位符规范化 |
| `prompt_upgrade` | bool | `true` | 提示词升级 |

::: tip 部分段缺失的重试
部分段缺失不再在修复配置里单独开关，改由翻译流水线的池化缩批重试统一处理（按 `fallback_shrink` 缩小批次只重译缺失段），见 [流水线与原理 · 批量与并发](/zh/guide/pipeline#批量与并发)。
:::

##### inline_term_extraction — 内联术语提取

原策略级 `glossary.bootstrap:` 已移除，内联术语提取改为翻译轮次的高级选项：

| 字段 | 类型 | 默认值 | 说明 |
| -------------------------- | ------ | --------------- | --------------------------------- |
| `enabled` | bool | `false` | 是否在本翻译轮的响应中同时抽取新术语 |
| `max_terms_per_1000_words` | float | `3` | 每千源文字词的术语上限系数（CJK 按字、其他按词），须大于 0 |
| `min_source_len` | int | `2` | 最小源文本长度（按 rune 计），须 ≥ 1 |
| `conflict_strategy` | string | `rewrite-local` | 冲突策略：`rewrite-local` / `off` |

省略整段或 `enabled: false` 时只使用 `glossary` 段指定的术语表、不抽取新术语。

##### context — 上下文窗口

| 字段 | 类型 | 默认值 | 说明 |
| --------- | ---- | ------ | ------------------------------------------------------------- |
| `enabled` | bool | `true` | 是否启用上下文 |
| `before` | int | `1` | 前文段落数 |
| `after` | int | `1` | 后文段落数 |
| `max_chars` | int | `0` | 上下文最大字符数（按 rune 计，超限截断并补省略号）；`0` 不限制 |

##### qa — 质量检测

CLI 配置仅接受默认值（`enabled: false`）。规则质检、AI 裁决、语义质检等能力以 Web 端执行配置为准。

### execution — 执行计划

组合后端、模板和策略为翻译流水线。

| 字段 | 类型 | 说明 |
| --------- | ------ | ------------------------------------------------------------------------------------------ |
| `profile` | string | 计划级翻译策略名称（引用 `translation_profiles` 的 key）。**未写时使用内置默认策略；写了但名称不存在会报错** |
| `rounds` | array | 执行轮次列表，按顺序执行 |
| `ruby_retry` | object | 可选。注音重试：`enabled`（写入即视为 true）、`backend`（引用 `backends` key）、`max_attempts`（默认 `1`） |

CLI 轮次仅支持 `translate` / `extract` / `revise`；Web 执行计划另支持 `adjudicate`（质量裁决）、`correct`（本地改写）、`semantic_qa`（语义质检），详见 [流水线与原理](/zh/guide/pipeline#规则质检与-ai-质量裁决)。

| 字段 | 类型 | 说明 |
| --------- | ------ | ---------------------------------------- |
| `mode` | string | `translate` / `extract` / `revise` |
| `backend` | string | 使用的 AI 后端（引用 `backends` 的 key） |
| `translate` | object | `mode=translate` 时必填，见下表 |
| `extract` | object | `mode=extract` 时必填，见下表 |
| `revise` | object | `mode=revise` 时必填，见下表 |

::: tip 轮次子段未写的字段自动补默认值
轮次子段本体必须存在（`mode: translate` 必须配 `translate:`），但段内字段可以省略，省略时按下表补默认——不再要求全部显式写齐。

| 轮次 | 未写字段的默认值 |
| -------- | -------------------------------------------------------------- |
| `translate` | `batch_size: 1` · `concurrency: 4` · `fallback_shrink: 0.5` · `retry: 3 次 / 2000ms / 抖动` |
| `extract` | `batch_size: 20` · `concurrency: 2` · `max_terms_per_1000_chars: 25` · `min_source_len: 2` |
| `revise` | `batch_size: 10` · `concurrency: 1` · `segment_scope: with_issues` |
:::

**translate 子配置：**

| 字段 | 类型 | 说明 |
| --------------------- | ------ | ----------------------------------------------------------------------------- |
| `prompt` | string | 翻译提示词模板 key |
| `batch_size` | int | 待译段落数上限（不计上下文段） |
| `max_words_per_batch` | int | 每批字词数上限（计入上下文段） |
| `concurrency` | int | 并发数 |
| `fallback_shrink` | float | 池缩比系数，合法域 (0, 1]。`1.0` = 多池同尺寸重切；`(0,1)` = 每池缩小。`0` 非法（会被拒绝）；池数量 = `retry.max_attempts + 1`，见 [流水线与原理](/zh/guide/pipeline#批量与并发) |
| `inline_term_extraction` | object | 可选。本轮翻译响应中顺带抽术语，见 [inline_term_extraction](#inline-term-extraction-—-内联术语提取) |
| `retry.*` | — | `max_attempts`（决定池深 = `max_attempts + 1`）/ `backoff_ms` / `jitter` |

::: tip 策略引用已移到计划级
翻译策略不在每轮 translate 内引用，改由 `execution.profile`（计划级）统一指定，translate 与 revise 轮共用该策略的 protect/ruby/repair/QA 行为预设。`--profile` flag 也改为覆盖此计划级值（不再改写每轮 translate 的 profile）。
:::

**extract 子配置：**

| 字段 | 类型 | 说明 |
| -------------------------- | ------ | ---------------------- |
| `template` | string | 术语抽取提示词模板 key |
| `batch_size` | int | 批处理大小 |
| `max_words_per_batch` | int | 每批最大词数 |
| `concurrency` | int | 并发数 |
| `max_terms_per_1000_chars` | float | 每千字符术语上限系数 |
| `min_source_len` | int | 术语最短源文长度 |
| `retry.*` | — | 重试配置 |

**revise 子配置：**

revise 轮必须配合 `--revision-input` 提供审阅输入文件（`schema_version: 1` 的 YAML/JSON，含段落索引、原文、现有译文与语义问题），详见 [配置契约 · CLI 修订输入](#cli-修订输入-revision-input)。

| 字段 | 类型 | 说明 |
| --------------------- | -------- | ------------------------------------------------------------------------------------------ |
| `batch_size` | int | 待修订段落数上限；0 不限制，与 `max_words_per_batch` 至少填一项 |
| `max_words_per_batch` | int | 每批字词数上限；0 不限制，与 `batch_size` 至少填一项 |
| `segment_scope` | string | `with_issues`（默认）：修订存在 `pending` 语义 issue 的段；`with_issue_codes`: 仅修订含 `issue_codes` 声明 code 的段 |
| `issue_codes` | []string | 仅 `with_issue_codes` 时填，≥1 项，且 ⊆ 语义白名单（`calque`/`term_fidelity`/`naturalness`/`mistranslation`/`omission`/`addition`/`grammar`/`register`） |
| `retry.*` | — | 重试配置 |

revise 轮的系统提示词内置不可覆盖，protect/ruby 等行为复用计划级 `execution.profile`，无 `fallback_shrink`（不缩批）。

#### CLI 修订输入（--revision-input）

审阅输入是独立的数据文件，不进行环境变量展开；`schema_version: 1` 必填，未知字段、重复键、空段落列表、重复或缺失的索引均报错：

```yaml
schema_version: 1
segments:
  - index: 0
    source: hello world
    target: 错误译文
    issues:
      - code: mistranslation
        message: 请恢复问候语的原意。
        snippet: 错误译文
```

- `index` 是源文件解析后的从零开始段落索引（纯文本按空行分段）；`source` 必须与该段原文**精确相同**（含空白），防止把过期审阅应用到已变化的文件
- `issues` 至少一条，`message` 必填；问题代码白名单与 `revise.issue_codes` 相同
- 每次执行只处理一个输入文件：`linguaflow translate -i source.txt -o revised.txt --revision-input review.yaml`
- 混合 translate/extract/revise 时，已列入审阅的段落不会被 translate 重译；所有轮次结束仍有待处理修订时判定失败，不输出文件

### glossary — 术语表

| 字段 | 类型 | 默认值 | 说明 |
| -------- | ------ | ---------------- | ------------------ |
| `enabled` | bool | `false` | 是否启用术语表 |
| `path` | string | `./glossary.csv` | 术语表文件路径 |
| `save` | bool | `true` | 是否保存提取的术语 |

文档中的路径相对**配置文件目录**解析；`--glossary-path` 相对当前工作目录解析并强制启用术语表。

### log — 日志

| 字段 | 类型 | 默认值 | 说明 |
| ------ | ------ | ------ | --------------------------------------- |
| `level` | string | `info` | 日志级别：`debug`/`info`/`warn`/`error` |
| `format` | string | `text` | 日志格式：`text`/`json` |

## 服务器部署文档（serve / local）

`linguaflow init --kind server` 生成 `server.yaml`（`kind: server` / `version: 1`）。`serve` 与 `local` 共用这份契约，通过 `--config` 或 `LINGUAFLOW_SERVER_CONFIG` 选择；没有提供任何来源时使用模式内置默认值。

启动前可用只读命令预检（不创建目录/密钥、不连接数据库、不绑定端口）：

```bash
linguaflow config check  --config server.yaml            # 校验通过与否
linguaflow config explain --config server.yaml --mode serve # 逐字段列出最终值与来源
```

### 必需输入（serve）

服务器模式**没有开箱即用的默认密钥**，以下三项缺一即拒绝启动：

| 输入 | 提供方式 |
| --- | --- |
| JWT 签名密钥（≥32 字节） | `LINGUAFLOW_JWT_SECRET` 或 `LINGUAFLOW_JWT_SECRET_FILE` |
| 凭据加密密钥 | `LINGUAFLOW_CREDENTIALS_MASTER_KEY`（32 字节随机值的 Base64）**或** `LINGUAFLOW_CREDENTIALS_KEYRING_FILE`（多密钥 JSON 文件），二选一、互斥 |
| 初始管理员 | 部署文档 `bootstrap.admin`（username/email）+ `LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD`（或 `_FILE`）；也可用 `linguaflow admin initialize` 命令提供 |

生成密钥材料使用离线命令 `linguaflow secrets generate`（详见 [CLI 命令参考](/zh/guide/cli#secrets-命令)）。**凭据加密密钥是数据库里所有 AI 密钥的解密钥匙**：一旦更换或丢失，已保存的密钥无法解密；数据库备份必须与对应密钥共同保存。

本地模式没有这些要求：首次启动自动在数据目录生成 `instance-secret` 与 `credentials-keyring.json` 并复用，也不接受 `bootstrap.admin` 与 `server.database.*` 配置。

### 配置结构

```yaml
kind: server
version: 1
server:
  host: 0.0.0.0
  port: 8080
  data_dir: ./data
  service_name: linguaflow
  auto_migrate: true
  serve_ui: true
  # JWT secret 请通过 LINGUAFLOW_JWT_SECRET 或其 _FILE 形式提供（≥32 字节）
  jwt_issuer: linguaflow
  jwt_expiry: 15m
  refresh_token_expiry: 720h
  shutdown_timeout: 10s
  revision_retention: 2160h
  database:
    driver: sqlite
    # PostgreSQL 需要提供 DSN
  cors:
    allowed_origins: []   # 显式 [] = 禁止跨域；不写此字段默认 ["*"]
log:
  level: info
  format: text
bootstrap:
  registration_enabled: false   # 首次初始化时是否开放注册，默认关闭
  admin:                        # 仅首次初始化生效，完成后可删除
    username: admin
    email: admin@example.com
    # 密码通过 LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD 或 _FILE 提供
```

### server.* 字段详解

仅在 `linguaflow serve` / `local` 模式下生效。每个字段都有同名大写蛇形的 `LINGUAFLOW_*` 环境变量。

| 字段 | 类型 | 默认值 | 说明 |
| ---------------------- | -------- | ------------------------------- | ------------------------------------------ |
| `host` | string | serve `0.0.0.0` / local `127.0.0.1` | 监听地址；local 非回环需 `--allow-network` |
| `port` | int | serve `8080` / local `18080` | 监听端口 |
| `service_name` | string | `linguaflow` | 服务名称 |
| `data_dir` | string | serve `./data` / local 用户配置目录 | 数据目录 |
| `auto_migrate` | bool | `true` | 自动数据库迁移 |
| `serve_ui` | bool | `true` | 是否提供嵌入式 Web UI，可用 `--no-ui` 关闭 |
| `jwt_secret` | string | **serve 必填** / local 自动生成 | JWT 签名密钥；建议走 `LINGUAFLOW_JWT_SECRET[_FILE]` |
| `jwt_issuer` | string | `linguaflow` | JWT 签发者 |
| `jwt_expiry` | duration | `15m` | JWT 过期时间 |
| `refresh_token_expiry` | duration | `720h`（30 天） | 刷新令牌过期时间 |
| `shutdown_timeout` | duration | `10s` | 优雅关闭超时 |
| `revision_retention` | duration | `2160h`（90 天） | [搜索替换](/zh/guide/review#搜索替换)历史保留时长（撤销窗口） |

##### server.cors — 跨域

| 字段 | 类型 | 默认值 | 说明 |
| ----------------- | -------- | ------- | ------------------------------------------------------------ |
| `allowed_origins` | []string | `["*"]` | 允许的来源；**显式写 `[]` 表示禁止一切跨域**（浏览器仅同源可用），不再回填 `*` |

##### server.database — 数据库（仅 serve）

| 字段 | 类型 | 默认值（SQLite / PostgreSQL） | 说明 |
| ------------------- | -------- | ----------------------------- | --------------------------------------------------------------------- |
| `driver` | string | `sqlite` | 驱动：`sqlite` \| `postgres` |
| `dsn` | string | - | 连接串；`postgres` 必填（支持 `_FILE`），`sqlite` 为空时使用 `data_dir/linguaflow.db` |
| `max_open_conns` | int | `0` / `25` | 最大打开连接数 |
| `max_idle_conns` | int | `2` / `5` | 最大空闲连接数 |
| `conn_max_lifetime` | duration | `0` / `30m` | 连接最大寿命 |

::: warning 本地模式不支持 PostgreSQL
`linguaflow local` 始终使用 SQLite，且部署文档里出现任何 `server.database.*` 字段都会报错。多用户需求请使用 `linguaflow serve` 并切换到 PostgreSQL。
:::

##### server.workers — 执行器与队列

| 字段 | 类型 | 默认值 | 说明 |
| ------------------------------------ | ---- | --------------- | -------------------------------------------------------- |
| `workers.translation.count` | int | `max(CPU 核数, 2)` | 翻译执行器数量 |
| `workers.translation.queue_capacity` | int | 翻译执行器数 × 4 | 翻译任务队列容量 |
| `workers.sync.count` | int | `max(CPU 核数, 2)` | 术语同步执行器数量 |
| `workers.sync.queue_capacity` | int | 同步执行器数 × 8 | 术语同步任务队列容量 |

##### server.pipeline — 流水线准入

控制作业执行时资源入线的节奏与内存保险丝。任务执行时每个资源按其源文本字节获得一个「工作配额」权重，两项准入上限**按任务分别生效**（并发任务各占一份配额）：

| 字段 | 类型 | 默认值 | 说明 |
| ------------------------ | ---- | ------ | ------------------------------------------------------------------------------------------------------------- |
| `max_inflight_weight_mb` | int | `32` | 每任务同时在途的源文本字节配额（MB）。这是**并发节流**而非内存上限：超出配额的资源排队等待，超预算的单资源在配额空置时独跑放行，不会饥饿 |
| `max_inflight_resources` | int | `8` | 每任务同时在途的资源数上限，兜住每资源的句柄开销 |
| `rss_limit_mb` | int | `0` | **进程级** RSS（常驻内存）保险丝，单位 MB；`0` = 关闭。启用后双水位：内存达到 85% 暂停所有任务的新资源准入（在途请求继续），回落到 70% 恢复 |

::: tip 触发 RSS 保险丝时任务不会失败
内存高水位只**暂停新资源准入**——任务保持「运行中」、资源排队，在途请求照常完成，不改变任务状态。大文件（长篇电子书等）或高并发作业占用内存偏高时再考虑启用；日常本地翻译默认关闭即可。
:::

##### server.preview — 单段预览（试译 / 修订）

| 字段 | 类型 | 默认值 | 说明 |
| ------------------ | -------- | ------ | ---------------------------------------- |
| `max_concurrency` | int | `2` | 单段预览（试译/修订共用）的并发上限 |
| `timeout` | duration | `5m` | 单次预览执行超时 |
| `apply_token_ttl` | duration | `15m` | 预览「应用译文」令牌有效期 |

##### server.quick_translate — 即时翻译

控制首页「即时翻译」（同步单段在线翻译）的并发与超时。译文纯临时、不落库。功能说明见 [即时翻译](/zh/guide/quick-translate)。

| 字段 | 类型 | 默认值 | 说明 |
| ----------------- | -------- | ------ | ------------------------------------------------------------------------------------------------------------- |
| `max_concurrency` | int | `2` | 单用户同时进行的即时翻译并发上限（per-actor 信号量）；**全局并发 = 此值 × 4** |
| `timeout` | duration | `5m` | 单次即时翻译执行超时;`timeout > max_timeout` 会在启动时报错(不静默钳制) |
| `max_timeout` | duration | `30m` | `timeout` 的硬上限（管理员安全阀，防误配占满并发槽位） |

::: tip 并发如何受约束
单用户并发受 `max_concurrency` 限制；整个实例的全局即时翻译并发受 `max_concurrency × 4` 限制。提示「并发已满」时稍后重试即可。
:::

##### server.sse — 实时事件流

控制作业实时事件（SSE）的回放与历史事件存储行为，影响 [任务详情 · 事件日志](/zh/guide/projects#任务详情-执行概览与事件日志) 的首字节延迟与历史补进。

| 字段 | 类型 | 默认值 | 说明 |
| ---------------------- | ---- | ----------------- | ------------------------------------------------------------------------------------------------- |
| `ring_buffer_capacity` | int | `256` | 每个 job 的内存 ring buffer 容量，用于 SSE 重连窗口补进 |
| `replay_batch_size` | int | `200` | SSE 首次历史回放（补进）从 DB 拉取的每批事件数 |
| `max_replay_events` | int | 省略时取 `ring_buffer_capacity × 2` | SSE 单次连接历史回放总量上限;**省略该键**才取 `ring_buffer_capacity × 2`,一旦显式提供就必须为正数(写 `0` 或负数会报错) |

::: info 新连接只补最近窗口
一个全新的 SSE 连接（无 `Last-Event-ID`）只会从「最近 `max_replay_events` 条」开始补进，而非从 `seq 0` 全量回放。更早的历史由前端通过 [REST 历史端点](/zh/api/#_9-任务事件历史-分页) 分页拉取，保证大作业也能秒开。
:::

##### server.credentials — 凭据加密

| 字段 | 类型 | 默认值 | 说明 |
| --------------- | ------ | ------------------------------- | ------------------------------------------------------------ |
| `master_key` | string | 无（serve 必填其一） | 标准 Base64 的 32 字节随机密钥；建议走 `LINGUAFLOW_CREDENTIALS_MASTER_KEY[_FILE]` |
| `keyring_file` | path | local 自动生成 `credentials-keyring.json` | 多密钥 JSON keyring 文件路径，用于轮换场景 |

两种来源**互斥**，同时提供会报错。单主密钥模式不在数据卷生成 keyring 文件；keyring 文件格式为 `{"version":1,"active_key_id":"...","keys":{"...":"BASE64 密钥"}}`，必须保持私有权限。轮换流程见 [管理员后台 · 凭据加密密钥轮换](/zh/guide/admin#凭据加密密钥轮换)。

##### server.storage — 对象存储

控制项目文件（源文件、译文、导出产物）的对象存储。产品侧说明见 [存储管理](/zh/guide/storage)。所有字段都有同名大写蛇形环境变量（如 `LINGUAFLOW_STORAGE_MAINTENANCE`；`backends` 数组可整段以 YAML 文本形式传给 `LINGUAFLOW_STORAGE_BACKENDS`）。

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 是否启用对象存储部署能力（`LINGUAFLOW_STORAGE_ENABLED`）。关闭时存储政策只能是「仅站点托管」，相关操作返回 `storage_deployment_disabled` |
| `maintenance` | bool | `false` | 存储维护态：`true` 时所有存储写入被拒绝（返回 `storage_maintenance`）；启动时存在未完成的旧版迁移任务也会自动进入维护态 |
| `backends` | []object | 未配置时隐含一个本地后端 | 存储后端列表，见下表 |
| `default_site_space` | string | 未配置 backends 时为 `local` | 默认站点空间，必须指向某个已配置后端 |
| `work_dir` | path | `<data_dir>/tmp` | 传输与处理用的工作目录 |
| `cache_dir` | path | `<data_dir>/cache` | 内容缓存目录 |
| `initialization.capacity_bytes` | 配额 | 未设置(=不限额) | 首次初始化 / 迁移时回填的**新站点空间默认配额**;`null` 表示不限额,正整数上限 `9007199254740991`(2^53−1)。**离线迁移**在 serve 模式下要求两个配额都已显式配置(见 [存储管理 · 旧数据迁移](/zh/guide/storage)) |
| `initialization.logical_limit_bytes` | 配额 | 未设置(=不限额) | 首次初始化 / 迁移时回填的**每个用户或组织的逻辑配额**(跨项目合计);取值同上。**离线迁移**在 serve 模式下要求两个配额都已显式配置 |
| `disk.minimum_free` | string | `1%` | 本地磁盘**保护余量**:可用空间低于它即拒绝新写入(返回 `storage_disk_insufficient`)。可写字节数,也可写小于 100 的百分数(含小数,如 `1%`、`2.5%`) |
| `limits.*` | object | 见下 | 单文件 / 临时 / 输出 / 解压展开、归档条目数、分段数、元数据、缓存与并发上限 |
| `network.allowed_hosts` / `network.allowed_cidrs` | []string | 空 | 出站存储网络的允许主机 / CIDR |
| `intent_ttl` | duration | `24h` | 存储操作意图的有效期 |
| `metadata_timeout` / `idle_timeout` | duration | `30s` / `30s` | 元数据请求 / 空闲连接超时 |
| `transfer_timeout` | duration | `15m` | 单次传输超时 |
| `retry_max_attempts` / `retry_window` / `retry_base_delay` / `retry_max_delay` | int / duration | `8` / `30m` / `1s` / `1m` | 存储操作重试策略 |
| `signed_url_ttl` / `signed_url_max_ttl` | duration | `5m` / `15m` | 签名 URL 有效期与硬上限(须满足 `signed_url_ttl ≤ signed_url_max_ttl ≤ deletion_grace`) |
| `source_retention` / `deletion_grace` | duration | `30d` / `24h` | 源文件保留期 / 删除宽限期 |
| `reconcile_interval` / `reconcile_batch_size` | duration / int | `1m` / `100` | 存储对账节奏与批量大小 |

`backends` 每项字段：

| 字段 | 说明 |
| --- | --- |
| `id` | 后端标识，必须唯一非空 |
| `driver` | `local`（本地磁盘）或 `s3`（S3 兼容对象存储） |
| `root` | **local 必填**：对象根目录；携带远程凭据字段会报错 |
| `endpoint` / `bucket` / `region` / `access_key_id` / `secret_access_key` | **s3 必填**；endpoint 仅接受 HTTPS |
| `prefix` / `path_style` / `session_token` | s3 可选：对象前缀、路径风格寻址、临时会话令牌 |
| `access_key_id` 等密钥字段 | 建议用环境变量注入，避免写进部署文档 |

`limits` 详细字段:

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `max_file_bytes` | `100 MiB` | 单文件上限 |
| `max_temp_bytes` | `4 GiB` | 临时容量上限(须 ≥ 单文件 / 输出 / 元数据上限) |
| `max_output_bytes` | `512 MiB` | 导出产物上限 |
| `max_expanded_bytes` | `1 GiB` | 归档解压展开上限 |
| `max_archive_entries` | `10000` | 归档条目数上限 |
| `max_segments` | `100000` | 分段上限 |
| `max_metadata_bytes` | `64 MiB` | 元数据 / 快照上限 |
| `max_concurrency` | `2` | 存储操作并发上限 |
| `max_cache_bytes` | `0`(不启用) | 内容缓存字节上限 |

```yaml
server:
  storage:
    backends:
      - id: local
        driver: local
        root: ./data/objects
      - id: s3-main
        driver: s3
        endpoint: https://s3.example.com
        bucket: linguaflow
        region: us-east-1
        prefix: prod/
        path_style: false
        # access_key_id / secret_access_key 建议经环境变量提供
    default_site_space: local
```

::: warning 目录与容量约束
- 各 local 后端的根目录之间**不允许相互重叠**,启动时校验(`work_dir` / `cache_dir` 默认落在数据目录下,不参与该重叠校验)
- `limits.*`(单文件 / 临时 / 输出 / 解压展开、元数据、缓存字节上限,归档条目数、分段数、并发数)与 `network.allowed_hosts` / `allowed_cidrs`、`retry_*`、`signed_url_ttl` 等时长数量项必须为正且相互满足大小关系(如 `signed_url_max_ttl ≤ deletion_grace`),否则启动报错——用 `linguaflow config check` 可在部署前验证

完整键清单以 `linguaflow init --kind server` 生成的模板与 [CLI · config explain](/zh/guide/cli#config-命令) 输出为准。
:::

::: warning 已废弃的 `limits.capacity_bytes`
旧键 `server.storage.limits.capacity_bytes`(及环境变量 `LINGUAFLOW_STORAGE_LIMITS_CAPACITY_BYTES`)已被移除:新站点空间的初始化配额改用 `server.storage.initialization.capacity_bytes`,已有空间配额改由界面或 `PUT /storage/spaces/{spaceId}/quota` 调整。沿用旧键会直接启动失败。
:::

##### bootstrap — 首次初始化（顶层）

| 字段 | 类型 | 默认值 | 说明 |
| ------------------------ | ------ | ------------------ | ------------------------------------------------------------ |
| `registration_enabled` | bool | `false` | 初始化时是否开放注册；之后可在管理端设置页随时调整（数据库中的值优先） |
| `admin.username` | string | serve 首次启动必填 | 初始管理员用户名 |
| `admin.email` | string | serve 首次启动必填 | 初始管理员邮箱 |
| `admin.password` | string | serve 首次启动必填 | 初始管理员密码；建议走 `LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD[_FILE]` |

::: warning 仅首次初始化生效
这些值只在空实例首次初始化时写入数据库，之后数据库中的账号与注册政策才是权威——**初始化完成后请从部署文档和环境变量中删除管理员输入**。已初始化的实例再次启动不会覆盖、也不会重置管理员密码。本地模式不接受 `bootstrap.admin`。
:::

## 命令行参数

### 全局参数

| 参数 | 短写 | 类型 | 默认值 | 说明 |
| -------------- | ---- | ------ | ------- | ----------------------------------- |
| `--config` `-c` | | string | `""` | 配置文档路径（serve/local 为部署文档；translate 为翻译配置） |
| `--log-level` | | string | `info` | 日志级别 |
| `--log-format` | | string | `text` | 日志格式 |
| `--verbose` `-v` | | bool | `false` | 等同于 `--log-level=debug`（显式 `--log-level` 优先） |
| `--progress` | | string | `auto` | 进度反馈：`auto`/`bar`/`log`/`none` |

### serve 子命令

| 参数 | 类型 | 默认值 | 说明 |
| ---------------- | ------ | ------- | ------------------------------------------ |
| `--config` `-c` | string | `""` | 部署文档路径（或 `LINGUAFLOW_SERVER_CONFIG`） |
| `--host` | string | — | 覆盖 `server.host` |
| `--port` | int | — | 覆盖 `server.port` |
| `--data-dir` | string | — | 覆盖 `server.data_dir` |
| `--auto-migrate` | bool | `true` | 覆盖 `server.auto_migrate` |
| `--no-ui` | bool | `false` | 关闭嵌入式 Web UI，仅提供 API |

::: warning 必需输入不受参数影响
JWT secret 与凭据加密密钥没有命令行参数入口，只能通过环境变量（或部署文档）提供。旧版本的 `--jwt-secret`、`--cors-origins` 参数已移除。
:::

### local 子命令

| 参数 | 类型 | 默认值 | 说明 |
| --------------- | ------ | ----------- | ------------------------------- |
| `--host` | string | `127.0.0.1` | 监听地址 |
| `--port` | int | `18080` | 监听端口（0=随机） |
| `--data-dir` | string | `""` | 数据目录 |
| `--no-browser` | bool | `false` | 不自动打开浏览器 |
| `--allow-network` | bool | `false` | 允许非回环监听（改 `--host` 时必须显式提供） |

### translate 子命令

| 参数 | 短写 | 类型 | 默认值 | 说明 |
| ----------------- | ---- | -------- | ------ | ---------------------------------------------------- |
| `--input` | `-i` | []string | 必填 | 输入文件或目录（可多个） |
| `--output` `-o` | | string | 必填 | 输出文件或目录 |
| `--config` `-c` | | string | `""` | 翻译配置路径（或 `LINGUAFLOW_TRANSLATION_CONFIG`） |
| `--from` | | string | `""` | 源语言（覆盖配置文件） |
| `--to` | | string | `""` | 目标语言（覆盖配置文件） |
| `--glossary-path` | | string | `""` | 术语表路径，设置后强制启用 |
| `--bootstrap` | | string | `""` | 术语提取模式：`off`/`pre`/`inline`。`inline` 开启**所有**翻译轮次的内联提取并移除独立抽取轮次；`pre` 改用独立抽取轮次（缺则自动补一个置于最前）并关闭内联提取；`off` 两者都关；非 `off` 同时启用术语表；留空沿用配置 |
| `--profile` | | string | `""` | 执行配置名称（覆盖计划级 `execution.profile`；引用 `translation_profiles` key，未命中报错） |
| `--prompt` | | string | `""` | 提示词模板名称（`translation_prompt_templates` key） |
| `--revision-input` | | string | `""` | revise 轮必填：`schema_version: 1` 审阅输入文件（YAML/JSON） |

### init 子命令

| 参数 | 短写 | 类型 | 默认值 | 说明 |
| --------- | ---- | ------ | ------------------------ | ------------------------------- |
| `--kind` | | string | `translation` | 生成文档类型：`translation` / `server` |
| `--path` | `-p` | string | `linguaflow.yaml` 或 `server.yaml` | 输出文件路径 |
| `--force` | | bool | `false` | 覆盖已有文件 |

`--kind server` 只生成 `server.yaml`；`--kind translation` 生成主配置及 `prompts/`、`profiles/` 引用文件。

## 下一步

- [翻译配置 · 使用](/zh/guide/translation-config) · [翻译配置 · 参考](/zh/guide/translation-config-reference)
- [CLI 命令参考](/zh/guide/cli) — 子命令与 flags（含 `secrets` / `config` / `admin` 维护命令）
- [安装部署](/zh/guide/installation) — 二进制 / Docker
