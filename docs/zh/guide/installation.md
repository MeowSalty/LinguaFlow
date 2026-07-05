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

创建 `docker-compose.yml` 文件：

```yaml
version: "3.8"

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
      - LINGUAFLOW_PORT=8080
      - LINGUAFLOW_DB_PATH=/app/data/linguaflow.db

volumes:
  linguaflow-data:
```

启动服务：

```bash
docker compose up -d
```

### 环境变量配置

| 变量名                      | 描述           | 默认值                 |
| --------------------------- | -------------- | ---------------------- |
| `LINGUAFLOW_PORT`           | 服务监听端口   | `8080`                 |
| `LINGUAFLOW_DB_PATH`        | 数据库文件路径 | `./data/linguaflow.db` |
| `LINGUAFLOW_ADMIN_USERNAME` | 管理员用户名   | -                      |
| `LINGUAFLOW_ADMIN_PASSWORD` | 管理员密码     | -                      |

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
