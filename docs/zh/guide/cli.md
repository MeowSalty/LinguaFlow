# CLI 参考

LinguaFlow 命令行工具支持启动服务、生成配置与直接翻译文件。

::: tip 只想马上译一个文件？
先看 [快速开始 · CLI](/zh/guide/cli-quickstart)，本页为完整参数参考。
:::

## 命令概览

| 命令                   | 描述                                          |
| ---------------------- | --------------------------------------------- |
| `linguaflow`           | 默认显示帮助；双击运行时自动启动 `local` 模式 |
| `linguaflow local`     | 启动本地单用户模式（推荐个人使用）            |
| `linguaflow serve`     | 启动服务器模式（**预览**，功能仍在完善）      |
| `linguaflow translate` | 直接翻译文件或目录                            |
| `linguaflow init`      | 生成配置文件                                  |
| `linguaflow version`   | 显示版本信息                                  |

## 全局参数

以下参数适用于所有子命令：

| 参数           | 短写 | 类型   | 默认值   | 描述                                             |
| -------------- | ---- | ------ | -------- | ------------------------------------------------ |
| `--config`     | `-c` | string | `""`     | 配置文件路径                                     |
| `--log-level`  |      | string | `"info"` | 日志级别：`debug` \| `info` \| `warn` \| `error` |
| `--log-format` |      | string | `"text"` | 日志格式：`text` \| `json`                       |
| `--verbose`    | `-v` | bool   | `false`  | 等同于 `--log-level=debug`                       |
| `--progress`   |      | string | `"auto"` | 进度反馈模式：`auto` \| `bar` \| `log` \| `none` |

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

| 参数              | 短写 | 类型     | 默认值   | 描述                                                               |
| ----------------- | ---- | -------- | -------- | ------------------------------------------------------------------ |
| `--input`         | `-i` | string[] | 必填     | 输入文件或目录路径，可传多个                                       |
| `--output`        | `-o` | string   | 必填     | 输出路径（单文件为文件路径，多文件/目录为目录路径）                |
| `--to`            |      | string   | `"zh"`   | 目标语言代码                                                       |
| `--from`          |      | string   | `"auto"` | 源语言代码（默认自动检测）                                         |
| `--glossary-path` |      | string   | `""`     | 术语表 CSV 路径                                                    |
| `--bootstrap`     |      | string   | `""`     | 术语自举模式：`off` \| `pre` \| `inline`                           |
| `--profile`       |      | string   | `""`     | 执行配置名称（引用配置中 `translation_profiles` 的 key）           |
| `--prompt`        |      | string   | `""`     | 提示词模板名称（引用配置中 `translation_prompt_templates` 的 key） |

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

在当前目录生成 `linguaflow.yaml` 配置文件模板。

```bash
linguaflow init [flags]
```

| 参数      | 短写 | 类型   | 默认值              | 描述                 |
| --------- | ---- | ------ | ------------------- | -------------------- |
| `--path`  | `-p` | string | `"linguaflow.yaml"` | 目标配置文件路径     |
| `--force` |      | bool   | `false`             | 如果文件已存在则覆盖 |

执行后会生成以下内容：

- `linguaflow.yaml` — 主配置文件（含注释说明）
- `prompts/default_translation.tmpl` — 默认翻译提示词模板
- `prompts/default_bootstrap.tmpl` — 默认术语抽取提示词模板
- `profiles/default.yaml` — 默认执行配置

## local 命令

以单用户本地模式启动 LinguaFlow。

```bash
linguaflow local [flags]
```

| 参数           | 类型   | 默认值                            | 描述                                    |
| -------------- | ------ | --------------------------------- | --------------------------------------- |
| `--host`       | string | `"127.0.0.1"`                     | 监听地址                                |
| `--port`       | int    | `18080`                           | 监听端口（设为 `0` 时自动选择随机端口） |
| `--data-dir`   | string | 系统用户配置目录下的 `LinguaFlow` | 数据目录                                |
| `--no-browser` | bool   | `false`                           | 不自动打开浏览器                        |

特性：

- 数据目录默认为系统用户配置目录下的 `LinguaFlow`（Windows 为 `%APPDATA%\LinguaFlow`）
- 端口占用时自动尝试后续最多 10 个端口
- 启动后自动打开浏览器访问 `http://<host>:<port>`
- 在 Windows 资源管理器中双击可执行文件时，自动以 `local` 模式启动

## serve 命令

启动服务器模式（**预览**）。多用户与权限等能力仍在完善，不建议用于生产关键业务；个人使用请优先 `local`。

```bash
linguaflow serve [flags]
```

| 参数             | 类型   | 默认值      | 描述                                       |
| ---------------- | ------ | ----------- | ------------------------------------------ |
| `--host`         | string | `"0.0.0.0"` | 监听地址                                   |
| `--port`         | int    | `8080`      | 监听端口                                   |
| `--data-dir`     | string | `"./data"`  | 数据目录                                   |
| `--auto-migrate` | bool   | `true`      | 启动时自动执行数据库迁移                   |
| `--no-ui`        | bool   | `false`     | 关闭嵌入式 Web UI，仅提供 API              |
| `--jwt-secret`   | string | `""`        | 覆盖 `LINGUAFLOW_JWT_SECRET`               |
| `--cors-origins` | string | `""`        | 覆盖 `LINGUAFLOW_CORS_ORIGINS`（逗号分隔） |

默认提供嵌入式 Web UI。仅需 API 时：

```bash
linguaflow serve --no-ui
# 或
LINGUAFLOW_SERVE_UI=false linguaflow serve
```

### 数据库配置

服务器模式的数据库通过环境变量配置，不读取配置文件中的 `server` 段：

| 环境变量                                | 描述                                                                        | 默认值                     |
| --------------------------------------- | --------------------------------------------------------------------------- | -------------------------- |
| `LINGUAFLOW_DATABASE_DRIVER`            | 数据库驱动：`sqlite` \| `postgres`                                          | `sqlite`                   |
| `LINGUAFLOW_DATABASE_DSN`               | 数据库连接串。`postgres` 必填；`sqlite` 为空时使用 `data_dir/linguaflow.db` | -                          |
| `LINGUAFLOW_DATABASE_MAX_OPEN_CONNS`    | 最大打开连接数                                                              | `sqlite=0` / `postgres=25` |
| `LINGUAFLOW_DATABASE_MAX_IDLE_CONNS`    | 最大空闲连接数                                                              | `sqlite=2` / `postgres=5`  |
| `LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME` | 连接最大寿命（Go duration）                                                 | `postgres=30m`             |

使用 PostgreSQL 的示例：

```bash
export LINGUAFLOW_DATABASE_DRIVER=postgres
export LINGUAFLOW_DATABASE_DSN='postgres://user:pass@localhost:5432/linguaflow?sslmode=disable'
linguaflow serve
```

::: warning 本地模式不支持 PostgreSQL
`linguaflow local` 始终使用 SQLite。高并发场景请使用 `linguaflow serve` 配合 PostgreSQL。
:::

### 管理员用户

通过环境变量可在启动时自动创建管理员用户：

```bash
export LINGUAFLOW_ADMIN_USERNAME=admin
export LINGUAFLOW_ADMIN_PASSWORD=your-password
linguaflow serve
```

如果指定的用户名已存在，则将其提升为管理员；如果不存在，则同时需要设置 `LINGUAFLOW_ADMIN_PASSWORD` 才能创建。

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

LinguaFlow 支持通过配置文件进行详细配置。配置文件的加载优先级为：

```text
命令行参数 > 环境变量 > 配置文件 > 内置默认值
```

配置文件中的所有字符串值支持环境变量扩展，语法为 `${ENV_VAR}` 或 `${ENV_VAR:-默认值}`。

使用 `linguaflow init` 生成配置文件模板，详见 [配置](/zh/guide/configuration)。

## 下一步

- [快速开始 · CLI](/zh/guide/cli-quickstart) — 最短命令行路径
- [配置参考](/zh/guide/configuration) — 配置文件格式
- [项目管理](/zh/guide/projects) — Web 界面操作
