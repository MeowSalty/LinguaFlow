# 管理员后台

服务器模式下，LinguaFlow 提供一套管理员接口与界面，用于多用户管理、系统设置、运行时监控与操作审计。本地模式为单用户，无需管理员能力，本页内容不适用。

::: tip 适用范围
管理员后台仅在**服务器模式**（`linguaflow serve`）下可用。本地模式自动以 `local` 用户身份运行，不存在多用户场景。
:::

## 成为管理员

管理员的判定是用户表中的 `role` 字段为 `admin`。获得管理员身份的方式：

- **首次初始化** — 空实例首次启动时通过 `bootstrap.admin` 输入（环境变量 `LINGUAFLOW_BOOTSTRAP_ADMIN_*` 或部署文档）创建初始管理员；也可用 `linguaflow admin initialize` 命令显式初始化（详见 [使用模式 · 管理员配置](/zh/guide/modes#管理员配置)）。
- **管理员追加** — 已初始化的实例可由维护命令 `linguaflow admin create` 追加管理员；已登录的管理员也可在用户管理中把其他用户的角色改为 `admin`。
- **管理员找回** — 初始管理员密码丢失或账户被停用时，用 `linguaflow admin recover` 将已有账户恢复为活跃管理员、重置密码并吊销其刷新令牌。

::: warning 注册不再自动产生管理员
公开注册一律创建普通用户；旧版「首个注册用户自动成为管理员」（`registration.auto_admin`）行为已移除。
:::

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
| 重置密码 | 为用户设置新密码（至少 8 个字符） |

创建用户与重置密码时，密码长度至少 8 个字符，邮箱需包含 `@`。用户名重复会返回 `409 conflict`。

对应接口见本页 [API 速览](#api-速览)。

## 系统设置

管理端设置页（`/admin/settings`）当前提供一个**开放注册**开关：

- 开关开启后，注册页接受新用户注册；关闭时注册接口返回 403，注册页显示「注册已关闭」提示
- 注册政策存储在数据库中，跨重启持久化；修改即时生效，无需重启服务
- 首次初始化时的默认值由部署文档的 `bootstrap.registration_enabled` 决定（默认 `false`，详见 [配置文件与环境变量](/zh/guide/configuration#bootstrap-首次初始化顶层)）

设置页还提供**任务历史保留**策略卡:

| 项 | 说明 |
| --- | --- |
| **自动清理** | 开关,默认关闭 |
| **保留天数** | `1`–`3650` 天,默认 `30`;翻译与术语同步任务共用同一周期 |
| **预览影响** | 只读预览:候选 / 到期 / 可删 / 受屏障 / 收尾中数量与关联规模,不触发任何删除 |
| **运行状态** | `disabled` / `idle` / `running` / `blocked` / `error`,含最近扫描时间、积压与跳过原因;后台每小时扫描一次 |

保存需带上读取时的 `revision` 做乐观锁,不一致会返回 `409 settings_conflict`,避免覆盖他人的并发修改;缩短保留期或启用前会二次确认。任务计时起点取真实结束时间,旧记录无法确认结束时间时按保守起点,**缺少计时起点的记录会暂停自动清理**(重试会清空结束时间并重新计时)。

::: warning 与部署配置的关系
此处的「系统设置」是数据库内的运行期政策，与 `server.yaml` 部署文档 / 环境变量是两套机制：部署配置在启动时加载，决定监听端口、数据库、密钥等基础设施行为；设置页面向可在线调整的运行期政策。初始化完成后，数据库中的注册政策**优先于** `bootstrap.registration_enabled`。
:::

## 存储管理

管理员后台的 **存储管理** 页负责站点级对象存储：新建存储连接与空间、管理访问授权（密钥只写不回显）、执行只读 / 写入检测、启停连接与空间、查看容量与缺失 / 损坏对象等健康诊断，以及设定全站**存储政策**(仅站点托管 / 站点托管或自有存储 / 必须使用自有存储,含默认选择、逻辑容量上限与新站点空间默认配额)。

页面与操作详见 [存储管理](/zh/guide/storage)；部署侧的 `server.storage.*` 配置见 [配置文件与环境变量](/zh/guide/configuration#server-storage-—-对象存储)。

## 运行时监控

管理员后台提供 **运行时监控** 页，展示当前实例的实时运行状态：

- **实例信息** — 实例 ID、启动时间、运行时长
- **执行器（runner）** — 翻译与术语同步各一条：恢复队列深度与恢复错误数、任务队列容量与等待数、worker 容量、存活与占用数
- **并发限流器** — 各类资源池的占用情况
- **外部 HTTP 请求遥测** — 按 AI 服务商 / 操作 / 结果分类的请求次数与耗时统计，便于排查上游异常

页面自动轮询刷新；数据短暂不可用时显示「陈旧数据」警告，而不是清空展示。此页数据仅管理员可见。

## 服务重启与任务恢复

服务器重启（升级、维护）后，中断的运行中任务会自动重置为待执行并从断点续跑，已完成的轮次与段落不重跑；恢复失败的任务会进入降级重试状态并在任务中心可见。管理端首页提供失败 / 暂停任务的**恢复列表**，可一键重新入队。

## 凭据加密密钥轮换

服务器模式数据库中的所有 AI 密钥都用**凭据加密密钥**（master key 或 keyring 文件）加密。怀疑泄露或定期治理时可按以下流程轮换（详见 [CLI 命令参考 · secrets](/zh/guide/cli#secrets-命令)）：

1. **生成新 keyring** — 原先用单 master key 的部署先用 `secrets keyring init --from-master-key-env` 把现有密钥收入 keyring；已有 keyring 的直接 `secrets keyring rotate` 生成含旧密钥与新 active key 的新文件（不改数据库、不覆盖旧文件）
2. **切换部署并重启** — 把 `LINGUAFLOW_CREDENTIALS_KEYRING_FILE` 指向新文件，移除旧 master key 输入，重启所有使用该数据库的服务进程；新写入用新密钥，旧数据仍可读
3. **重加密存量数据** — `linguaflow admin credentials reencrypt --config server.yaml` 逐行重加密；中途失败可修复后重跑，已完成的行自动跳过
4. **保留旧密钥** — 命令不会删除旧密钥；历史数据库备份仍依赖它们，请与新密钥一同归档

::: danger 密钥丢失不可恢复
凭据加密密钥一旦丢失，数据库中已保存的 AI 密钥将**永久无法解密**（任务执行需重新录入密钥）。请务必把密钥材料与数据库备份成对保存。
:::

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
| `job.history_deleted` | 删除单条翻译任务记录 | `job` |
| `glossary.sync_task_history_deleted` | 删除单条术语同步任务记录 | `glossary` |
| `admin.task_retention.update` | 修改任务历史保留策略 | `system_settings` |

::: tip 即时翻译为何也记录
即时翻译本身不落库译文，但仍会消耗 LLM 配额并可能触发限流。将其记入审计日志，便于管理员核算用量与排查异常。
:::

随着功能迭代，动作列表可能扩展。前端对未知 `action` 会原样显示字符串，因此集成外部监控时建议按前缀（如 `job.`、`segment.`）做宽松匹配，而非精确等于。

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
| `GET` | `/admin/settings` | 读取注册开关等系统设置 |
| `PATCH` | `/admin/settings` | 更新系统设置（如 `registration_enabled` 布尔开关） |
| `POST` | `/admin/task-retention/preview` | 预览任务历史保留策略影响(只读,不触发删除) |
| `GET` | `/admin/task-retention/status` | 任务历史清理运行状态与本进程扫描摘要 |
| `GET` | `/admin/storage/policy` | 读取站点存储政策（含可用政策模式与限制原因） |
| `PUT` | `/admin/storage/policy` | 整体保存站点存储政策（`generation` 乐观锁） |
| `GET` | `/admin/storage/diagnostics` | 存储只读诊断（容量、缺失 / 损坏对象、恢复积压等） |
| `GET` | `/admin/storage/connections` | 列出站点级存储连接 |
| `GET` | `/admin/runtime/summary` | 运行时监控摘要（执行器、队列、限流器与外部请求遥测） |

字段与响应结构的权威定义见 [OpenAPI 规范](/zh/api/#openapi-规范) 与 Redoc。

## 相关文档

- [使用模式](/zh/guide/modes) — 服务器模式部署、初始管理员与密钥
- [存储管理](/zh/guide/storage) — 站点存储、政策与自有存储
- [配置文件与环境变量](/zh/guide/configuration) — 部署文档与 `bootstrap.*` 配置项
- [CLI 命令参考](/zh/guide/cli) — `secrets` / `admin` 维护命令
- [API 参考](/zh/api/) — 接口总览与 Redoc 入口
