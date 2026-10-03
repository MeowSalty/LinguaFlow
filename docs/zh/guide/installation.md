# 安装部署

LinguaFlow 提供多种安装方式。**个人使用推荐预编译二进制（本地模式）**；Docker / 服务器模式适合容器化或试用多用户部署。

| 方式 | 默认模式 | 默认端口 | 说明 |
| --- | --- | --- | --- |
| 预编译二进制 / 双击运行 | 本地模式 | `18080` | 免登录，推荐上手 |
| `linguaflow local` | 本地模式 | `18080` | 同上 |
| Docker 镜像默认 | 服务器模式（预览） | `8080` | 需注入密钥与管理员后启动；见下方说明 |
| `linguaflow serve` | 服务器模式（预览） | `8080` | 需部署配置与密钥；见 [使用模式](/zh/guide/modes) |

跑通第一次翻译请先看 [快速开始 · Web](/zh/guide/getting-started)。

## 系统要求

| 要求                  | 最低版本 |
| --------------------- | -------- |
| Go（从源码构建）      | 1.26.8+  |
| Node.js（从源码构建） | 20+      |
| pnpm（从源码构建）    | 最新版   |
| Docker（容器部署）    | 20+      |

## Docker 部署

::: warning 容器默认是服务器模式，且必须提供密钥
官方镜像默认执行服务器模式（端口 `8080`），与本机双击二进制进入的本地模式不同。服务器模式启动前**必须**提供三类输入，缺一即拒绝启动：

1. **JWT 签名密钥** — `LINGUAFLOW_JWT_SECRET`（≥32 字节随机值）
2. **凭据加密密钥** — `LINGUAFLOW_CREDENTIALS_MASTER_KEY`（32 字节随机值的 Base64）
3. **初始管理员** — `LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME` / `_EMAIL` / `_PASSWORD`（仅首次初始化需要）

三个值都可用 `linguaflow secrets generate --stdout` 生成；JWT secret 与凭据加密密钥必须是两个不同的随机值。服务器模式仍在完善中，适合试用，不建议作为生产唯一依赖。个人本机请优先使用 [预编译二进制](#预编译二进制)。
:::

### 基本部署

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -d \
  --name linguaflow \
  -p 8080:8080 \
  -v linguaflow-data:/app/data \
  -e LINGUAFLOW_JWT_SECRET="$(linguaflow secrets generate --stdout)" \
  -e LINGUAFLOW_CREDENTIALS_MASTER_KEY="$(linguaflow secrets generate --stdout)" \
  -e LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=admin \
  -e LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
  -e LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD="请改成强密码" \
  ghcr.io/meowsalty/linguaflow:latest
```

浏览器访问 `http://localhost:8080`，用上面的管理员账号登录后使用。

::: warning 密钥保存与复用
JWT secret 与凭据加密密钥请保存到密码管理器或部署平台的秘密配置中，并在**每次重建容器时注入同一份值**——凭据加密密钥一旦更换，数据库里已保存的 AI 密钥将无法解密。首次初始化成功后，`LINGUAFLOW_BOOTSTRAP_ADMIN_*` 三个变量可以不再注入（账号与注册政策已存入数据库）。
:::

::: tip 注意「非安全上下文」限制
浏览器只在 HTTPS 或 `localhost` 下放开部分 API。若你把容器映射到局域网 IP / 域名用**明文 HTTP** 访问，**文件上传**和**复制到剪贴板**会被浏览器禁用。生产建议经反向代理上 HTTPS，详见 [使用模式 · 通过 HTTP 访问的限制](/zh/guide/modes#通过-http-访问的限制-非安全上下文)。
:::

### Docker Compose

SQLite（默认）+ 环境变量注入密钥的部署示例：

```yaml
services:
  linguaflow:
    image: ghcr.io/meowsalty/linguaflow:latest
    container_name: linguaflow
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - linguaflow-data:/app/data
    environment:
      LINGUAFLOW_DATA_DIR: /app/data
      # 以下三项建议通过部署平台的秘密配置或 .env 注入，不要明文提交
      LINGUAFLOW_JWT_SECRET: ${LINGUAFLOW_JWT_SECRET:?需要至少 32 字节的随机值}
      LINGUAFLOW_CREDENTIALS_MASTER_KEY: ${LINGUAFLOW_CREDENTIALS_MASTER_KEY:?需要 32 字节随机值的 Base64}
      LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME: ${LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME:-admin}
      LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL: ${LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL:?需要初始管理员邮箱}
      LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD: ${LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD:?需要初始管理员密码}

volumes:
  linguaflow-data:
```

在 Compose 文件同级放一个 `.env`（加入 `.gitignore`，不要提交）：

```bash
LINGUAFLOW_JWT_SECRET=<secrets generate --stdout 的输出>
LINGUAFLOW_CREDENTIALS_MASTER_KEY=<另一个独立随机值>
LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME=admin
LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL=admin@example.com
LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD=<强密码>
```

::: tip 更安全的密钥文件方式
敏感值支持 `_FILE` 后缀的环境变量，指向容器内可读的文件路径，配合 Docker secrets 或 bind mount 使用（如 `LINGUAFLOW_JWT_SECRET_FILE: /run/secrets/jwt-secret`）。JWT secret、凭据加密密钥、数据库 DSN 与管理员密码均支持该形式。
:::

使用 PostgreSQL 的部署示例（适合高并发场景）：

```yaml
services:
  linguaflow:
    image: ghcr.io/meowsalty/linguaflow:latest
    container_name: linguaflow
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      LINGUAFLOW_JWT_SECRET: ${LINGUAFLOW_JWT_SECRET:?需要至少 32 字节的随机值}
      LINGUAFLOW_CREDENTIALS_MASTER_KEY: ${LINGUAFLOW_CREDENTIALS_MASTER_KEY:?需要 32 字节随机值的 Base64}
      LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME: ${LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME:-admin}
      LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL: ${LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL:?需要初始管理员邮箱}
      LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD: ${LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD:?需要初始管理员密码}
      LINGUAFLOW_DATABASE_DRIVER: postgres
      LINGUAFLOW_DATABASE_DSN: postgres://linguaflow:secret@postgres:5432/linguaflow?sslmode=disable
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:17-alpine
    container_name: linguaflow-postgres
    restart: unless-stopped
    environment:
      - POSTGRES_USER=linguaflow
      - POSTGRES_PASSWORD=secret
      - POSTGRES_DB=linguaflow
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U linguaflow"]
      interval: 5s
      timeout: 3s
      retries: 10

volumes:
  postgres-data:
```

启动服务：

```bash
docker compose up -d
```

### 环境变量与数据库（摘要）

Compose 示例中常用变量：

| 变量 | 用途 |
| --- | --- |
| `LINGUAFLOW_DATA_DIR` | 数据目录（SQLite 文件等） |
| `LINGUAFLOW_JWT_SECRET` | JWT 签名密钥（必需，≥32 字节；支持 `_FILE`） |
| `LINGUAFLOW_CREDENTIALS_MASTER_KEY` | 凭据加密密钥（必需；支持 `_FILE`） |
| `LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME` / `_EMAIL` / `_PASSWORD` | 首次初始化的管理员（密码支持 `_FILE`） |
| `LINGUAFLOW_BOOTSTRAP_REGISTRATION_ENABLED` | 是否开放注册（默认 `false`，也可登录后管理员在设置页开启） |
| `LINGUAFLOW_DATABASE_DRIVER` / `DSN` | 切换 PostgreSQL 时使用（`DSN` 支持 `_FILE`） |
| `LINGUAFLOW_SERVE_UI` | `false` 时仅 API（也可用 `--no-ui`） |

**完整环境变量表、部署文档字段与配置优先级** 只维护在一处：

→ [配置文件与环境变量](/zh/guide/configuration)

| 驱动 | 说明 |
| --- | --- |
| `sqlite`（默认） | 本地模式强制使用；服务器模式也可 |
| `postgres` | 仅服务器模式；需自备实例 |

本地模式始终 SQLite，不读 PostgreSQL 相关变量。

### HuggingFace Spaces 部署

LinguaFlow 支持部署到 HuggingFace Spaces，使用 `Dockerfile.hf` 构建。

#### 自动部署（推荐）

每次发布新版本（Release 正式发布、Docker 镜像构建完成后），CI 会自动把最小部署内容（引用发布镜像的 `Dockerfile` 与含 Space 配置的 `README.md`）推送到 Space 仓库 `MeowSalty/linguaflow`，无需手动操作。

#### 手动部署

1. **创建 Space**
   - 访问 [HuggingFace Spaces](https://huggingface.co/new-space)
   - 选择 **Docker** 作为 SDK
   - 设置 Space 名称和可见性

2. **准备部署文件**

   Space 仓库只需两个文件（CI 自动部署即是推送这两个文件）：

   - `Dockerfile`：将 `Dockerfile.hf` 中的镜像 tag 替换为具体版本（如 `ghcr.io/meowsalty/linguaflow:0.10.0`）
   - `README.md`：顶部 front-matter 必须包含 `sdk: docker` 与 `app_port: 7860`，HF 依赖它识别 Docker Space 并转发端口

   推送时使用全新孤儿提交，避免覆盖 Space 仓库中的其他配置。

3. **环境变量配置**

   在 Space 的 **Settings** 页面添加环境变量（可用 secrets 形式保存）：

   | 变量名                              | 描述                                   |
   | ----------------------------------- | -------------------------------------- |
   | `LINGUAFLOW_JWT_SECRET`             | JWT 签名密钥（≥32 字节随机值）         |
   | `LINGUAFLOW_CREDENTIALS_MASTER_KEY` | 凭据加密密钥（32 字节随机值的 Base64） |
   | `LINGUAFLOW_BOOTSTRAP_ADMIN_USERNAME` | 初始管理员用户名                     |
   | `LINGUAFLOW_BOOTSTRAP_ADMIN_EMAIL`  | 初始管理员邮箱                         |
   | `LINGUAFLOW_BOOTSTRAP_ADMIN_PASSWORD` | 初始管理员密码                       |
   | `LINGUAFLOW_DATA_DIR`               | 设为 `/data`，否则重启丢数据           |

   ::: warning
   Space 容器仅 `/data` 目录持久化。请务必设置 `LINGUAFLOW_DATA_DIR=/data` 保留 SQLite 数据；JWT secret 与凭据加密密钥也要长期保存不变，否则重启后已保存的 AI 密钥将无法解密。
   :::

4. **访问服务**

   部署完成后，通过 `https://<username>-<space-name>.hf.space` 访问服务，用初始管理员账号登录。

::: tip
HuggingFace Spaces 默认使用 7860 端口，`Dockerfile.hf` 已自动配置。
:::

## 预编译二进制

从 [GitHub Releases](https://github.com/MeowSalty/LinguaFlow/releases) 下载对应平台的二进制文件。**这是个人使用的推荐方式。**

支持的平台：

| 平台    | 架构         |
| ------- | ------------ |
| Linux   | amd64, arm64 |
| macOS   | amd64, arm64 |
| Windows | amd64, arm64 |

::: code-group

```bash [Linux / macOS]
chmod +x linguaflow
./linguaflow
# 本地模式，自动打开 http://127.0.0.1:18080
```

```powershell [Windows]
.\linguaflow.exe
# 或资源管理器中双击；本地模式，端口 18080
```

:::

::: tip 校验文件完整性
Release 页面提供 SHA256 校验和文件，下载后请验证文件完整性。
:::

## 从源码构建

### 克隆仓库

```bash
git clone https://github.com/MeowSalty/LinguaFlow.git
cd LinguaFlow
```

### 安装依赖

```bash
task backend:install
task frontend:install
```

### 构建

```bash
task backend:build
```

构建产物位于 `bin/linguaflow`。

### 开发模式

```bash
# 启动后端开发服务器
task backend:dev

# 启动前端开发服务器（另一个终端）
task frontend:dev
```

## 验证安装

| 启动方式 | 验证地址 |
| --- | --- |
| 二进制 / `linguaflow local` | `http://127.0.0.1:18080`（端口占用时会自动递增） |
| Docker / `linguaflow serve` | `http://localhost:8080`（或你映射的端口） |

看到 Web 界面即表示安装成功。接着按 [快速开始 · Web](/zh/guide/getting-started) 配置后端并完成第一次翻译。

## 下一步

- [快速开始 · Web](/zh/guide/getting-started) — 最短使用路径
- [使用模式](/zh/guide/modes) — 本地模式与服务器模式（预览）
- [配置文件与环境变量](/zh/guide/configuration) — 完整配置参考
