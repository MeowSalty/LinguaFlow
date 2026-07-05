# 快速开始

本指南将帮助您在 5 分钟内快速上手 LinguaFlow。

## 安装

::: code-group
```bash [Docker（推荐）]
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -p 8080:8080 ghcr.io/meowsalty/linguaflow:latest
```

```bash [预编译二进制]
# 从 GitHub Releases 下载对应平台的二进制文件
# https://github.com/MeowSalty/LinguaFlow/releases

# Linux / macOS
chmod +x linguaflow
./linguaflow

# Windows（双击运行或命令行）
linguaflow.exe
```

```bash [从源码构建]
git clone https://github.com/MeowSalty/LinguaFlow.git
cd LinguaFlow
task backend:install
task frontend:install
task backend:build
# 构建产物位于 bin/linguaflow
```
:::

## 首次启动

LinguaFlow 启动后，默认以**本地模式**运行，自动打开浏览器访问 `http://localhost:18080`。

::: tip 本地模式
- 预编译二进制 / 源码构建：默认以**本地模式**启动，端口 `18080`，自动打开浏览器
- Docker 部署：默认以**服务器模式**启动，端口 `8080`
- 本地模式无需登录，数据存储在本地 SQLite 数据库中，适合个人使用
- 如需团队协作，请参阅 [使用模式](/zh/guide/modes) 了解服务器模式
:::

## 完整使用流程

以下是使用 LinguaFlow 进行翻译的完整流程：

### 1. 配置 AI 后端

进入顶部导航栏的 **AI 后端** 页面：

1. 点击 **添加后端**
2. 选择 AI 提供商（OpenAI / Anthropic / Google Gemini）
3. 填入 API Key 和相关配置
4. 保存配置

::: info 支持的 AI 后端
| 后端 | 类型标识 | 默认模型 |
|------|----------|----------|
| OpenAI | `openai` | `gpt-4o-mini` |
| Anthropic | `anthropic` | `claude-sonnet-4-5` |
| Google Gemini | `google` | `gemini-2.5-flash` |

OpenAI 类型还兼容 Azure OpenAI、Ollama、LM Studio 等 OpenAI API 兼容服务，通过自定义 `base_url` 即可对接。
:::

### 2. 创建执行计划

进入顶部导航栏 **翻译配置 → 执行计划模板** 页面：

1. 点击 **创建计划**
2. 填写计划名称（如 "默认翻译计划"）
3. 添加翻译轮次，配置以下内容：
   - **后端**：选择第 1 步创建的 AI 后端
   - **提示词模板**：选择内置的「通用提示词」（或自定义模板）
   - **翻译配置**：选择内置的「通用策略」（或自定义配置）
   - **批次大小**：每批翻译的段落数，建议根据所选模型的能力调整。上下文窗口较大的模型可适当增大批次以提升吞吐量，较小的模型则应减小批次避免超出限制
   - **并发数**：同时翻译的并发数，需结合 API 速率限制设置。设得过高会频繁撞上速率限制墙，反而降低整体效率
4. 保存

::: tip 内置模板
LinguaFlow 提供了内置的提示词模板「通用提示词」和翻译配置「通用策略」，适用于大多数场景，可直接使用。但执行计划需要手动创建，因为它需要关联您自己配置的 AI 后端。
:::

### 3. 创建翻译项目

进入顶部导航栏的 **项目** 页面：

1. 点击 **新建项目**
2. 填写项目信息：
   - **项目名称**（必填）
   - **源语言**（默认自动检测）
   - **目标语言**（默认简体中文）
3. 点击项目卡片进入工作区

### 4. 上传资源

在项目工作区的 **资源** 标签页中：

1. 点击上传按钮，选择需要翻译的文件
2. 等待文件解析完成

支持的文件格式：

| 格式 | 扩展名 |
|------|--------|
| Markdown | `.md` / `.markdown` / `.mdx` |
| EPUB | `.epub` |
| 字幕 | `.srt` / `.vtt` / `.ass` |
| 纯文本 | `.txt` |

### 5. 开始翻译

在项目工作区中选择需要翻译的资源或段落：

1. 点击 **翻译** 按钮
2. 在弹出的面板中选择第 2 步创建的执行计划
3. 可选配置：
   - **自动审批**：翻译完成后自动标记为已审批
   - **覆盖模式**：跳过已翻译 / 覆盖未审批 / 覆盖全部
4. 点击 **开始翻译**
5. 在 **任务** 标签页中查看翻译进度

::: tip 翻译进度
翻译任务由后台 Worker 异步执行，支持实时进度追踪。您可以在任务列表中查看每个资源的翻译状态。
:::

## 下一步

- 阅读 [安装部署](/zh/guide/installation) 了解详细的安装方式
- 阅读 [使用模式](/zh/guide/modes) 了解本地模式和服务器模式的区别
- 阅读 [翻译配置](/zh/guide/translation-config) 了解如何优化翻译效果（提示词模板、翻译配置、执行计划等）
