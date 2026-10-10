# 使用模式

LinguaFlow 提供两种运行模式。**个人使用请优先本地模式**；服务器模式面向多用户与 API 部署，目前仍为预览能力。

| 模式 | 命令 | 默认地址 | 推荐场景 |
| --- | --- | --- | --- |
| **本地模式** | `linguaflow` / `linguaflow local` | `http://127.0.0.1:18080` | 个人本机、免登录 |
| **服务器模式**（预览） | `linguaflow serve` | `http://0.0.0.0:8080` | 多用户 / 仅 API（功能仍在完善） |

Docker 镜像默认以**服务器模式**监听 `8080`，与双击二进制进入本地模式不同。详见 [安装部署](/zh/guide/installation)。

## 本地模式

本地模式适用于个人使用，无需登录，数据存储在本机。适合作为默认上手路径。

### 启动本地模式

```bash
linguaflow local
```

或者直接双击运行 `linguaflow`（Windows）/ `./linguaflow`（Linux/macOS），程序检测到双击启动后会自动进入本地模式。

启动后会自动打开浏览器访问 `http://127.0.0.1:18080`。

### 本地模式特点

- **无需登录** — 启动即可使用，前端自动跳过登录和注册页面
- **自动打开浏览器** — 默认访问 `http://127.0.0.1:18080`，可通过 `--no-browser` 禁用
- **嵌入式前端** — 前端静态资源打包在后端二进制中，无需单独部署
- **本地数据存储** — SQLite 数据库，数据目录为系统用户配置目录下的 `LinguaFlow` 文件夹
- **端口冲突处理** — 如果端口被占用，自动递增端口号，最多尝试 10 次
- **CORS 限制** — 仅允许来自 `127.0.0.1` 和 `localhost` 的请求

### 本地模式配置

| 参数              | 描述                                       | 默认值                     |
| ----------------- | ------------------------------------------ | -------------------------- |
| `--port`          | 监听端口（`0` 交给系统分配）               | `18080`                    |
| `--host`          | 监听地址                                   | `127.0.0.1`                |
| `--data-dir`      | 数据目录                                   | `UserConfigDir/LinguaFlow` |
| `--no-browser`    | 不自动打开浏览器                           | `false`                    |
| `--allow-network` | 允许非回环监听（仅在显式改 `--host` 时需要） | `false`                    |

::: warning 非回环监听需显式放行
本地模式身份即管理员，因此默认只监听回环地址（`127.0.0.1`）。若通过 `--host` 改为局域网地址，**必须**在同一条命令中显式加 `--allow-network`，否则启动失败——能连上你的人就拥有这台实例的管理员权限，请确认网络环境可信。
:::

::: tip 数据目录说明
`UserConfigDir` 因操作系统而异：

- Windows: `%AppData%`（如 `C:\Users\<用户名>\AppData\Roaming`）
- macOS: `~/Library/Application Support`
- Linux: `~/.config`（遵循 `XDG_CONFIG_HOME`）
  :::

::: info 自动生成的实例密钥
本地模式首次启动会在数据目录自动生成两个私有密钥文件：`instance-secret`（JWT 签名密钥）与 `credentials-keyring.json`（AI 密钥的加密密钥），后续启动复用。请像对待数据库文件一样对待它们——**丢失后已保存的 AI 密钥将无法解密**，备份时需要一并备份。
:::

## 服务器模式（预览）

::: warning 预览状态
服务器模式（多用户、权限、组织等）仍在完善中，**不建议用于生产环境或关键业务**。个人翻译请使用 [本地模式](#本地模式)。以下说明便于试用与反馈。

想先看看多用户效果？官方在线预览实例 **[meowsalty-linguaflow.hf.space](https://meowsalty-linguaflow.hf.space/)** 注册账号即可登录（演示环境，**数据不做持久化**）。
:::

服务器模式面向需要登录、多用户或「仅暴露 API」的部署场景。与本地模式最大的不同：**服务器模式不会替你生成任何密钥**，启动前必须自行准备部署配置与两类密钥。

### 启动服务器模式

推荐先在部署目录生成一份部署文档，再按需修改：

```bash
# 1. 生成 server.yaml 部署文档（含注释说明）
linguaflow init --kind server

# 2. 准备两个必需密钥（32 字节随机值，Base64）
linguaflow secrets generate --output /private/linguaflow/jwt-secret
linguaflow secrets generate --output /private/linguaflow/credentials-master-key

# 3. 预检配置（只读解析，不启动、不建库）
linguaflow config check --config server.yaml
linguaflow config explain --config server.yaml   # 查看最终配置与每项来源

# 4. 启动（JWT secret、凭据加密密钥、初始管理员通过环境变量注入）
export LINGUAFLOW_JWT_SECRET_FILE=/private/linguaflow/jwt-secret
export LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE=/private/linguaflow/credentials-master-key
export LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=admin
export LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
export LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD='初次登录用，之后请修改'
linguaflow serve --config server.yaml
```

缺任何一项密钥或初始管理员输入，`serve` 都会**拒绝启动**——这是刻意设计，避免拿弱密钥跑起来。部署文档只通过 `--config`（或 `LINGUAFLOW_SERVER_CONFIG` 环境变量）选择，程序**不会**自动搜索目录或读取 `.env` 文件。

::: tip 不想碰配置文件？
所有部署字段也支持环境变量（见 [配置文件与环境变量](/zh/guide/configuration)），可以完全不写 `server.yaml`，纯用环境变量启动。配置文件适合把监听地址、数据目录等非敏感项固化下来，敏感值仍建议走环境变量。
:::

### 服务器模式特点

- **多用户** — 注册 / 登录（注册默认关闭，由管理员在设置页开启）
- **JWT 认证** — Access Token 与 Refresh Token；JWT 签名密钥 ≥32 字节、必须显式提供
- **凭据加密** — 所有 AI 服务密钥以加密形式落库，需要提供凭据加密密钥（master key 或 keyring 文件，二选一）
- **网络访问** — 默认监听 `0.0.0.0`（部署时请自行限制暴露面）
- **数据库** — 默认 SQLite；可切 PostgreSQL（`server.database` 配置）
- **嵌入式 Web UI** — 默认开启；`--no-ui` 或 `LINGUAFLOW_SERVE_UI=false` 可仅暴露 API
- **重启自动恢复** — 服务重启时，中断的运行中任务自动重置为待执行并从断点续跑

### 服务器模式配置

| 参数             | 描述                               | 默认值    |
| ---------------- | ---------------------------------- | --------- |
| `--config` `-c`  | 部署文档路径（`server.yaml`）      | —         |
| `--host`         | 覆盖 `server.host`                 | `0.0.0.0` |
| `--port`         | 覆盖 `server.port`                 | `8080`    |
| `--data-dir`     | 覆盖 `server.data_dir`             | `./data`  |
| `--auto-migrate` | 覆盖 `server.auto_migrate`         | `true`    |
| `--no-ui`        | 关闭嵌入式 Web UI，仅提供 API      | `false`   |

完整的 `server.yaml` 字段、环境变量与密钥格式见 [配置文件与环境变量](/zh/guide/configuration)。

### 管理员配置

服务器模式的初始管理员在**首次启动初始化**时创建，来源有两种：

```bash
# 方式一：环境变量（首次启动自动初始化）
export LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=admin
export LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
export LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD='...'
linguaflow serve --config server.yaml

# 方式二：维护命令（服务未启动时离线执行也可）
linguaflow admin initialize --config server.yaml \
  --username admin --email admin@example.com --password-file ./admin-password
```

初始化只发生一次；之后数据库里的账号与注册政策才是权威，`bootstrap.admin` 输入可以在初始化完成后从部署配置中删除。后续维护使用：

| 命令                                            | 用途                                                         |
| ----------------------------------------------- | ------------------------------------------------------------ |
| `linguaflow admin create --config ... --username ... --email ... --password-file ...` | 为已运行实例追加一名管理员 |
| `linguaflow admin recover --config ... --username ... --password-stdin` | 找回：将已有账户恢复为活跃管理员、重置密码并吊销其刷新令牌 |

旧版本通过 `LINGUAFLOW_ADMIN_USERNAME` / `LINGUAFLOW_ADMIN_PASSWORD` 建号、或「首个注册用户自动成为管理员」的行为**均已移除**；注册一律创建普通用户。

管理员可在服务器模式下管理用户、查看全局统计与审计日志、开关注册，详见 [管理员后台](/zh/guide/admin)。

::: warning 安全提示
服务器模式下，请确保：

- 使用强密钥：JWT secret 与凭据加密密钥都必须是独立随机的 ≥32 字节值（可用 `linguaflow secrets generate` 生成），**不要共用同一个值**
- 备份凭据加密密钥（master key / keyring 文件）：丢失后数据库中已保存的 AI 密钥将**永久无法解密**；数据库备份与密钥必须成对保存
- 初始化完成后修改管理员密码，并从部署配置中移除 bootstrap 管理员输入
- 在生产环境中配置 HTTPS（通过反向代理）
- 限制监听地址为内网或配置防火墙
  :::

### 密钥轮换（进阶）

凭据加密密钥支持轮换（例如怀疑泄露或定期治理）：`secrets keyring rotate` 生成含新旧密钥的 keyring 文件 → 重启服务 → `admin credentials reencrypt` 重加密存量数据。完整步骤见 [管理员后台 · 密钥轮换](/zh/guide/admin#凭据加密密钥轮换)。

### 通过 HTTP 访问的限制（非安全上下文）

上一条「配置 HTTPS」不只是传输安全建议，还关系到浏览器能否放开部分 Web API。浏览器仅在**安全上下文**（HTTPS 或 `localhost`）下放开 `crypto.randomUUID`、`navigator.clipboard` 等 API。服务器模式默认监听 `0.0.0.0`，若你用**明文 HTTP** 访问局域网 IP 或域名（Docker 裸 HTTP 部署最常见），以下功能会被浏览器禁用，界面会拦截操作并提示：

| 功能 | 受限原因 | 表现 |
| --- | --- | --- |
| **文件上传** | 依赖 `crypto.randomUUID` 生成任务 ID | 上传入口被拦截，提示「非安全连接，文件上传不可用」 |
| **复制到剪贴板** | 依赖 `navigator.clipboard` | 批次内容复制被拦截，提示「非安全连接，复制不可用」 |

启动时若检测到非安全上下文，界面会弹出一次性通知，列出受影响功能并给出指引。

::: tip 如何解除限制
任选其一：

- **生产推荐**：通过反向代理套一层 HTTPS（Nginx / Caddy / 云负载均衡）再访问
- **本机调试**：改用 `http://localhost:端口` 或 `http://127.0.0.1:端口`（属于安全上下文）
- **Chrome / Edge 临时放行**：按启动通知里的指引，在 `chrome://flags/#unsafely-treat-insecure-origin-as-secure`（Edge 为 `edge://flags/...`）填入当前 origin 并启用，重启浏览器后刷新页面
:::

本地模式访问的是 `127.0.0.1`，属于安全上下文，不受此影响。

## 模式对比

| 特性           | 本地模式                             | 服务器模式                            |
| -------------- | ------------------------------------ | ------------------------------------- |
| CLI 命令       | `linguaflow local`                   | `linguaflow serve --config server.yaml` |
| 用户认证       | 自动认证（跳过 JWT）                 | JWT Token 认证                        |
| 多用户         | 单用户（`local` 用户）               | 多用户、组织与角色                    |
| 用户注册       | 不支持                               | 默认关闭，管理员可在设置页开启        |
| 密钥           | 自动生成 `instance-secret` 与凭据 keyring | 必须显式提供 JWT secret 与凭据加密密钥 |
| 初始管理员     | 免登录内置                           | 首次初始化时经 `bootstrap.admin` 创建 |
| 默认端口       | `18080`                              | `8080`                                |
| 默认监听       | `127.0.0.1`（非回环需 `--allow-network`） | `0.0.0.0`                         |
| 数据目录       | `UserConfigDir/LinguaFlow`           | `./data`                              |
| 数据库         | 仅 SQLite                            | SQLite / PostgreSQL                   |
| 前端资源       | 嵌入在二进制中                       | 默认嵌入，可 `--no-ui` 关闭           |
| CORS 策略      | 仅允许回环来源                       | 默认 `["*"]`；配置为 `[]` 时禁止跨域  |
| 自动打开浏览器 | 是                                   | 否                                    |
| 双击启动       | 自动进入本地模式                     | 不支持                                |
| 成熟度         | 推荐日常使用                         | 预览，功能仍在完善                    |
| 适用场景       | 个人使用                             | 试用多用户 / API 服务                 |

## 界面上的差异

- **本地模式**：免登录直接进入主界面，顶部通常有「本地模式」标识  
- **服务器模式**：需登录（注册默认关闭，管理员可在设置页开启）；可在设置中心管理个人资料、安全、偏好与团队  

## 下一步

- [快速开始 · Web](/zh/guide/getting-started) — 本地模式最短路径
- [安装部署](/zh/guide/installation) — Docker 与数据目录
- [配置文件与环境变量](/zh/guide/configuration) — 环境变量与配置文件
- [CLI 命令参考](/zh/guide/cli) — 全部子命令
