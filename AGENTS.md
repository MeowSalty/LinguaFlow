# AGENTS.md — LinguaFlow 开发指南（frontend 分支）

当前处于 **frontend 分支**。本分支专注前端，仓库树中**不包含** `backend/`。

| 路径 | 权限 |
|------|------|
| `frontend/` | 可读写，本分支唯一开发目录 |
| `api/` | **只读**，由 backend 分支维护；仅同步，不在此改规范 |
| `backend/` | **禁止存在**；若合并后再次出现，必须立即删除 |

联调后端请使用独立 worktree（如 `LinguaFlow-backend`），不要在本分支恢复 `backend/`。

## 项目概述

LinguaFlow 是一个多语言翻译平台，采用前后端分离架构。

| 层级 | 目录 | 技术栈 |
|------|------|--------|
| 前端 | [`frontend/`](frontend/) | Vue 3 + TypeScript + Vite + Pinia + naive-ui |
| 后端 | `backend/`（仅 main / backend 分支） | Go + ent (ORM) + chi (路由) + SQLite |
| API 规范 | [`api/`](api/) | OpenAPI 3.0 (规范先行) |

## 分支路径所有权（长期约定）

| 分支 | 树中应有 | 禁止 |
|------|----------|------|
| `main` | `frontend/` + `backend/` + `api/` | — |
| `frontend` | `frontend/` + `api/` | `backend/` |
| `backend` | `backend/` + `api/` | `frontend/` |

### 合并铁律

1. **`frontend` → `main`**：只合入前端改动；**绝不能**删除或改写 main 上的 `backend/`。
   - 推荐在 main worktree 使用：`pwsh -File scripts/merge-frontend-into-main.ps1`
   - 或合并后执行：`git checkout HEAD -- backend` 再提交
2. **`main` → `frontend`**：同步后若再次出现 `backend/`，立即 `git rm -r backend` 并提交，保持本分支无后端代码。
3. **`api/`**：规范变更在 backend 侧完成并合入 main 后，再通过同步进入 frontend；本分支不直接改 OpenAPI 源文件。

### 从 main 同步到本分支

```powershell
# 推荐：使用脚本（merge + 自动清理 backend/）
pwsh -File scripts/sync-main-into-frontend.ps1

# 或手动
git fetch origin
git merge origin/main
if (Test-Path backend) { git rm -r backend; git commit -m "chore: 同步 main 后保持前端分支无 backend" }
```

### 合入 main（保留 backend/）

```powershell
# 在 main worktree 中
pwsh -File scripts/merge-frontend-into-main.ps1
```

## 目录结构（本分支）

```
.
├── api/                 # API 规范 (只读，OpenAPI YAML)
├── frontend/            # Vue 3 前端应用
│   └── src/             # 源码 (pages, components, stores)
├── scripts/             # 分支维护脚本
├── Taskfile.yml         # 任务运行器配置
└── AGENTS.md            # 本文件
```

## API 规范

项目采用**规范先行**原则：

- **源文件**: [`api/openapi/base.yaml`](api/openapi/base.yaml) — 变更应在 backend 流程中完成，本分支只读
- **生成文件**: [`api/openapi/openapi-3.0.yaml`](api/openapi/openapi-3.0.yaml) — **不可手动编辑**，由 `task openapi:bundle` 生成
- 前端类型：`task frontend:openapi:generate`

## 常用命令

项目使用 [Task](https://taskfile.dev) 作为任务运行器，所有命令通过 `task` 执行。

### OpenAPI 相关

| 命令 | 说明 |
|------|------|
| `task openapi:bundle` | 合并多文件 YAML 为单文件 `openapi-3.0.yaml` |
| `task frontend:openapi:generate` | 生成前端 TypeScript 类型定义 |

### 前端

| 命令 | 说明 |
|------|------|
| `task frontend:install` | 安装依赖 (pnpm) |
| `task frontend:dev` | 启动开发服务器 |
| `task frontend:build` | 构建生产版本 |
| `task frontend:type-check` | TypeScript 类型检查 |
| `task frontend:lint` | 代码检查 (oxlint + eslint) |
| `task frontend:format` | 格式化代码 (oxfmt) |

后端相关 `task backend:*` 仅在 backend / main worktree 中使用。

## 开发工作流

1. 需要最新 API 时：从 main 同步本分支（见上文脚本），再 `task frontend:openapi:generate`
2. 启动后端：在 backend worktree 执行 `task backend:dev`
3. 启动前端：`task frontend:dev`（Vite 代理 API 到后端）
4. 提交前：`task frontend:lint`；确认 diff 仅含 `frontend/`（及必要的分支维护文件）
5. 提 PR 到 main：确保未修改/删除 `backend/**`
