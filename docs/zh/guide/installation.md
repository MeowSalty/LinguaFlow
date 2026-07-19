# 安装部署

LinguaFlow 提供多种安装方式，您可以根据自己的需求选择。

## 系统要求

| 要求                  | 最低版本 |
| --------------------- | -------- |
| Go（从源码构建）      | 1.21+    |
| Node.js（从源码构建） | 20+      |
| pnpm（从源码构建）    | 最新版   |
| Docker（容器部署）    | 20+      |

## Docker 部署

### 基本部署

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -d \
  --name linguaflow \
  -p 8080:8080 \
  -v linguaflow-data:/app/data \
  ghcr.io/meowsalty/linguaflow:latest
```

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

### 环境变量配置

| 变量名                                  | 描述                                  | 默认值                     |
| --------------------------------------- | ------------------------------------- | -------------------------- |
| `LINGUAFLOW_PORT`                       | 服务监听端口                          | `8080`                     |
| `LINGUAFLOW_DATA_DIR`                   | 数据目录（SQLite 数据库文件存放于此） | `./data`                   |
| `LINGUAFLOW_DATABASE_DRIVER`            | 数据库驱动：`sqlite` \| `postgres`    | `sqlite`                   |
| `LINGUAFLOW_DATABASE_DSN`               | 数据库连接串；选 `postgres` 时必填    | -                          |
| `LINGUAFLOW_DATABASE_MAX_OPEN_CONNS`    | 最大打开连接数                        | `sqlite=0` / `postgres=25` |
| `LINGUAFLOW_DATABASE_MAX_IDLE_CONNS`    | 最大空闲连接数                        | `sqlite=2` / `postgres=5`  |
| `LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME` | 连接最大寿命（Go duration，如 `30m`） | `postgres=30m`             |
| `LINGUAFLOW_JWT_SECRET`                 | JWT 签名密钥，生产环境务必修改        | 内置开发用密钥             |
| `LINGUAFLOW_ADMIN_USERNAME`             | 管理员用户名                          | -                          |
| `LINGUAFLOW_ADMIN_PASSWORD`             | 管理员密码                            | -                          |

### 数据库配置

LinguaFlow 支持 **SQLite** 和 **PostgreSQL** 两种数据库：

| 驱动       | 适用模式              | 说明                                                  |
| ---------- | --------------------- | ----------------------------------------------------- |
| `sqlite`   | 本地模式 / 服务器模式 | 默认驱动，零配置，单文件部署；本地模式强制使用 SQLite |
| `postgres` | 仅服务器模式          | 高并发场景；需自行准备 PostgreSQL 实例                |

#### SQLite（默认）

无需额外配置，数据库文件自动创建在 `data_dir/linguaflow.db`，并启用外键、WAL 日志和忙等待。若需指向自定义路径，可通过环境变量间接控制：

```bash
export LINGUAFLOW_DATA_DIR=/var/lib/linguaflow
linguaflow serve
```

#### PostgreSQL

通过环境变量切换到 PostgreSQL：

```bash
export LINGUAFLOW_DATABASE_DRIVER=postgres
export LINGUAFLOW_DATABASE_DSN='postgres://user:password@localhost:5432/linguaflow?sslmode=disable'
linguaflow serve
```

::: tip 连接串格式
`LINGUAFLOW_DATABASE_DSN` 使用 PostgreSQL 标准 URI，例如 `postgres://user:pass@host:5432/dbname?sslmode=require`，也支持键值对形式 `host=... user=... password=... dbname=... sslmode=...`。
:::

::: warning 本地模式不支持 PostgreSQL
`linguaflow local` 始终使用 SQLite，以便单文件零依赖运行。多用户需求请使用 `linguaflow serve`。
:::

::: tip 自动迁移与并发安全
启用 `auto_migrate`（默认开启）时，PostgreSQL 实例会通过 `pg_advisory_lock` 串行化 schema 迁移，多个 LinguaFlow 实例可同时连接同一数据库而不会在启动阶段产生冲突。
:::

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

从 [GitHub Releases](https://github.com/MeowSalty/LinguaFlow/releases) 下载对应平台的二进制文件。

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
```

```powershell [Windows]
.\linguaflow.exe
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

启动后访问 `http://localhost:18080`，如果看到 LinguaFlow 界面即表示安装成功。

## 下一步

- 阅读 [使用模式](/zh/guide/modes) 了解本地模式和服务器模式
- 阅读 [配置](/zh/guide/configuration) 了解详细配置选项
