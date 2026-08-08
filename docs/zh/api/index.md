# API 参考

LinguaFlow 提供 RESTful API，便于与外部系统集成。个人使用 Web / CLI 即可，不必先读本页。

## 基础信息

| 项目                                 | 说明                                                   |
| ------------------------------------ | ------------------------------------------------------ |
| Base URL（本地模式）                 | `http://127.0.0.1:18080/api/v1`                        |
| Base URL（服务器模式 / Docker 默认） | `http://localhost:8080/api/v1`                         |
| 认证方式                             | 本地模式通常无需认证；服务器模式为 Bearer Token（JWT） |
| 内容类型                             | `application/json`                                     |

::: tip 认证

- **本地模式**：免登录，适合本机调用
- **服务器模式（预览）**：需要 JWT；多用户能力仍在完善  
  :::

下文示例默认使用 **本地模式** 地址。若使用 Docker / `serve`，请把主机与端口换成 `8080`，并在需要时加上 `Authorization` 头。

## 完整 API 文档

完整的交互式 API 文档请访问：

**[LinguaFlow API 文档](/redoc/index.html){target="_blank"}**

<!-- PLACEHOLDER_QUICK_REF -->

## 常用场景（curl）

以下仅作集成入门；字段以 OpenAPI / Redoc 为准。

### 1. 探测运行模式

```bash
curl -s http://127.0.0.1:18080/api/v1/mode
```

用于前端或脚本判断本地 / 服务器模式。

### 2. 列出项目

```bash
curl -s http://127.0.0.1:18080/api/v1/projects
```

### 3. 服务器模式登录（预览）

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "password"}'
```

将返回中的 access token 用于后续请求：

```bash
curl -s http://localhost:8080/api/v1/projects \
  -H "Authorization: Bearer <token>"
```

### 4. 创建项目（示意）

具体 JSON 字段以 OpenAPI 为准，常见需要名称与语言方向，例如：

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/projects \
  -H "Content-Type: application/json" \
  -d '{"name":"demo","source_lang":"en","target_lang":"zh-Hans"}'
```

### 5. 列出 AI 后端

```bash
curl -s http://127.0.0.1:18080/api/v1/backends
```

### 6. 探测可用模型列表

使用当场提供的凭据向服务商拉取模型列表（**不落库**），用于填写创建后端时的 `options.model`：

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/backends/models \
  -H "Content-Type: application/json" \
  -d '{"type":"openai","api_key":"sk-...","base_url":""}'
```

成功时返回 `items` 数组，每项含 `id`（可直接写入 `model`）与 `name`。`type` 为 `openai` / `anthropic` / `google`；`base_url` 可选。

::: tip 创建后端时
`CreateBackendRequest` / `UpdateBackendRequest` 的 `options` 与其中的 `model` 均为 **必填**，不再有隐式默认模型名。
:::

上传资源、创建作业等涉及 multipart 或较长请求体，建议直接对照 **Redoc** 中的对应接口与示例。

### 7. 即时翻译（单段，不落库）

把单段文本交给某份执行计划，同步返回译文、质量标记、各轮摘要、批次诊断与用量。译文**不落库**，适合零散翻译或脚本集成。

```bash
curl -s -X POST http://127.0.0.1:18080/api/v1/quick-translate \
  -H "Content-Type: application/json" \
  -d '{
    "source_text": "Hello, world.",
    "source_lang": "auto",
    "target_lang": "zh",
    "execution_plan_id": 1
  }'
```

常用可选字段：

| 字段                 | 说明                                                                       |
| -------------------- | -------------------------------------------------------------------------- |
| `project_id`         | 可选。提供时复用该项目的术语表与语言配置，并校验访问权                      |
| `glossary`           | 内联临时术语表数组（`source`/`target` 必填，可选 `forbidden`/`mandatory` 等）。项目场景下叠加在项目术语表之上 |

响应中的 `round_summary[].status` 可能为 `success` / `partial` / `failed` / `skipped`（多轮计划后续轮次因 `segment_filter` 跳过）。并发与超时由服务端 `quick_translate` 配置控制，见 [配置文件与环境变量 · 即时翻译](/zh/guide/configuration#server-quick-translate-即时翻译)。

## 错误码

| 状态码 | 说明               |
| ------ | ------------------ |
| 200    | 成功               |
| 201    | 创建成功           |
| 400    | 请求参数错误       |
| 401    | 未认证             |
| 403    | 无权限             |
| 404    | 资源不存在         |
| 409    | 冲突（如重复资源） |
| 422    | 语义/校验错误      |
| 500    | 服务器内部错误     |

## OpenAPI 规范

完整的 OpenAPI 3.0 规范文件：

- 多文件规范：仓库内 `api/openapi/` 目录
- 合并后规范：`api/openapi/openapi-3.0.yaml`（亦可能由文档站 `public/openapi/` 提供）

::: tip 自动生成
前端 TypeScript 类型和后端 Go 代码均基于 OpenAPI 规范自动生成。集成时请以规范与 Redoc 为权威来源。
:::

## 相关文档

- [快速开始 · Web](/zh/guide/getting-started) — 界面流程
- [快速开始 · CLI](/zh/guide/cli-quickstart) — 不经过 HTTP 的批处理
- [使用模式](/zh/guide/modes) — 本地 / 服务器与认证差异
