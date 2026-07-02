<div align="center">

<!-- TODO: Logo 还没画好，先丢这里吧 .github/assets/logo.svg -->
<!-- <img src=".github/assets/logo.svg" alt="LinguaFlow" width="128" /> -->
<img src="https://img.shields.io/badge/LinguaFlow-6366f1?style=for-the-badge&logo=googletranslate&logoColor=white" alt="LinguaFlow" />

# LinguaFlow

**AI 驱动的多语言翻译工作台**

[![前端 CI](https://github.com/MeowSalty/LinguaFlow/actions/workflows/ci-frontend.yml/badge.svg)](https://github.com/MeowSalty/LinguaFlow/actions/workflows/ci-frontend.yml)
[![后端 CI](https://github.com/MeowSalty/LinguaFlow/actions/workflows/ci-backend.yml/badge.svg)](https://github.com/MeowSalty/LinguaFlow/actions/workflows/ci-backend.yml)
[![Release](https://github.com/MeowSalty/LinguaFlow/actions/workflows/release.yml/badge.svg)](https://github.com/MeowSalty/LinguaFlow/actions/workflows/release.yml)
[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](LICENSE)

</div>

LinguaFlow 帮助你将文档、字幕、电子书等内容翻译成多种语言。上传源文件，配置 AI 后端，即可开始批量翻译。支持术语管理、翻译审核、进度追踪，让翻译工作更高效、更准确。

---

## 支持的文件格式

| 格式 | 扩展名 |
|------|--------|
| Markdown | `.md` `.markdown` `.mdx` |
| EPUB 电子书 | `.epub` |
| SRT 字幕 | `.srt` |
| VTT 字幕 | `.vtt` |
| ASS 字幕 | `.ass` |
| 纯文本 | `.txt` |

---

## 核心功能

### 项目管理

以项目为单位组织翻译工作。每个项目设置源语言和目标语言，上传源文件后即可创建翻译任务。

- 创建、编辑、删除项目
- 支持中、英、日、韩、法、德、西等数十种语言互译
- 项目级别的术语表管理

### 智能翻译

集成主流 AI 翻译服务，支持自定义翻译策略和提示词。

- 支持 OpenAI、Anthropic、Google Gemini
- 自动检测源语言
- 上下文感知，保持段落间连贯性
- 代码块、链接、占位符等特殊内容自动保护

### 批量处理

一次翻译整个目录的文件，支持并发处理和增量更新。

- 拖拽上传文件或文件夹
- 增量更新：保留已有译文，仅翻译新增内容
- 并发翻译，充分利用多核性能
- 实时进度追踪，预估剩余时间

### EPUB 电子书

专门针对 EPUB 电子书的翻译支持。

- 按章节浏览和翻译
- 章节级翻译进度追踪
- HTML 内容预览
- 保留原始格式和结构

### 术语管理

创建术语表确保专业词汇翻译一致。支持自动术语提取，让 AI 帮你发现关键术语。

- CSV 格式导入导出
- 自动术语提取（Bootstrap）
- 术语修改后自动同步已翻译段落

### 翻译审核

逐段查看翻译结果，支持行内编辑和批量审核。

- 按状态筛选（待翻译/已翻译/已编辑/已批准/已驳回）
- 行内编辑译文
- 批量通过/拒绝
- 添加备注

### 灵活配置

自定义翻译流水线的每个环节。

- **提示词模板**：定义 AI 翻译的系统指令
- **翻译配置**：分段策略、内容保护、响应修复等
- **执行计划**：组合后端、提示词、配置，定义多轮翻译流程

---

## 两种使用方式

### Web 界面

启动服务后访问 Web 界面，可视化管理翻译工作。

- 项目、资源、任务、术语表的完整管理
- 拖拽上传文件
- 实时任务进度追踪
- 暗色/亮色主题切换

### 命令行工具

CLI 适合集成到自动化流程或批量处理场景。

```bash
# 翻译单个文件
linguaflow translate -i README.md -o README_zh.md --to zh

# 翻译整个目录
linguaflow translate -i ./docs -o ./docs-zh --to zh

# 使用术语表
linguaflow translate -i docs.md -o out.md --to zh --glossary-path terms.csv
```

---

## 快速开始

### 下载预编译版本

从 [GitHub Releases](https://github.com/MeowSalty/LinguaFlow/releases) 下载对应平台的二进制文件。

支持的平台：
- Linux (amd64, arm64)
- macOS (amd64, arm64)
- Windows (amd64, arm64)

每个二进制文件附带 SHA256 校验和，可用于验证文件完整性。

### Docker

```bash
docker pull ghcr.io/meowsalty/linguaflow:latest
docker run -p 8080:8080 ghcr.io/meowsalty/linguaflow:latest
```

### 从源码构建

```bash
git clone https://github.com/MeowSalty/LinguaFlow.git
cd LinguaFlow
task frontend:install
task backend:install
task openapi:generate
task backend:local:build
```

### 启动本地模式

本地模式无需登录，适合个人使用。

**双击运行**：直接双击编译好的 `linguaflow`（或 `linguaflow.exe`）即可启动，会自动打开浏览器。

**命令行启动**：

```bash
./bin/linguaflow local
```

服务默认监听 http://localhost:18080，如果端口被占用会自动递增。

### 首次使用

1. **配置 AI 后端** — 在「AI 后端」页面添加你的 OpenAI/Anthropic/Google Gemini 账号
2. **创建项目** — 在「项目」页面创建新项目，设置源语言和目标语言
3. **上传文件** — 进入项目工作区，拖拽上传源文件
4. **开始翻译** — 选择文件，创建翻译任务
5. **审核译文** — 翻译完成后在「段落」标签页查看和编辑结果

### 启动服务器模式

服务器模式支持多租户和权限管理。

> 半成品，还没做完，不建议使用

```bash
./bin/linguaflow serve
```

---

## 技术架构

| 层级 | 技术栈 |
|------|--------|
| 前端 | Vue 3 + TypeScript + naive-ui |
| 后端 | Go + ent + chi |
| 数据库 | SQLite |
| API | OpenAPI 3.0 |

项目采用前后端分离架构，支持单机部署或分离部署。

---

## 许可证

本项目采用**双许可**模式：

| 许可证 | 适用场景 | 状态 |
|--------|----------|------|
| [GNU AGPL v3](LICENSE) | 开源使用、个人项目、非商业用途 | ✅ 可用 |
| 商业许可 | 商业闭源使用、私有部署 | 🚧 即将推出 |

- **开源用户**：遵循 AGPL v3 协议，修改后的代码需开源
- **商业用户**：如需闭源使用或私有部署，请等待商业许可上线

详情请参阅 [LICENSE](LICENSE) 文件。
