# 配置

LinguaFlow 支持通过命令行参数、环境变量和配置文件进行配置。

## 配置优先级

```text
命令行参数 > 环境变量 > 配置文件 > 内置默认值
```

## 环境变量

### 系统环境变量

| 变量名                      | 描述                              | 使用场景                   |
| --------------------------- | --------------------------------- | -------------------------- |
| `OPENAI_API_KEY`            | OpenAI API 密钥                   | 默认后端配置引用           |
| `LINGUAFLOW_ADMIN_USERNAME` | 管理员用户名                      | 服务启动时自动创建         |
| `LINGUAFLOW_ADMIN_PASSWORD` | 管理员密码                        | 配合用户名使用             |
| `LINGUAFLOW_SERVE_UI`       | 是否提供 Web UI（`true`/`false`） | 服务器模式控制前端资源挂载 |
| `LINGUAFLOW_JWT_SECRET`     | JWT 签名密钥，生产环境务必修改    | 服务器模式认证             |
| `LINGUAFLOW_CORS_ORIGINS`   | 允许的跨域来源（逗号分隔）        | 服务器模式跨域配置         |

### 数据库环境变量（serve 模式）

服务器模式（`linguaflow serve`）的数据库通过环境变量配置，不读取 YAML 配置文件中的 `server` 段。本地模式（`linguaflow local`）始终使用 SQLite，忽略以下变量。

| 变量名                                  | 描述                                                                        | 默认值                     |
| --------------------------------------- | --------------------------------------------------------------------------- | -------------------------- |
| `LINGUAFLOW_DATABASE_DRIVER`            | 数据库驱动：`sqlite` \| `postgres`                                          | `sqlite`                   |
| `LINGUAFLOW_DATABASE_DSN`               | 数据库连接串。`postgres` 必填；`sqlite` 为空时使用 `data_dir/linguaflow.db` | -                          |
| `LINGUAFLOW_DATABASE_MAX_OPEN_CONNS`    | 最大打开连接数（`database/sql` 连接池）                                     | `sqlite=0` / `postgres=25` |
| `LINGUAFLOW_DATABASE_MAX_IDLE_CONNS`    | 最大空闲连接数                                                              | `sqlite=2` / `postgres=5`  |
| `LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME` | 连接最大寿命，Go duration 格式（如 `30m`）                                  | `postgres=30m`             |

示例：

```bash
# PostgreSQL
export LINGUAFLOW_DATABASE_DRIVER=postgres
export LINGUAFLOW_DATABASE_DSN='postgres://user:pass@localhost:5432/linguaflow?sslmode=disable'
linguaflow serve
```

::: tip 自动迁移与并发安全
启用 `auto_migrate`（默认开启）时，PostgreSQL 实例会通过 `pg_advisory_lock` 串行化 schema 迁移，多个 LinguaFlow 实例可同时连接同一数据库而不会在启动阶段产生冲突。
:::

### 配置文件中的环境变量引用

配置文件中的所有值都支持 `${VAR_NAME}` 和 `${VAR_NAME:-default}` 语法，在解析前自动展开：

```yaml
backends:
  my-backend:
    options:
      api_key: ${OPENAI_API_KEY}
      base_url: ${CUSTOM_API_URL:-https://api.openai.com/v1}
```

## 配置文件

使用 `linguaflow init` 命令生成配置文件模板：

```bash
linguaflow init
```

将在当前目录生成 `linguaflow.yaml` 配置文件及配套目录结构：

```text
<项目根目录>/
├── linguaflow.yaml                      # 主配置文件
├── prompts/
│   ├── default_translation.tmpl         # 翻译提示词模板
│   └── default_bootstrap.tmpl           # 术语提取提示词模板
└── profiles/
    └── default.yaml                     # 默认翻译策略
```

可通过 `--path` 指定输出路径，`--force` 覆盖已有文件：

```bash
linguaflow init --path my-config.yaml --force
```

### 配置文件结构

```yaml
# 配置文件版本
version: 1

# 语言设置
source_lang: auto
target_lang: zh

# AI 后端配置（map 结构，key 为后端名称）
backends:
  openai-default:
    type: openai
    enabled: true
    rate_limit_per_minute: 0
    options:
      api_key: ${OPENAI_API_KEY}
      base_url: https://api.openai.com/v1
      model: gpt-4o-mini
      temperature: 0.2
      max_tokens: 0
      timeout: 60s
      response_format: json_schema
      # stream: false  # 兼容网关仅接受 stream:true 时开启

# 翻译提示词模板（map 结构，key 为模板名称）
translation_prompt_templates:
  通用提示词:
    # content: |  # 内联内容
    #   ...
    file: prompts/default_translation.tmpl # 或引用外部文件（与 content 二选一）

# 术语抽取提示词模板
bootstrap_prompt_templates:
  通用术语抽取:
    file: prompts/default_bootstrap.tmpl

# 执行配置 / 翻译策略（map 结构，key 为策略名称，CLI 不支持翻译质量检测）
translation_profiles:
  通用策略:
    # 或引用外部文件（与以下内联字段二选一）
    # file: profiles/default.yaml
    protect:
      enabled: true
      rules: [code, link, placeholder, xml]
    ruby:
      enabled: false
      preserve_kinds: []
    postprocess:
      enabled: true
      trim_spaces: true
    repair:
      enabled: true
      json_structural: true
      schema_aliases: true
      partial: true
      partial_threshold: 0.5
      placeholder_normalize: true
      prompt_upgrade: true
    bootstrap:
      enabled: false
      max_terms_per_1000_chars: 3.0
      min_source_len: 2
      inline_conflict_strategy: rewrite-local
    context:
      enabled: true
      before: 1
      after: 1
      max_chars: 0

# 执行计划（CLI：translate / extract；Web 端另支持 adjudicate）
execution:
  rounds:
    - mode: translate
      backend: openai-default
      translate:
        prompt: 通用提示词
        profile: 通用策略
        batch_size: 1
        max_words_per_batch: 0
        concurrency: 4
        fallback_shrink: 0.5
        retry:
          max_attempts: 3
          backoff_ms: 2000
          jitter: true
    # - mode: extract
    #   backend: openai-default
    #   extract:
    #     template: 通用术语抽取
    #     batch_size: 20
    #     concurrency: 2

# 术语表配置
glossary:
  enabled: false
  path: ./glossary.csv
  save: true

# 翻译记忆配置
translation_memory:
  enabled: false
  driver: sqlite
  dsn: ./.linguaflow/tm.db

# 插件配置
plugins:
  enabled: false
  scripts: []

# 输出配置
output:
  mode: overwrite
  preserve_extension: true
  incremental: false

# 日志配置
log:
  level: info
  format: text

# 服务器配置（仅 serve 模式）
server:
  host: 0.0.0.0
  port: 8080
  mode: server
  service_name: linguaflow
  data_dir: ./data
  auto_migrate: true
  serve_ui: true
  jwt_secret: dev-insecure-secret-change-me
  jwt_issuer: linguaflow
  jwt_expiry: 15m
  refresh_token_expiry: 720h
  shutdown_timeout: 10s
  cors:
    allowed_origins: ["*"]
  registration:
    enabled: true
    auto_admin: true
```

### 配置项详解

#### version — 配置版本

配置文件格式版本号，当前为 `1`。

#### source_lang / target_lang — 语言设置

| 字段          | 类型   | 默认值 | 说明                        |
| ------------- | ------ | ------ | --------------------------- |
| `source_lang` | string | `auto` | 源语言，`auto` 表示自动检测 |
| `target_lang` | string | `zh`   | 目标语言                    |

#### backends — AI 后端

配置 AI 翻译服务，使用 map 结构，key 为后端名称。

| 字段                    | 类型   | 说明                                        |
| ----------------------- | ------ | ------------------------------------------- |
| `type`                  | string | 后端类型：`openai` / `anthropic` / `google` |
| `enabled`               | bool   | 是否启用，默认 `true`                       |
| `rate_limit_per_minute` | int    | 每分钟请求限制，`0` 表示不限制              |
| `options`               | map    | 后端特定选项，见下表                        |

**options 通用字段：**

| 字段          | 类型   | 说明                                                                                                 |
| ------------- | ------ | ---------------------------------------------------------------------------------------------------- |
| `api_key`     | string | API 密钥                                                                                             |
| `base_url`    | string | 自定义 API 端点                                                                                      |
| `model`       | string | 使用的模型                                                                                           |
| `temperature` | float  | 生成温度                                                                                             |
| `max_tokens`  | int    | 最大 token 数，`0` 表示自动                                                                          |
| `timeout`     | string | 请求超时时间，如 `60s`                                                                               |
| `stream`      | bool   | 是否以流式发起请求（内部累积为完整响应），默认 `false`。`true` 适用于只接受 `stream:true` 的兼容网关 |

**各后端默认模型：**

| 后端类型    | 默认模型            |
| ----------- | ------------------- |
| `openai`    | `gpt-4o-mini`       |
| `anthropic` | `claude-sonnet-4-5` |
| `google`    | `gemini-2.5-flash`  |

#### translation_prompt_templates — 翻译提示词模板

定义翻译指令模板，使用 map 结构，key 为模板名称。

| 字段      | 类型   | 说明                                    |
| --------- | ------ | --------------------------------------- |
| `content` | string | 模板内容（内联）                        |
| `file`    | string | 外部模板文件路径（与 `content` 二选一） |

#### bootstrap_prompt_templates — 术语抽取模板

定义术语抽取指令模板，使用 map 结构，key 为模板名称。与翻译提示词模板结构相同。

::: tip 文件引用

- 仅支持相对路径，不允许绝对路径
- 路径必须在配置文件目录内（禁止 `../` 遍历）
- 内联内容优先于文件引用

:::

::: info Web 端额外资源
术语精简提示词（Prune）与质量裁决（`adjudicate`）等能力主要在 **Web 服务端** 的资源模型中配置；CLI 配置文件以 `translate` / `extract` 轮次为主。
:::

#### translation_profiles — 执行配置（翻译策略）

控制翻译行为，使用 map 结构，key 为策略名称。可通过 `file` 字段引用外部文件，或内联配置以下字段：

##### split — 分段

| 字段        | 类型   | 默认值      | 说明           |
| ----------- | ------ | ----------- | -------------- |
| `enabled`   | bool   | `true`      | 是否启用分段   |
| `strategy`  | string | `paragraph` | 分段策略       |
| `max_chars` | int    | `1200`      | 每段最大字符数 |

##### protect — 内容保护

| 字段      | 类型     | 默认值                           | 说明         |
| --------- | -------- | -------------------------------- | ------------ |
| `enabled` | bool     | `true`                           | 是否启用保护 |
| `rules`   | []string | `[code, link, placeholder, xml]` | 保护规则列表 |

##### ruby — 注音标注

| 字段             | 类型     | 说明                                                 |
| ---------------- | -------- | ---------------------------------------------------- |
| `enabled`        | bool     | 是否启用注音                                         |
| `retry_backend`  | string   | 注音失败时的重试后端                                 |
| `preserve_kinds` | []string | 保留的注音类型：`phonetic` / `semantic` / `creative` |

##### postprocess — 后处理

| 字段          | 类型 | 默认值 | 说明           |
| ------------- | ---- | ------ | -------------- |
| `enabled`     | bool | `true` | 是否启用后处理 |
| `trim_spaces` | bool | `true` | 去除多余空格   |

##### repair — 响应修复

| 字段                    | 类型  | 默认值 | 说明            |
| ----------------------- | ----- | ------ | --------------- |
| `enabled`               | bool  | `true` | 是否启用修复    |
| `json_structural`       | bool  | `true` | JSON 结构修复   |
| `schema_aliases`        | bool  | `true` | Schema 别名修复 |
| `partial`               | bool  | `true` | 部分响应修复    |
| `partial_threshold`     | float | `0.5`  | 部分修复阈值    |
| `placeholder_normalize` | bool  | `true` | 占位符规范化    |
| `prompt_upgrade`        | bool  | `true` | 提示词升级      |

##### bootstrap — 术语提取

| 字段                       | 类型   | 默认值          | 说明                              |
| -------------------------- | ------ | --------------- | --------------------------------- |
| `enabled`                  | bool   | `false`         | 是否启用内联术语提取              |
| `max_terms_per_1000_chars` | float  | `3.0`           | 每千字符最大术语数                |
| `min_source_len`           | int    | `2`             | 最小源文本长度                    |
| `inline_conflict_strategy` | string | `rewrite-local` | 冲突策略：`rewrite-local` / `off` |

##### context — 上下文窗口

| 字段        | 类型 | 默认值 | 说明                         |
| ----------- | ---- | ------ | ---------------------------- |
| `enabled`   | bool | `true` | 是否启用上下文               |
| `before`    | int  | `1`    | 前文段落数                   |
| `after`     | int  | `1`    | 后文段落数                   |
| `max_chars` | int  | `0`    | 上下文最大字符数，`0` 不限制 |

#### execution — 执行计划

组合后端、模板和配置为翻译流水线。CLI 配置中仅含 `rounds` 列表。

##### rounds — 执行轮次

CLI 支持 `translate`（翻译）与 `extract`（术语提取）。Web 执行计划模板另支持 `adjudicate`（质量裁决），详见 [高级功能 · AI 质量裁决](/zh/guide/advanced#ai-质量裁决)。

| 字段        | 类型   | 说明                                     |
| ----------- | ------ | ---------------------------------------- |
| `mode`      | string | `translate` 或 `extract`                 |
| `backend`   | string | 使用的 AI 后端（引用 `backends` 的 key） |
| `translate` | object | `mode=translate` 时必填，见下表          |
| `extract`   | object | `mode=extract` 时必填，见下表            |

**translate 子配置：**

| 字段                  | 类型   | 说明                                     |
| --------------------- | ------ | ---------------------------------------- |
| `prompt`              | string | 翻译提示词模板 key                       |
| `profile`             | string | 执行配置 / 策略 key                      |
| `batch_size`          | int    | 批处理大小                               |
| `max_words_per_batch` | int    | 每批最大词数                             |
| `concurrency`         | int    | 并发数                                   |
| `fallback_shrink`     | float  | 回退收缩比例                             |
| `retry.*`             | —      | `max_attempts` / `backoff_ms` / `jitter` |

**extract 子配置：**

| 字段                       | 类型   | 说明                   |
| -------------------------- | ------ | ---------------------- |
| `template`                 | string | 术语抽取提示词模板 key |
| `batch_size`               | int    | 批处理大小             |
| `max_words_per_batch`      | int    | 每批最大词数           |
| `concurrency`              | int    | 并发数                 |
| `max_terms_per_1000_chars` | float  | 每千字符术语上限系数   |
| `min_source_len`           | int    | 术语最短源文长度       |
| `retry.*`                  | —      | 重试配置               |

::: tip Web 端质量裁决
在 Web 服务端创建执行计划时，可增加 `adjudicate` 轮次：调用 AI 对 `source_residual`、`length_ratio` 等软规则误报降噪。提示词内置，无需选择模板。
:::

#### glossary — 术语表

| 字段      | 类型   | 默认值           | 说明               |
| --------- | ------ | ---------------- | ------------------ |
| `enabled` | bool   | `false`          | 是否启用术语表     |
| `path`    | string | `./glossary.csv` | 术语表文件路径     |
| `save`    | bool   | `true`           | 是否保存提取的术语 |

#### translation_memory — 翻译记忆

| 字段      | 类型   | 默认值                | 说明             |
| --------- | ------ | --------------------- | ---------------- |
| `enabled` | bool   | `false`               | 是否启用翻译记忆 |
| `driver`  | string | `sqlite`              | 存储驱动         |
| `dsn`     | string | `./.linguaflow/tm.db` | 数据源连接字符串 |

#### plugins — 插件

| 字段      | 类型     | 默认值  | 说明         |
| --------- | -------- | ------- | ------------ |
| `enabled` | bool     | `false` | 是否启用插件 |
| `scripts` | []string | `[]`    | 插件脚本列表 |

#### output — 输出

| 字段                 | 类型   | 默认值      | 说明                  |
| -------------------- | ------ | ----------- | --------------------- |
| `mode`               | string | `overwrite` | 输出模式：`overwrite` |
| `preserve_extension` | bool   | `true`      | 保留原始文件扩展名    |
| `incremental`        | bool   | `false`     | 增量输出              |

#### log — 日志

| 字段     | 类型   | 默认值 | 说明                                    |
| -------- | ------ | ------ | --------------------------------------- |
| `level`  | string | `info` | 日志级别：`debug`/`info`/`warn`/`error` |
| `format` | string | `text` | 日志格式：`text`/`json`                 |

#### server — 服务器

仅在 `linguaflow serve` 模式下生效。

| 字段                   | 类型     | 默认值                          | 说明                                       |
| ---------------------- | -------- | ------------------------------- | ------------------------------------------ |
| `host`                 | string   | `0.0.0.0`                       | 监听地址                                   |
| `port`                 | int      | `8080`                          | 监听端口                                   |
| `mode`                 | string   | `server`                        | 运行模式：`server`/`local`                 |
| `service_name`         | string   | `linguaflow`                    | 服务名称                                   |
| `data_dir`             | string   | `./data`                        | 数据目录                                   |
| `auto_migrate`         | bool     | `true`                          | 自动数据库迁移                             |
| `jwt_secret`           | string   | `dev-insecure-secret-change-me` | JWT 签名密钥                               |
| `jwt_issuer`           | string   | `linguaflow`                    | JWT 签发者                                 |
| `jwt_expiry`           | duration | `15m`                           | JWT 过期时间                               |
| `refresh_token_expiry` | duration | `720h`（30 天）                 | 刷新令牌过期时间                           |
| `shutdown_timeout`     | duration | `10s`                           | 优雅关闭超时                               |
| `serve_ui`             | bool     | `true`                          | 是否提供嵌入式 Web UI，可用 `--no-ui` 关闭 |

##### server.cors — 跨域

| 字段              | 类型     | 默认值  | 说明       |
| ----------------- | -------- | ------- | ---------- |
| `allowed_origins` | []string | `["*"]` | 允许的来源 |

##### server.registration — 注册

| 字段         | 类型 | 默认值 | 说明                       |
| ------------ | ---- | ------ | -------------------------- |
| `enabled`    | bool | `true` | 是否开放用户注册           |
| `auto_admin` | bool | `true` | 首个注册用户自动成为管理员 |

##### server.database — 数据库

服务器模式数据库通过环境变量配置（见上方「数据库环境变量」小节），本地模式始终使用 SQLite。

| 字段                | 类型     | 默认值（SQLite / PostgreSQL） | 说明                                                                  |
| ------------------- | -------- | ----------------------------- | --------------------------------------------------------------------- |
| `driver`            | string   | `sqlite`                      | 驱动：`sqlite` \| `postgres`                                          |
| `dsn`               | string   | -                             | 连接串；`postgres` 必填，`sqlite` 为空时使用 `data_dir/linguaflow.db` |
| `max_open_conns`    | int      | `0` / `25`                    | 最大打开连接数                                                        |
| `max_idle_conns`    | int      | `2` / `5`                     | 最大空闲连接数                                                        |
| `conn_max_lifetime` | duration | `0` / `30m`                   | 连接最大寿命                                                          |

::: warning 本地模式不支持 PostgreSQL
`linguaflow local` 始终使用 SQLite，以确保单文件、零依赖运行。多用户需求请使用 `linguaflow serve` 并切换到 PostgreSQL。
:::

## 命令行参数

### 全局参数

| 参数           | 短写 | 类型   | 默认值  | 说明                                |
| -------------- | ---- | ------ | ------- | ----------------------------------- |
| `--config`     | `-c` | string | `""`    | 配置文件路径                        |
| `--log-level`  |      | string | `info`  | 日志级别                            |
| `--log-format` |      | string | `text`  | 日志格式                            |
| `--verbose`    | `-v` | bool   | `false` | 等同于 `--log-level=debug`          |
| `--progress`   |      | string | `auto`  | 进度反馈：`auto`/`bar`/`log`/`none` |

### serve 子命令

| 参数             | 类型   | 默认值  | 说明                                       |
| ---------------- | ------ | ------- | ------------------------------------------ |
| `--host`         | string | `""`    | 覆盖 `server.host`                         |
| `--port`         | int    | `0`     | 覆盖 `server.port`                         |
| `--data-dir`     | string | `""`    | 覆盖 `server.data_dir`                     |
| `--auto-migrate` | bool   | `true`  | 覆盖 `server.auto_migrate`                 |
| `--no-ui`        | bool   | `false` | 关闭嵌入式 Web UI，仅提供 API              |
| `--jwt-secret`   | string | `""`    | 覆盖 `LINGUAFLOW_JWT_SECRET`               |
| `--cors-origins` | string | `""`    | 覆盖 `LINGUAFLOW_CORS_ORIGINS`（逗号分隔） |

### local 子命令

| 参数           | 类型   | 默认值      | 说明               |
| -------------- | ------ | ----------- | ------------------ |
| `--host`       | string | `127.0.0.1` | 监听地址           |
| `--port`       | int    | `18080`     | 监听端口（0=随机） |
| `--data-dir`   | string | `""`        | 数据目录           |
| `--no-browser` | bool   | `false`     | 不自动打开浏览器   |

### translate 子命令

| 参数              | 短写 | 类型     | 默认值 | 说明                                                 |
| ----------------- | ---- | -------- | ------ | ---------------------------------------------------- |
| `--input`         | `-i` | []string | `nil`  | 输入文件或目录（可多个）                             |
| `--output`        | `-o` | string   | `""`   | 输出文件或目录                                       |
| `--from`          |      | string   | `""`   | 源语言（覆盖配置文件）                               |
| `--to`            |      | string   | `""`   | 目标语言（覆盖配置文件）                             |
| `--glossary-path` |      | string   | `""`   | 术语表路径，设置后强制启用                           |
| `--bootstrap`     |      | string   | `""`   | 术语提取模式：`off`/`pre`/`inline`                   |
| `--profile`       |      | string   | `""`   | 执行配置名称（`translation_profiles` key）           |
| `--prompt`        |      | string   | `""`   | 提示词模板名称（`translation_prompt_templates` key） |

### init 子命令

| 参数      | 短写 | 类型   | 默认值            | 说明         |
| --------- | ---- | ------ | ----------------- | ------------ |
| `--path`  | `-p` | string | `linguaflow.yaml` | 输出文件路径 |
| `--force` |      | bool   | `false`           | 覆盖已有文件 |

## 下一步

- 阅读 [翻译配置](/zh/guide/translation-config) 了解翻译配置的详细使用
- 阅读 [安装部署](/zh/guide/installation) 了解部署配置
