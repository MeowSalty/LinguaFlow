# 管理员后台

服务器模式下，LinguaFlow 提供一套管理员接口与界面，用于多用户管理、系统监控与操作审计。本地模式为单用户，无需管理员能力，本页内容不适用。

::: tip 适用范围
管理员后台仅在**服务器模式**（`linguaflow serve`）下可用。本地模式自动以 `local` 用户身份运行，不存在多用户场景。
:::

## 成为管理员

管理员的判定是用户表中的 `role` 字段为 `admin`。获得管理员身份的方式：

- **启动时指定**：通过环境变量 `LINGUAFLOW_ADMIN_USERNAME` / `LINGUAFLOW_ADMIN_PASSWORD` 在首次启动时创建初始管理员账户（详见 [使用模式 · 管理员配置](/zh/guide/modes#管理员配置)）。
- **自动提升**：未设置上述环境变量时，若 `registration.auto_admin` 为 `true`（默认），首个注册的用户自动成为管理员。
- **既有管理员授权**：已登录的管理员可在用户管理中把其他用户的角色改为 `admin`。

::: warning 末位管理员保护
系统不允许移除最后一个活跃管理员账户。下列操作会被拒绝并返回 `409 conflict`：

- 把唯一活跃管理员的角色降级为 `user`
- 停用唯一活跃管理员账户
  :::

此外，管理员**不能修改自己的角色**、**不能停用自己的账户**，避免误锁自己出局。

## 用户管理

管理员可以管理服务器上的全部用户：

| 操作 | 说明 |
| --- | --- |
| 列出用户 | 支持按用户名 / 邮箱搜索、按角色筛选、按启用状态筛选，分页返回 |
| 查看用户 | 获取单个用户的资料与角色 |
| 创建用户 | 直接创建账户（绕过注册流程），可指定 `role` 为 `user` 或 `admin` |
| 更新用户 | 修改显示名、邮箱、角色、启用状态 |
| 停用用户 | 软删除：置 `active=false`，账户无法再登录，但历史数据保留 |
| 重置密码 | 为用户设置新密码（最少 8 位） |

创建用户与重置密码时，密码长度至少 8 位，邮箱需包含 `@`。用户名重复会返回 `409 conflict`。

对应接口见本页 [API 速览](#api-速览)。

## 系统统计

`/admin/stats` 返回全平台的聚合指标，用于了解整体规模：

| 字段 | 含义 |
| --- | --- |
| `total_users` | 用户总数 |
| `active_users` | 启用中的用户数 |
| `total_projects` | 项目总数 |
| `total_organizations` | 组织总数 |
| `total_jobs` | 作业总数 |
| `total_resources` | 资源文件总数 |

该接口为全局视角，不按用户 / 组织隔离；个人视角的用量（API 调用、Token 消耗等）走 `/stats/summary`。

## 审计日志

审计日志记录平台上的关键写操作：谁（actor）、在何时、对什么资源（resource_type / resource_id）、做了什么（action）。每条记录还附带人类可读的 `message` 与结构化 `metadata`。

LinguaFlow 区分两套活动视图：

| 视图 | 接口 | 可见范围 | 用途 |
| --- | --- | --- | --- |
| **个人活动** | `GET /api/v1/activity` | 仅本人发起的、或属于其所在组织 / 项目的活动 | 普通用户回顾自己的操作 |
| **全局审计** | `GET /api/v1/admin/audit-logs` | 全部活动记录 | 管理员排查、合规追溯 |

两者返回结构一致（`Activity` 模型），但个人活动接口按当前用户的可见范围过滤，全局审计接口需管理员权限。Web 界面在仪表盘展示个人活动，管理员后台展示全局审计日志。

### 分页

两个接口都采用基于记录 `id` 的**反向游标分页**（最新在前）：

| 参数 | 说明 |
| --- | --- |
| `limit` | 每页条数，`1`–`100`，默认 `50` |
| `cursor` | 上一页最后一条的 `id`；省略时从最新一页开始 |

请求下一页时，把当前页最后一条的 `id` 作为 `cursor` 传入即可。`/activity` 的响应中还会给出 `next_cursor` 字符串游标，可直接透传。

### 动作类型

下表列出后端实际记录的 `action` 值。前端会按这些键做颜色分类与中文标签映射；直接消费 API 时可参照下表理解。

| `action` | 触发场景 | 资源类型 |
| --- | --- | --- |
| `job.create` | 创建翻译作业 | `job` |
| `job.cancel` | 取消作业 | `job` |
| `job.retry` | 重试作业 | `job` |
| `segment.approve` | 审校通过单个段落 | `segment` |
| `segment.reject` | 审校驳回单个段落 | `segment` |
| `segment.batch_review` | 批量审核段落 | `resource` |
| `segment.approve_all` | 一键通过全部段落 | `resource` |
| `segment.retranslate_rejected` | 重译被驳回的段落 | `resource` |
| `resource.segment.update` | 手动编辑资源段落 | `segment` |
| `resource.segment.translation_preview.apply` | 应用预览翻译到段落 | `segment` |
| `glossary.sync_execute` | 执行术语表同步 | `glossary` |
| `quick_translate` | 即时翻译（不落库，仅记录用量与事件） | — |

::: tip 即时翻译为何也记录
即时翻译本身不落库译文，但仍会消耗 LLM 配额并可能触发限流。将其记入审计日志，便于管理员核算用量与排查异常。
:::

随着功能迭代，动作列表可能扩展。前端对未知 `action` 会原样显示字符串，因此集成外部监控时建议按前缀（如 `job.`、`segment.`）做宽松匹配，而非精确等于。

## 系统设置

`/admin/settings` 以扁平 `key → value`（均为字符串）的形式读写系统级配置，存储于数据库的 `system_settings` 表，用于跨重启持久化少量运行期参数。

- `GET /admin/settings` —— 返回当前全部键值
- `PATCH /admin/settings` —— 传入待更新的键值对，仅覆盖提供的键，未传入的键保持不变

::: warning 与配置文件的关系
此处的「系统设置」是数据库内的运行期参数，与 `linguaflow.yaml` / 环境变量是两套机制。YAML 与环境变量在启动时加载，决定监听端口、数据库、日志等基础设施行为；系统设置面向可在线调整的运行期参数。两者**不会**自动同步，请勿在此处期望修改端口或数据库连接。
:::

具体可用的设置键随版本演进，以 Redoc 与实际接口返回为准。

## API 速览

管理员接口统一挂在 `/api/v1/admin/*` 下，均需 `Bearer Token` 且要求 `role=admin`，否则返回 `403 forbidden`。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/admin/users` | 列出用户（支持 `search` / `role` / `active` / `cursor` / `limit`） |
| `POST` | `/admin/users` | 创建用户 |
| `GET` | `/admin/users/{userId}` | 获取用户详情 |
| `PATCH` | `/admin/users/{userId}` | 更新用户资料 / 角色 / 启用状态 |
| `DELETE` | `/admin/users/{userId}` | 停用用户 |
| `PUT` | `/admin/users/{userId}/password` | 重置密码 |
| `GET` | `/admin/stats` | 全局统计 |
| `GET` | `/admin/audit-logs` | 全局审计日志（`cursor` / `limit`） |
| `GET` | `/admin/settings` | 读取系统设置 |
| `PATCH` | `/admin/settings` | 更新系统设置 |

字段与响应结构的权威定义见 [OpenAPI 规范](/zh/api/#openapi-规范) 与 Redoc。

## 相关文档

- [使用模式](/zh/guide/modes) — 服务器模式与初始管理员配置
- [配置文件与环境变量](/zh/guide/configuration) — `registration.auto_admin` 等配置项
- [API 参考](/zh/api/) — 接口总览与 Redoc 入口
