# CLI 命令参考

LinguaFlow 命令行工具支持启动服务、翻译文件，以及部署所需的密钥生成、配置预检与管理员维护；另有独立二进制 `linguaflow-migrate`，负责版本升级的离线数据迁移与旧存储数据导入。

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
| `linguaflow admin`     | 管理员维护（`initialize` / `create` / `recover` / `credentials reencrypt` / `authentication-recovery`） |
| `linguaflow-migrate`   | 离线迁移工具（独立二进制：`list` / `inspect` / `upgrade plan|rehearse|apply` / `storage plan|apply` / `status` / `resume` / `verify` / `rollback`，见下文） |
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
| `--to` | | string | `""` | 目标语言代码 (留空则用配置文件) |
| `--from` | | string | `""` | 源语言代码 (留空则用配置文件，可配 `auto` 自动检测) |
| `--glossary-path` | | string | `""` | 术语表 CSV 路径 |
| `--bootstrap` | | string | `""` | 术语提取模式：`off` \| `pre` \| `inline`。`inline` 开启所有翻译轮次的内联提取并移除独立抽取轮次；`pre` 改用独立抽取轮次（缺则自动补一个）；非 `off` 同时启用术语表 |
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

```bash [内联术语提取]
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
| `admin initialize` | 对空实例显式执行首次初始化（创建管理员与注册政策；`--username` / `--email`（可选） / `--password-file` 或 `--password-stdin`） |
| `admin create` | 为已初始化实例追加一名管理员（参数同上；不含 `initialize`） |
| `admin recover` | 找回管理员：将已有账户恢复为活跃管理员、重置密码并吊销其刷新令牌（保留原邮箱；`--email` 可选） |
| `admin authentication-recovery` | 受部署权限保护的登录恢复命令组，见 [下文](#admin-authentication-recovery-命令组) |
| `admin credentials reencrypt` | 凭据密钥轮换后重加密存量数据（含 LLM 凭据与存储授权；配合 keyring 轮换使用） |

```bash
# 首次初始化（与 serve 的 bootstrap.admin 输入等效，二选一即可）
linguaflow admin initialize --config ./server.yaml \
  --username admin --email admin@example.com --password-file ./admin-password

# 追加管理员（email 现在是可选参数）
linguaflow admin create --config ./server.yaml \
  --username another-admin --password-file ./admin-password

# 找回管理员（密码从标准输入读入）
linguaflow admin recover --config ./server.yaml --username admin --password-stdin

# 密钥轮换后重加密（详见管理员后台文档）
linguaflow admin credentials reencrypt --config ./server.yaml
```

密码输入保留空格、只移除末尾换行，不接受明文密码命令行参数。已初始化实例再次执行 `initialize` 只验证状态，不会覆盖政策、角色或密码。

### admin authentication-recovery 命令组

当所有管理员都无法登录（密码丢失、唯一管理员账户异常）时的兜底恢复入口，直接访问数据库运行，不受 Web 登录影响。前提是**能在部署机上执行命令**——它受部署权限（服务器与数据目录的物理控制权）保护：

| 子命令 | 用途 |
| --- | --- |
| `admin authentication-recovery issue` | 为活跃管理员签发**短期、单次**的本地登录恢复凭据（写入私有文件，拒绝覆盖） |
| `admin authentication-recovery restore-local-login` | 消费恢复凭据：恢复全站密码登录与用户名定位、关闭本地注册，并重置指定活跃管理员的密码 |
| `admin authentication-recovery revoke` | 撤销未使用的恢复凭据 |

```bash
# 1. 为活跃管理员签发恢复凭据（输出到私有文件）
linguaflow admin authentication-recovery issue --config ./server.yaml \
  --username admin --output ./recovery-token

# 2. 用凭据执行恢复：新密码从文件或标准输入读入
linguaflow admin authentication-recovery restore-local-login \
  --config ./server.yaml --token-file ./recovery-token --password-file ./new-password

# 不再需要时撤销凭据
linguaflow admin authentication-recovery revoke --config ./server.yaml \
  --token-file ./recovery-token
```

::: warning 签发即敏感操作
`issue` 会签发能重置管理员密码的凭据，凭据文件与恢复过程请按密钥同等对待；签发与消费都会记入审计日志（`admin.authentication_recovery.*`）。`restore-local-login` 会**关闭本地注册**并恢复密码登录，属于改变全站登录政策的操作，执行前确认这正是你要的。功能背景见 [账号与认证 · 管理员登录恢复](/zh/guide/authentication#管理员登录恢复)。
:::

各命令均支持 `--data-dir`（覆盖部署数据目录）与 `--auto-migrate`（默认 `true`，执行前准备数据库 schema）。

## linguaflow-migrate 命令

版本升级的**离线迁移工具**（独立二进制，有意不挂在 `linguaflow` 命令树上）。覆盖两类场景：

- **版本升级**（`upgrade`）— 把 v0.15.0 的数据库与账号数据离线转换到当前版本
- **旧存储导入**（`storage`）— 把旧版文件存储（`data/jobs` 目录）迁入当前的对象存储体系，产品背景见 [存储管理](/zh/guide/storage)

::: warning 迁移必须离线执行
所有写操作（`upgrade rehearse|apply`、`resume`、`rollback`、`storage apply`）都要求 `--offline`——先停掉全部 LinguaFlow 服务进程（含 worker），确认没有写入者后再执行。中断的迁移不会自动恢复：数据库带未完成迁移状态时**服务会拒绝启动**（报错提示保留数据库并恢复迁移操作），需用 `status` 确认状态后以 `resume --offline` 续跑，直到迁移完成。
:::

::: warning v0.13 / v0.14 历史数据不在支持范围
本工具只支持从 **v0.15.0** 升级。v0.13.0 / v0.14 的历史数据请使用**发布对应版本的原版迁移程序**做一次性迁移——不带账号库格式标记的旧数据库（含 v0.13.0）启动时会被直接拒绝，当前版本不提供旧账户自动转换。
:::

::: warning v0.16 遗留操作目录只能识别、不能接管
迁移操作文件的格式已升至 v2。此前版本（v0.16）创建的迁移操作目录（v1 格式）只会被 `status` 只读识别并显示当年的构建号，`resume` / `verify` / `rollback` 一律拒绝执行——请改用**当年执行该操作的构建**对应的迁移二进制完成或回退那批操作，新工具不会接手中途的旧操作。
:::

### 获取与编译

::: warning 迁移工具不随 Release 与 Docker 镜像分发
GitHub Release 产物与 Docker 镜像目前**只包含主程序** `linguaflow`。`linguaflow-migrate` 需要从**要升级到的那个版本**（tag 或 commit）的源码自行编译——Go 静态编译，可在本地交叉编译后上传服务器。
:::

```bash
# 推荐：Taskfile 构建，自动注入版本与提交信息（在仓库根目录执行）
# 为 Linux amd64 服务器交叉编译，按服务器实际架构调整 GOARCH
task -t backend/Taskfile.yml migrate:build GOOS=linux GOARCH=amd64
# 产物：backend/bin/linguaflow-migrate

# 不用 Task 时的等价手工构建
cd backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o linguaflow-migrate ./cmd/linguaflow-migrate
```

两个约束：

- **同一个构建跑全程** — `upgrade plan` 会把工具的构建身份冻结进计划，`apply` / `resume` / `verify` 都会校验执行二进制与之一致。编译一次、上传同一份文件，中途不要重新编译替换。
- **工具与主程序同版本** — 迁移完成后，用**同一版本源码**编译的主程序（或该版本的官方 Release 主程序）启动服务。

### 迁移前准备

工具**强制显式指定数据来源**，不会静默读取默认路径。三者选一：

| 输入方式 | 参数 | 说明 |
| --- | --- | --- |
| 部署文档 | `--config server.yaml` | 文档内需含 `server.data_dir`（serve 模式还需 `server.database.driver` / `server.database.dsn`） |
| 数据目录 | `--data-dir <路径>` | local 模式或未写进部署文档时直接指定 |
| 环境变量 | `LINGUAFLOW_DATABASE_DSN` 等 | 也可用 `LINGUAFLOW_DATA_DIR`、`LINGUAFLOW_DATABASE_DRIVER`、`LINGUAFLOW_DATABASE_DSN_FILE` |

同时用 `--mode serve|local` 声明实例模式，必须与实际实例一致。迁移工具只读取数据库位置相关的输入，不会加载 JWT、密钥环等凭据。

升级策略由 `--strategy inplace|clone` 决定，缺省自动推断：不指定目标参数时为 `inplace`，给出目标参数时自动切到 `clone`；显式指定的策略与目标参数冲突会被拒绝。

| 策略 | 行为 | 目标参数 |
| --- | --- | --- |
| `inplace`（默认） | 直接在源库上转换并发布：SQLite 沿用原数据库路径（发布中断可续跑恢复），PostgreSQL 原地升级 schema | 无 |
| `clone` | 在独立目标上转换，源数据保持不动：SQLite 发布到一个**完全不存在的新数据目录**，PostgreSQL 写入独立空目标库 | SQLite 用 `--target-data-dir`；PostgreSQL 用 `--target-config` |

数据库保护由 `--database-backup auto|external` 控制，缺省自动：SQLite 与 clone 策略固定为 `auto`（工具自行留存恢复材料）；**PostgreSQL 原地升级默认 `external`**，要求你先自行备份数据库，并在首次 `apply` 时用 `--backup-reference <备份标识>` 声明备份出处（如备份文件路径或备份系统中的任务号），续跑时沿用计划中的记录。PostgreSQL 的 clone 升级与演练还依赖与主版本匹配的 `pg_dump` / `pg_restore`，不在 PATH 时用环境变量 `LINGUAFLOW_MIGRATION_POSTGRES_BIN` 指定包含它们的目录。演练（`rehearse`）用 `--rehearsal-config` 指定独立演练库（仅 PostgreSQL）。

### 命令速查

| 命令 | 说明 |
| --- | --- |
| `list` | 显示支持的迁移路线与验收状态（只读，无需数据库输入） |
| `inspect` | 只读探测源数据库格式，确认工具能否识别 |
| `upgrade plan` | 冻结升级计划：源指纹、账号映射、目标 schema、工具构建摘要，写入操作目录 |
| `upgrade rehearse` | 在隔离演练目标上实际转换一遍并生成验证报告（需 `--offline`） |
| `upgrade apply` | 按计划正式转换目标并发布（需 `--offline`） |
| `status` | 查看操作目录中计划、日志与报告的状态（只读） |
| `resume` | 中断后续跑（需 `--offline`） |
| `verify` | 只读复核转换结果 |
| `rollback` | 保留恢复材料并回退（需 `--offline`） |
| `storage plan` | 清点旧版 `data/jobs` 文件并生成导入计划 |
| `storage apply` | 执行旧存储导入（需 `--offline`） |

执行写操作前，工具会先获取**源库迁移锁**（SQLite 是数据库文件旁的 `<数据库名>.migration.lock` 文件锁，PostgreSQL 是 advisory 锁），确认没有并发写入者。计划冻结后，`apply` / `resume` / `verify` 都会校验执行二进制与计划中的构建摘要一致，并核对操作目录中的保护收据（`protection-binding.json`）与基线证据（`baseline.json`）——生产操作请固定使用 `migrate:build` 构建出的同一个二进制，不要混用不同版本的工具，也不要手工改动操作目录里的任何文件；收据缺失或不匹配被拒绝时，按错误提示保留数据库与操作材料原样，用 `status` 排查。

### 版本升级示例

```bash
# 1. 只读探测源库，确认可识别
linguaflow-migrate inspect --mode serve --config server.yaml

# 2. 生成升级计划（冻结源、账号映射与目标）
#    给出 --target-data-dir 即选择 clone 策略（发布到全新目录）；
#    不给目标参数则默认 inplace 原地升级
linguaflow-migrate upgrade plan --mode serve --config server.yaml \
  --operation-dir /var/linguaflow/upgrade-op \
  --target-data-dir /var/linguaflow/new-data \
  --account-map ./accounts.json

# 3. 停服后先演练，检查验证报告
linguaflow-migrate upgrade rehearse --operation-dir /var/linguaflow/upgrade-op --offline

# 4. 正式转换并发布
linguaflow-migrate upgrade apply --operation-dir /var/linguaflow/upgrade-op --offline

# 5. 复核结果后，用新数据目录启动新版本
linguaflow-migrate verify --operation-dir /var/linguaflow/upgrade-op
```

账号映射（`--account-map`，可选）以用户 ID 为键，只允许调整 `username` / `email`，未给出的用户保持原值；密码、角色与数据归属不会被改动。不提供映射时按原值规范化全部账号。

### 旧存储导入示例

```bash
# 生成导入计划（默认读取 <data_dir>/jobs，可用 --legacy-root 覆盖）
linguaflow-migrate storage plan --mode serve --config server.yaml \
  --operation-dir /var/linguaflow/storage-op

# 停服后执行导入
linguaflow-migrate storage apply --operation-dir /var/linguaflow/storage-op --offline
```

::: info 配额与清单
旧存储导入会记录两层配额（见 [配置文件与环境变量 · 对象存储](/zh/guide/configuration#server-storage-—-对象存储)）；serve 模式要求两个配额都已显式配置。若此前已生成过迁移清单文件（ManifestVersion=1），可用 `storage plan --manifest <文件>` 接管继续。
:::

### 从源码运行（Taskfile）

开发环境下通常不直接调用二进制，而是通过 backend Taskfile 任务（`CLI_ARGS` 写法兼容 PowerShell 包装器）：

```bash
task -t backend/Taskfile.yml migrate:list
task -t backend/Taskfile.yml migrate:inspect 'CLI_ARGS=--mode serve --config /abs/server.yaml'
task -t backend/Taskfile.yml migrate:build      # 构建唯一离线工具二进制
task -t backend/Taskfile.yml migrate:plan 'CLI_ARGS=... '   # 等价于 upgrade plan
task -t backend/Taskfile.yml migrate:rehearse 'CLI_ARGS=--operation-dir /abs/op --offline'
task -t backend/Taskfile.yml migrate:apply 'CLI_ARGS=--operation-dir /abs/op --offline'
task -t backend/Taskfile.yml migrate:status 'CLI_ARGS=--operation-dir /abs/op'
task -t backend/Taskfile.yml migrate:resume 'CLI_ARGS=--operation-dir /abs/op --offline'
```

`CLI_ARGS` 中的文件路径必须使用绝对路径。生产操作固定使用 `migrate:build` 产物（`backend/bin/linguaflow-migrate`）。

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
