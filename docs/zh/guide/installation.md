# 安装部署

LinguaFlow 提供多种安装方式。**个人使用推荐预编译二进制（本地模式）**；Docker / 服务器模式适合容器化或试用多用户部署。

| 方式 | 默认模式 | 默认端口 | 说明 |
| --- | --- | --- | --- |
| 预编译二进制 / 双击运行 | 本地模式 | `18080` | 免登录，推荐上手 |
| `linguaflow local` | 本地模式 | `18080` | 同上 |
| Docker 镜像默认 | 服务器模式（预览） | `8080` | 需注册/登录；见下方说明 |
| `linguaflow serve` | 服务器模式（预览） | `8080` | 功能仍在完善，勿用于生产关键业务 |

跑通第一次翻译请先看 [快速开始 · Web](/zh/guide/getting-started)。

## 系统要求

| 要求                  | 最低版本 |
| --------------------- | -------- |
| Go（从源码构建）      | 1.21+    |
| Node.js（从源码构建） | 20+      |
| pnpm（从源码构建）    | 最新版   |
| Docker（容器部署）    | 20+      |

## Docker 部署

::: warning 容器默认是服务器模式
官方镜像默认执行服务器模式（端口 `8080`），与本机双击二进制进入的本地模式不同。服务器模式仍在完善中，适合试用，不建议作为生产唯一依赖。个人本机请优先使用 [预编译二进制](#预编译二进制)。
:::

### 基本部署

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -d \
  --name linguaflow \
  -p 8080:8080 \
  -v linguaflow-data:/app/data \
  ghcr.io/meowsalty/linguaflow:latest
```

浏览器访问 `http://localhost:8080`，按提示注册/登录后使用。

### Docker Compose

使用 SQLite（默认）的部署示例：

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
      - LINGUAFLOW_DATA_DIR=/app/data
      - LINGUAFLOW_JWT_SECRET=change-me-to-a-random-string

volumes:
  linguaflow-data:
```

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
      - LINGUAFLOW_DATABASE_DRIVER=postgres
      - LINGUAFLOW_DATABASE_DSN=postgres://linguaflow:secret@postgres:5432/linguaflow?sslmode=disable
      - LINGUAFLOW_JWT_SECRET=change-me-to-a-random-string
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
| `LINGUAFLOW_JWT_SECRET` | JWT 密钥（务必改掉默认值） |
| `LINGUAFLOW_DATABASE_DRIVER` / `DSN` | 切换 PostgreSQL 时使用 |
| `LINGUAFLOW_SERVE_UI` | `false` 时仅 API（也可用 `--no-ui`） |
| `LINGUAFLOW_ADMIN_USERNAME` / `PASSWORD` | 启动时管理员账户 |

**完整环境变量表、连接池参数与配置文件字段** 只维护在一处：

→ [配置文件与环境变量](/zh/guide/configuration)

| 驱动 | 说明 |
| --- | --- |
| `sqlite`（默认） | 本地模式强制使用；服务器模式也可 |
| `postgres` | 仅服务器模式；需自备实例 |

本地模式始终 SQLite，不读 PostgreSQL 相关变量。

### HuggingFace Spaces 部署

LinguaFlow 支持部署到 HuggingFace Spaces，使用 `Dockerfile.hf` 构建。

#### 部署步骤

1. **创建 Space**
   - 访问 [HuggingFace Spaces](https://huggingface.co/new-space)
   - 选择 **Docker** 作为 SDK
   - 设置 Space 名称和可见性

2. **配置仓库**

   将项目代码推送到 Space 仓库，或直接在 Space 中关联 GitHub 仓库。

3. **环境变量配置**

   在 Space 的 **Settings** 页面添加环境变量：

   | 变量名                      | 描述         |
   | --------------------------- | ------------ |
   | `LINGUAFLOW_ADMIN_USERNAME` | 管理员用户名 |
   | `LINGUAFLOW_ADMIN_PASSWORD` | 管理员密码   |

4. **访问服务**

   部署完成后，通过 `https://<username>-<space-name>.hf.space` 访问服务。

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
