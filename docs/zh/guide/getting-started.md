# 快速开始

本指南将帮助您快速上手 LinguaFlow。

## 系统要求

- Go 1.21 或更高版本
- Node.js 20 或更高版本
- pnpm 包管理器

## 安装

### 从源码构建

```bash
# 克隆仓库
git clone https://github.com/MeowSalty/LinguaFlow.git
cd LinguaFlow

# 安装依赖
task backend:install
task frontend:install

# 构建
task backend:build
```

### Docker 部署

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -p 8080:8080 ghcr.io/meowsalty/linguaflow:latest
```

## 配置

LinguaFlow 支持通过环境变量和配置文件进行配置。请参阅 [配置指南](/zh/guide/configuration) 了解更多详情。

## 下一步

- 阅读 [配置指南](/zh/guide/configuration) 了解详细配置选项
- 查看 [API 参考](/zh/api/) 了解可用的 API 端点
