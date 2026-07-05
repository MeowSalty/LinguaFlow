# API 参考

::: warning 注意
此页面正在编写中，内容可能会有变动。
:::

LinguaFlow 提供 RESTful API 用于翻译管理。详细的 API 文档将在后续版本中提供。

## 基础信息

- Base URL: `http://localhost:8080/api/v1`
- 认证方式: Bearer Token

## 主要端点

- `GET /translations` - 获取翻译列表
- `POST /translations` - 创建翻译
- `GET /translations/:id` - 获取单个翻译
- `PUT /translations/:id` - 更新翻译
- `DELETE /translations/:id` - 删除翻译
