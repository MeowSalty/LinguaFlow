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

| 参数           | 描述             | 默认值                     |
| -------------- | ---------------- | -------------------------- |
| `--port`       | 监听端口         | `18080`                    |
| `--host`       | 监听地址         | `127.0.0.1`                |
| `--data-dir`   | 数据目录         | `UserConfigDir/LinguaFlow` |
| `--no-browser` | 不自动打开浏览器 | `false`                    |

::: tip 数据目录说明
`UserConfigDir` 因操作系统而异：

- Windows: `%AppData%`（如 `C:\Users\<用户名>\AppData\Roaming`）
- macOS: `~/Library/Application Support`
- Linux: `~/.config`（遵循 `XDG_CONFIG_HOME`）
  :::

## 服务器模式（预览）

::: warning 预览状态
服务器模式（多用户、权限、组织等）仍在完善中，**不建议用于生产环境或关键业务**。个人翻译请使用 [本地模式](#本地模式)。以下说明便于试用与反馈。
:::

服务器模式面向需要登录、多用户或「仅暴露 API」的部署场景。

### 启动服务器模式

```bash
linguaflow serve
```

### 服务器模式特点

- **多用户** — 注册 / 登录（能力仍在迭代）
- **JWT 认证** — Access Token 与 Refresh Token
- **网络访问** — 默认监听 `0.0.0.0`（部署时请自行限制暴露面）
- **数据库** — 默认 SQLite；可尝试 PostgreSQL（环境变量配置）
- **嵌入式 Web UI** — 默认开启；`--no-ui` 或 `LINGUAFLOW_SERVE_UI=false` 可仅暴露 API

### 服务器模式配置

| 参数             | 描述                          | 默认值    |
| ---------------- | ----------------------------- | --------- |
| `--port`         | 监听端口                      | `8080`    |
| `--host`         | 监听地址                      | `0.0.0.0` |
| `--data-dir`     | 数据目录                      | `./data`  |
| `--auto-migrate` | 自动迁移数据库                | `true`    |
| `--no-ui`        | 关闭嵌入式 Web UI，仅提供 API | `false`   |
| `--jwt-secret`   | 覆盖 JWT 签名密钥             | —         |
| `--cors-origins` | 覆盖允许的跨域来源            | —         |

### 管理员配置

服务器模式下，可通过环境变量配置初始管理员账户：

```bash
export LINGUAFLOW_ADMIN_USERNAME=admin
export LINGUAFLOW_ADMIN_PASSWORD=your-secure-password
linguaflow serve
```

如果未设置环境变量，首个注册的用户将自动成为管理员（取决于配置中 `registration.auto_admin` 的设置）。

::: warning 安全提示
服务器模式下，请确保：

- 使用强密码
- 修改默认的 JWT Secret（配置项 `server.jwt_secret`）
- 在生产环境中配置 HTTPS（通过反向代理）
- 限制监听地址为内网或配置防火墙
  :::

## 模式对比

| 特性           | 本地模式                   | 服务器模式                  |
| -------------- | -------------------------- | --------------------------- |
| CLI 命令       | `linguaflow local`         | `linguaflow serve`          |
| 用户认证       | 自动认证（跳过 JWT）       | JWT Token 认证              |
| 多用户         | 单用户（`local` 用户）     | 多租户                      |
| 用户注册       | 不支持                     | 默认开放                    |
| 默认端口       | `18080`                    | `8080`                      |
| 默认监听       | `127.0.0.1`                | `0.0.0.0`                   |
| 数据目录       | `UserConfigDir/LinguaFlow` | `./data`                    |
| 数据库         | 仅 SQLite                  | SQLite / PostgreSQL         |
| 前端资源       | 嵌入在二进制中             | 默认嵌入，可 `--no-ui` 关闭 |
| CORS 策略      | 仅 localhost               | 可配置（默认 `*`）          |
| 自动打开浏览器 | 是                         | 否                          |
| 双击启动       | 自动进入本地模式           | 不支持                      |
| 成熟度         | 推荐日常使用               | 预览，功能仍在完善          |
| 适用场景       | 个人使用                   | 试用多用户 / API 服务       |

## 界面上的差异

- **本地模式**：免登录直接进入主界面，顶部通常有「本地模式」标识  
- **服务器模式**：需注册/登录；可切换服务端地址  

## 下一步

- [快速开始 · Web](/zh/guide/getting-started) — 本地模式最短路径
- [安装部署](/zh/guide/installation) — Docker 与数据目录
- [配置文件与环境变量](/zh/guide/configuration) — 环境变量与配置文件
- [CLI 命令参考](/zh/guide/cli) — 全部子命令
