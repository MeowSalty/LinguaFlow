# API 参考

LinguaFlow 提供 RESTful API 用于翻译管理。

## 基础信息

| 项目 | 说明 |
|------|------|
| Base URL | `http://localhost:8080/api/v1` |
| 认证方式 | Bearer Token（JWT） |
| 内容类型 | `application/json` |

::: tip 认证
服务器模式下需要 JWT 认证。本地模式下无需认证。
:::

## 完整 API 文档

完整的交互式 API 文档请访问：

**[LinguaFlow API 文档](/redoc/index.html){target="_blank"}**

<!-- PLACEHOLDER_QUICK_REF -->

## 快速参考

### 认证

```bash
# 登录获取 Token
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "password"}'

# 使用 Token 访问 API
curl -H "Authorization: Bearer <token>" \
  http://localhost:8080/api/v1/projects
```

### 错误码

| 状态码 | 说明 |
|--------|------|
| 200 | 成功 |
| 201 | 创建成功 |
| 400 | 请求参数错误 |
| 401 | 未认证 |
| 403 | 无权限 |
| 404 | 资源不存在 |
| 500 | 服务器内部错误 |

## OpenAPI 规范

完整的 OpenAPI 3.0 规范文件：

- 多文件规范：`api/openapi/` 目录
- 合并后规范：`api/openapi/openapi-3.0.yaml`

::: tip 自动生成
前端 TypeScript 类型和后端 Go 代码均基于 OpenAPI 规范自动生成。
:::
