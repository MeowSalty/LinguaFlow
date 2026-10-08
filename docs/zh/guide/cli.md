# CLI 命令参考

LinguaFlow 命令行工具支持启动服务、翻译文件，以及部署所需的密钥生成、配置预检与管理员维护；另有独立二进制 `linguaflow-storage-migrate`（旧存储迁移与备份）和 `linguaflow-migrate`（v0.13.0 历史数据一次性迁移）。

::: tip 只想马上译一个文件？
先看 [快速开始 · CLI](/zh/guide/cli-quickstart)，本页为完整参数参考。配置文件字段见 [配置文件与环境变量](/zh/guide/configuration)。
:::

## 命令概览

| 命令 | 描述 |
| ---------------------- | --------------------------------------------- |
| `linguaflow`           | 默认显示帮助；双击运行时自动启动 `local` 模式 |
| `linguaflow local`     | 启动本地单用户模式（推荐个人使用）            |
| `linguaflow serve`     | 启动服务器模式（**预览**，需部署配置与密钥）  |
| `linguaflow translate` | 直接翻译文件或目录                            |
| `linguaflow init`      | 生成配置模板（`--kind translation` / `--kind server`） |
| `linguaflow config`    | 只读预检部署配置（`check` / `explain`）       |
| `linguaflow secrets`   | 离线生成密钥材料（`generate` / `keyring init` / `keyring rotate`） |
| `linguaflow admin`     | 管理员维护（`initialize` / `create` / `recover` / `credentials reencrypt`） |
| `linguaflow-storage-migrate` | 存储迁移与备份（独立工具，`inventory` / `dry-run` / `apply` / `resume` / `rollback` / `backup`） |
| `linguaflow-migrate`   | v0.13.0 历史数据一次性迁移（独立工具，`v013 postgres` / `v013 sqlite` / `v013 local`） |
| `linguaflow version`   | 显示版本信息                                  |

## 全局参数

以下参数适用于所有子命令：

| 参数 | 短写 | 类型 | 默认值 | 描述 |
| -------------- | ---- | ------ | -------- | ------------------------------------------------ |
| `--config` `-c` | | string | `""` | 配置文档路径（serve/local 为部署文档；translate 为翻译配置） |
| `--log-level` | | string | `"info"` | 日志级别：`debug` \| `info` \| `warn` \| `error` |
| `--log-format` | | string | `"text"` | 日志格式：`text` \| `json` |
| `--verbose` `-v` | | bool | `false` | 等同于 `--log-level=debug`（显式 `--log-level` 优先） |
| `--progress` | | string | `"auto"` | 进度反馈模式：`auto` \| `bar` \| `log` \| `none` |

进度模式说明：

- `auto`：TTY 环境使用进度条，非 TTY 使用日志
- `bar`：强制使用终端进度条
- `log`：强制使用周期日志（每 5 秒或每 10 个片段）
- `none`：静默模式

## translate 命令

直接在命令行中翻译文件，无需启动 Web 服务。

### 基本用法

```bash
linguaflow translate -i input.md -o output.md --to zh
```

### 参数说明

| 参数 | 短写 | 类型 | 默认值 | 描述 |
| ----------------- | ---- | -------- | ------ | --------------------------------------------------- |
| `--input` | `-i` | string[] | 必填 | 输入文件或目录路径，可传多个 |
| `--output` | `-o` | string | 必填 | 输出路径（单文件为文件路径，多文件/目录为目录路径） |
| `--config` | `-c` | string | `""` | 翻译配置路径（或 `LINGUAFLOW_TRANSLATION_CONFIG`） |
| `--to` | | string | `""` | 目标语言代码(留空则用配置文件) |
| `--from` | | string | `""` | 源语言代码(留空则用配置文件,可配 `auto` 自动检测) |
| `--glossary-path` | | string | `""` | 术语表 CSV 路径 |
| `--bootstrap` | | string | `""` | 术语自举模式：`off` \| `pre` \| `inline` |
| `--profile` | | string | `""` | 执行配置名称（覆盖计划级 `execution.profile`；引用 `translation_profiles` key，未命中报错） |
| `--prompt` | | string | `""` | 提示词模板名称（引用配置中 `translation_prompt_templates` 的 key） |
| `--revision-input` | | string | `""` | revise 轮必填：`schema_version: 1` 审阅输入文件（YAML/JSON），详见 [配置文件与环境变量 · CLI 修订输入](/zh/guide/configuration#cli-修订输入-revision-input) |

### 示例

::: code-group

```bash [翻译单个文件]
linguaflow translate -i README.md -o README_zh.md --to zh
```

```bash [翻译多个文件]
linguaflow translate -i docs.md notes.txt -o ./out --to zh
```

```bash [翻译整个目录]
linguaflow translate -i ./docs/en/ -o ./docs/zh/ --to zh
```

```bash [指定源语言]
linguaflow translate -i article.md -o article.ja --from en --to ja
```

```bash [使用术语表]
linguaflow translate -i docs.md -o out.md --to zh --glossary-path ./terms.csv
```

```bash [术语自举]
linguaflow translate -i docs.md -o out.md --to zh --bootstrap=inline
```

```bash [指定执行配置]
linguaflow translate -i docs.md -o out.md --to zh --profile technical
```

```bash [按审阅输入修订译文]
linguaflow translate -i docs.md -o docs.rev.md --to zh \
  --config linguaflow.yaml --revision-input review.yaml
```

:::

### 支持的文件格式

翻译命令支持以下文件格式：

- Markdown (`.md`, `.markdown`, `.mdx`)
- HTML (`.html`, `.htm`)
- DOCX (`.docx`)
- SRT 字幕 (`.srt`)
- VTT 字幕 (`.vtt`)
- ASS 字幕 (`.ass`)
- EPUB 电子书 (`.epub`)
- JSON (`.json`)
- YAML (`.yaml`, `.yml`)
- TOML (`.toml`)
- 纯文本 (`.txt`)
- XUnity Text (`.txt`，`key=value` 格式自动识别)

目录扫描时，不支持的文件会被自动跳过，并在翻译结束后输出统计摘要（成功/失败/跳过数量）。批量翻译如有失败文件，程序将以非零退出码退出。

## init 命令

生成配置模板及全部引用文件。

```bash
linguaflow init --kind translation          # 默认：翻译配置
linguaflow init --kind server               # 服务器部署文档
```

| 参数 | 短写 | 类型 | 默认值 | 描述 |
| --------- | ---- | ------ | ------------------- | ----------------------------- |
| `--kind` | | string | `translation` | 生成文档类型：`translation` / `server` |
| `--path` | `-p` | string | `linguaflow.yaml` / `server.yaml` | 输出文件路径 |
| `--force` | | bool | `false` | 如果文件已存在则覆盖 |

`--kind translation` 生成：

- `linguaflow.yaml` — 主配置文件（`kind: translation` / `version: 2`，含注释说明）
- `prompts/default_translation.tmpl` — 默认翻译提示词模板
- `prompts/default_bootstrap.tmpl` — 默认术语抽取提示词模板
- `profiles/default.yaml` — 默认执行配置（`schema_version: 1`）

`--kind server` 只生成单文件 `server.yaml` 部署文档（`kind: server` / `version: 1`）。init 只生成模板文件，不初始化数据库。

## local 命令

以单用户本地模式启动 LinguaFlow。

```bash
linguaflow local [flags]
```

| 参数 | 类型 | 默认值 | 描述 |
| --------------- | ------ | --------------------------------- | --------------------------------------------------- |
| `--host` | string | `"127.0.0.1"` | 监听地址（非回环地址必须同时提供 `--allow-network`） |
| `--port` | int | `18080` | 监听端口（设为 `0` 时自动选择随机端口） |
| `--data-dir` | string | 系统用户配置目录下的 `LinguaFlow` | 数据目录 |
| `--no-browser` | bool | `false` | 不自动打开浏览器 |
| `--allow-network` | bool | `false` | 允许非回环监听（可连接者拥有本地管理员权限） |

特性：

- 数据目录默认为系统用户配置目录下的 `LinguaFlow`（Windows 为 `%APPDATA%\LinguaFlow`）
- 端口占用时自动尝试后续最多 10 个端口
- 启动后自动打开浏览器访问 `http://<host>:<port>`
- 在 Windows 资源管理器中双击可执行文件时，自动以 `local` 模式启动
- 首次启动在数据目录自动生成 `instance-secret`（JWT 密钥）与 `credentials-keyring.json`（凭据加密 keyring），无需手工准备；请连同数据库一起备份

## serve 命令

启动服务器模式（**预览**）。多用户与权限等能力仍在完善，不建议用于生产关键业务；个人使用请优先 `local`。

服务器模式**必须**显式提供 JWT 签名密钥、凭据加密密钥，首次启动还需初始管理员输入，缺一即拒绝启动。部署配置通过 `--config` 选择，不会自动读取 `.env`：

```bash
linguaflow serve --config server.yaml
```

| 参数 | 类型 | 默认值 | 描述 |
| ---------------- | ------ | ----------- | ------------------------------------------ |
| `--config` `-c` | string | `""` | 部署文档路径（或 `LINGUAFLOW_SERVER_CONFIG`） |
| `--host` | string | — | 覆盖 `server.host` |
| `--port` | int | — | 覆盖 `server.port` |
| `--data-dir` | string | — | 覆盖 `server.data_dir` |
| `--auto-migrate` | bool | `true` | 覆盖 `server.auto_migrate` |
| `--no-ui` | bool | `false` | 关闭嵌入式 Web UI，仅提供 API |

默认提供嵌入式 Web UI。仅需 API 时：

```bash
linguaflow serve --config server.yaml --no-ui
# 或
LINGUAFLOW_SERVE_UI=false linguaflow serve --config server.yaml
```

完整启动步骤与必需环境变量见 [使用模式 · 启动服务器模式](/zh/guide/modes#启动服务器模式)；部署文档字段见 [配置文件与环境变量](/zh/guide/configuration)。

## config 命令

只读预检部署配置，解析文档、环境变量与 flags 的最终合并结果。**不创建目录或密钥、不连接数据库、不执行初始化、不绑定端口**，适合部署前验证：

```bash
linguaflow config check --config server.yaml
# 输入合法时输出：Configuration inputs are valid.

linguaflow config explain --config server.yaml --mode serve
# 逐字段列出 FIELD / VALUE / SOURCE / MODES / EFFECT（敏感值脱敏）
```

| 参数 | 类型 | 默认值 | 描述 |
| --------- | ------ | -------- | ------------------------------- |
| `--mode` | string | `serve` | 部署模式：`serve` / `local` |
| `--config` `-c` | string | `""` | 部署文档路径 |

注意：`bootstrap.*` 的输出只表示「初始化输入」，无法离线判断数据库是否已初始化或当前注册政策；这些只在真正启动时验证。

## secrets 命令

离线生成密钥材料，供 serve 部署的 JWT 签名与凭据加密使用。**只生成密钥，不加载部署配置、不连接数据库、不启动服务**。输出文件必须不存在（原子创建私有文件，不覆盖）。

```bash
# 生成 32 字节随机密钥（标准 Base64，可作 JWT secret / master key / 初始密码）
linguaflow secrets generate --output /private/linguaflow/jwt-secret
linguaflow secrets generate --stdout        # 打印到标准输出

# 生成多密钥 keyring 文件（轮换场景）
linguaflow secrets keyring init --output /private/linguaflow/credentials-keyring.json
linguaflow secrets keyring init --output ... --from-master-key-env   # 把现有 master key 收入 keyring
linguaflow secrets keyring rotate --input 旧.json --output 新.json    # 追加新 key 并切 active
```

| 子命令 | 说明 |
| --- | --- |
| `secrets generate` | 生成一个独立随机 32 字节密钥；`--output` 与 `--stdout` 二选一 |
| `secrets keyring init` | 生成新的 keyring JSON 文件；`--from-master-key-env` 可把 `LINGUAFLOW_CREDENTIALS_MASTER_KEY` 环境中的现有密钥保留为其中一把 |
| `secrets keyring rotate` | 基于已有 keyring 生成含新 active key 的新文件（不覆盖旧文件、不改数据库） |

::: warning 密钥管理守则
JWT secret 与凭据加密密钥必须各自独立生成，不要共用一个值；不要把密钥输出写入仓库、日志或共享终端记录。凭据加密密钥丢失后，数据库中已保存的 AI 密钥无法解密——请与数据库备份一同妥善保存。完整轮换流程见 [管理员后台 · 凭据加密密钥轮换](/zh/guide/admin#凭据加密密钥轮换)。
:::

## admin 命令

管理员维护命令，直接使用部署配置与数据库权限，HTTP 服务未启动时也可运行。

| 子命令 | 用途 |
| --- | --- |
| `admin initialize` | 对空实例显式执行首次初始化（创建管理员与注册政策；`--username` / `--email` / `--password-file` 或 `--password-stdin`） |
| `admin create` | 为已初始化实例追加一名管理员（参数同上；不含 `initialize`） |
| `admin recover` | 找回管理员：将已有账户恢复为活跃管理员、重置密码并吊销其刷新令牌 |
| `admin credentials reencrypt` | 凭据密钥轮换后重加密存量数据（含 LLM 凭据与存储授权；配合 keyring 轮换使用） |

```bash
# 首次初始化（与 serve 的 bootstrap.admin 输入等效，二选一即可）
linguaflow admin initialize --config ./server.yaml \
  --username admin --email admin@example.com --password-file ./admin-password

# 追加管理员
linguaflow admin create --config ./server.yaml \
  --username another-admin --email another@example.com --password-file ./admin-password

# 找回管理员（密码从标准输入读入）
linguaflow admin recover --config ./server.yaml --username admin --password-stdin

# 密钥轮换后重加密（详见管理员后台文档）
linguaflow admin credentials reencrypt --config ./server.yaml
```

密码输入保留空格、只移除末尾换行，不接受明文密码命令行参数。已初始化实例再次执行 `initialize` 只验证状态，不会覆盖政策、角色或密码。

## linguaflow-storage-migrate 命令

存储迁移与备份**独立工具**（与主程序一同分发，有意不挂在 `linguaflow` 命令树上），用于两类场景：

- **旧数据迁移** — 把旧版文件存储（默认 `data_dir/jobs`）迁入新的对象存储体系，产品背景见 [存储管理](/zh/guide/storage)
- **备份与恢复校验** — 对现有存储做离线一致性备份与恢复演练

```bash
# 只读清点：看看有多少旧文件要迁
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json inventory

# 演练：不写任何数据，输出迁移计划
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json dry-run

# 正式迁移（要求先备份，见下方 warning）
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json apply \
  --offline --backup-confirmed

# 中断后续跑 / 回滚
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json resume --offline --backup-confirmed
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json rollback --offline --backup-confirmed

# 离线一致性备份
linguaflow-storage-migrate --config server.yaml --mode serve --manifest ./storage-migration.json backup capture \
  --offline --destination /backup/linguaflow
```

| 持久参数 | 默认值 | 描述 |
| --- | --- | --- |
| `--config` | `""` | 部署文档路径 |
| `--mode` | `serve` | 部署模式：`serve` / `local` |
| `--manifest` | `""` | 迁移清单文件路径(工具生成与续跑依据);`inventory` / `dry-run` / `apply` / `resume` / `rollback` 与全部 `backup` 动作**必填** |
| `--legacy-root` | `<data_dir>/jobs` | 旧版文件存储根目录 |
| `--offline` | `false` | 离线操作标记；写操作必填 |
| `--backup-confirmed` | `false` | 确认已完成备份；写操作必填 |

| 子命令 | 说明 |
| --- | --- |
| `inventory` | 只读清点旧文件，生成清单 |
| `dry-run` | 只读演练，输出迁移计划但不落盘 |
| `apply` | 正式迁移（写操作，需 `--offline --backup-confirmed`） |
| `resume` | 从中断点续跑迁移 |
| `rollback` | 回滚迁移 |
| `backup manifest` / `backup capture` / `backup restore-check` | 生成备份清单 / 执行离线一致性备份（`--destination`，`--retention` 默认 30 天、上限 366 天）/ 只读校验恢复结果（要求目标部署处于维护态） |

::: warning 写操作前置条件
`apply` / `resume` / `rollback` 与所有 `backup` 动作都要求 `--offline`（服务停机、独占数据），且迁移写操作必须同时提供 `--backup-confirmed`。离线迁移要求默认站点空间是 `local` 驱动——先迁到本地空间，再在界面上把项目迁往 S3。`apply` 完成后按输出提示把后端的 legacy 根配置为清单中的 `LegacyRoot` 路径，再启动新服务。
:::

## linguaflow-migrate 命令

v0.13.0 历史数据**一次性迁移**的独立工具（与主程序一同分发），把 v0.13.0 的 PostgreSQL / serve、SQLite / serve、SQLite / local 三种形态的数据迁到当前版本：转换历史任务快照、实例初始化状态与旧配置，并把旧明文 provider key 转成加密凭据；用户 ID、密码、角色与数据归属保持不变。正常 `linguaflow` 主程序不包含历史转换入口，迁移工具也不启动 HTTP 服务。

::: warning 迁移前必读
- 运行前**停止旧服务**与所有连接该数据目录的进程；PostgreSQL 务必先有可恢复的备份，SQLite 会由工具在切换时完整保留原目录作为备份
- 默认**预演**（在数据库迁移锁与 SERIALIZABLE 事务内完成转换后回滚；预演会持锁，且可能消耗 PostgreSQL 序列值），加 `--apply` 才正式提交
- 已经迁移、混合版本或无法可靠解释的数据会被拒绝，不会重置账户或猜测历史值
- 不要把密码、DSN 或密钥内容写进命令参数，一律走环境变量或私有文件
:::

```bash
linguaflow-migrate v013 postgres [--apply] [--data-dir PATH] [--keyring-file PATH]
linguaflow-migrate v013 sqlite --mode serve|local --data-dir PATH [--apply] [--keyring-file PATH]
linguaflow-migrate v013 local --data-dir PATH [--apply]
```

| 参数 | 说明 |
| --- | --- |
| `--data-dir` | 数据目录（优先于 `LINGUAFLOW_DATA_DIR`，默认当前目录下 `data`） |
| `--mode` | 仅 SQLite 入口需要：`serve`（服务器模式库）/ `local`（本地模式库），不按数据库类型或路径猜测 |
| `--apply` | 正式提交；省略时只预演 |
| `--keyring-file` | 指定凭据 keyring 文件；与主密钥环境变量互斥 |

密钥来源（详见工具 README）：

- 提供 `LINGUAFLOW_CREDENTIALS_MASTER_KEY`（32 字节随机值的标准 Base64，或其 `_FILE` 变体）时迁移直接使用该主密钥，**迁移后启动服务必须提供同一密钥**
- 否则使用 keyring 文件（`--keyring-file` / `LINGUAFLOW_CREDENTIALS_KEYRING_FILE`，默认 `<data-dir>/credentials-keyring.json`；已有文件必须有效，绝不自动覆盖）
- JWT secret 可提供 `LINGUAFLOW_JWT_SECRET`（≥32 字节，或 `_FILE`），未提供时复用或创建 `<data-dir>/jwt-secret`

不读取 `.env` 或部署文档（迁移前应取消 `LINGUAFLOW_SERVER_CONFIG`）；PostgreSQL 连接通过 `LINGUAFLOW_DATABASE_DSN`（或 `_FILE`，二者互斥）提供。正式迁移成功后会输出启动所需的文件路径与变量名，按提示连同数据库备份妥善保管密钥。

通常不直接调用二进制，而是通过 backend Taskfile 任务（`CLI_ARGS=...` 写法兼容 PowerShell 包装器）：

```bash
task -t backend/Taskfile.yml migrate:build
task -t backend/Taskfile.yml migrate:v013 'CLI_ARGS=--data-dir data'
task -t backend/Taskfile.yml migrate:v013:apply 'CLI_ARGS=--data-dir data'
```

中断恢复、Windows 路径限制、备份与恢复规则等完整行为见工具自带文档 `backend/cmd/linguaflow-migrate/README.md`。

## version 命令

显示 LinguaFlow 版本信息。

```bash
linguaflow version
```

输出格式：

```text
linguaflow <版本号> (commit <提交哈希>) <系统>/<架构> <Go 版本>
```

## 配置文件

两类配置文档详见 [配置文件与环境变量](/zh/guide/configuration)。要点：

- **翻译配置**（`kind: translation` / `version: 2`）用于 `translate`；**部署文档**（`kind: server` / `version: 1`）用于 `serve` / `local`，两者不通用
- 优先级：`模式内置默认值 < 配置文件显式字段 < 环境变量 < 显式 flags`
- 配置只通过 `--config`（或对应环境变量）选择，不自动搜索目录、不读取 `.env`
- 严格校验：未知字段、重复键、null 值等直接报错，不会静默回退默认值

使用 `linguaflow init` 生成配置模板，详见 [配置文件与环境变量](/zh/guide/configuration)。

## 下一步

- [快速开始 · CLI](/zh/guide/cli-quickstart) — 最短命令行路径
- [配置文件与环境变量](/zh/guide/configuration) — 配置文件格式
- [项目管理](/zh/guide/projects) — Web 界面操作
