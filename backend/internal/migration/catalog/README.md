# 统一迁移工具交付合同

本目录随源码入库；`list` 内置 [验收记录](acceptance.json) 与逐库/模式路线，退役依据见 [COMPAT register](compatibility.md)。完整维护手册与执行记录在 `backend/docs/migration.md`、`backend/docs/migration-acceptance.md`；即使本地文档被忽略，本目录仍保留必要支持范围、操作步骤和恢复责任。

发布基线核对日期 2026-10-22，实施及最终验收日期 2026-10-23。源发布基线 [v0.15.0](https://github.com/MeowSalty/LinguaFlow/releases/tag/v0.15.0)，固定 commit `6b264ad5d8b88295c397204eacaa0838cfc97a69`。目标 AuthFormatVersion=1、CurrentDataVersion=3，正式发布版本未指定。源 schema/DTO 在 `upgrade/v015/source` 冻结；执行计划绑定完整源指纹、账号映射、配置/密钥及原件摘要、目标 schema 摘要和执行二进制 SHA-256。

| 能力（本地对象） | SQLite serve | SQLite local | PostgreSQL serve |
| --- | --- | --- | --- |
| v0.15 账号/业务转换 | supported | supported | unverified |
| 存储导入与 ManifestVersion=1 | supported | supported | unverified |
| 升级/导入所需的 DB、密钥、对象恢复材料 | supported | supported | unverified |

SQLite 各格在 Windows 10 amd64 / Go 1.26.10 独立验收，其他操作系统实际执行尚未验收。真实 S3 对象恢复和 COMPLIANCE 版本保留均为 `unverified`，协议替身测试不能提高该状态。local 模式使用 SQLite。v0.13/v0.14 历史路线为 `archived_unverified`，不承诺完整升级链。

唯一离线程序为 `linguaflow-migrate`，旧第二二进制、旧 Task 和旧独立 CLI 包已直接删除。正常 `linguaflow` 不依赖 `internal/migration`。在项目根目录执行以下 Task；`CLI_ARGS` 中所有文件路径必须显式选择并使用绝对路径，示例仅为占位值。

访问源数据库前必须显式选择配置文件、数据库 DSN 或数据目录；相关环境变量也属于显式输入。没有这些输入时，工具在打开数据库及读取无关凭据前拒绝，不会选中默认 `backend/data`。`list` 和帮助命令无需数据库输入。

```powershell
task backend:migrate:list
task backend:migrate:inspect CLI_ARGS='--mode serve --config C:/Migration/source.yaml'
task backend:migrate:build
task backend:migrate:plan CLI_ARGS='--mode serve --config C:/Migration/source.yaml --operation-dir C:/Migration/run-01 --target-data-dir C:/Migration/new-instance --account-map C:/Migration/accounts.json'
task backend:migrate:rehearse CLI_ARGS='--operation-dir C:/Migration/run-01 --offline'
task backend:migrate:apply CLI_ARGS='--operation-dir C:/Migration/run-01 --offline'
task backend:migrate:status CLI_ARGS='--operation-dir C:/Migration/run-01'
task backend:migrate:verify CLI_ARGS='--operation-dir C:/Migration/run-01'
```

生产操作应固定使用构建后的同一个二进制；把命令中的 Task 换成 `backend/bin/linguaflow-migrate.exe upgrade plan` 等对应 CLI。Task 的 `go run` 也绑定其实际编译产物，代码或构建参数变化后不能接管旧计划。更换工具时保留旧二进制及 operation，不能编辑 build/schema 摘要绕过限制。

账号映射以用户 ID 为键，仅允许 username/email 调整；未给出的用户按原值规范化，计划始终冻结完整用户集合。示例：

```json
{"2":{"username":"alice"},"3":{"email":null},"4":{"email":"person@example.invalid"}}
```

`email:null` 清空数据库邮箱，密码、角色、active、用户 ID、归属、密文和 key ID 均保持原值。规范化冲突或保留名返回 `mapping_required`；坏 bcrypt/混合或未知 schema 在写入前拒绝，不能改角色或密码“修复”映射。原 local 管理员仍是原 ID 和 `local`。转换建立密码/用户名登录、禁用邮箱登录、保留注册开关并要求注册邮箱；验证证据为空、auth_version=1，旧 refresh token 清除于目标。

SQLite `target-data-dir` 必须完全不存在，工具在同卷独立 staging 验证后无覆盖发布，绝不覆盖源或已有目标。源库、目标、operation 和引用本地根不得重叠；链接、junction、短路径别名和路径身份变化被拒绝。PostgreSQL 使用 `--target-config` 指定独立空目标库及新文件根；`--rehearsal-config` 是另一个独立库。需完整版本一致的 pg_dump/pg_restore 和匹配服务器 major，可用 `LINGUAFLOW_MIGRATION_POSTGRES_BIN` 指定路径。工具不切换 DSN。

PostgreSQL 有限路线要求同一个明确 Host/Port，允许该端点的 TLS fallback，拒绝多主机或无法在外部 dump/restore 精确复现的连接选项；继承的 PGHOSTADDR/service 等不会进入子进程。空目标与源探测拒绝额外类型、函数、扩展及其他 catalog 对象。源指纹包含 serial/identity 序列归属、参数和实际分配状态；SQLite 同样绑定 sqlite_sequence。固定 CHECK 以保留逻辑分组的有限语法核对，不能通过追加 OR true 放宽原发布约束。

apply/rehearse/resume/rollback 都需要 `--offline`，表示正常服务、worker、GC 和其他写者已停。operation 使用私有权限的 `plan.json`、`journal.json`、`report.json`；计划不可变，日志可原子更新，DB 业务步骤与回执同事务，恢复按回执核对日志滞后。部分目标为 preparing，正常 database 启动在任何自动格式/schema/data 写入前拒绝 preparing、abandoned 和未知状态；合法普通库可无迁移记录。最终验证才同事务提交账号标记、data_version=3 和 ready。

升级自动在源低层只读适配器上一致备份，不在源创建政策、账号标记、pin 或迁移状态。本地被引用文件形成独立字节副本；源已丢失或来源不可证明时继续保留明确的领域事实，不能宣称这些原件已恢复。S3 引用必须用 `--external-backup-receipt` 提供版本 1 外部 JSON，字段如下：

```json
{
  "version":1,"source_fingerprint":"<64-hex>",
  "objects":[{"location_id":1,"space_id":1,"key":"source.txt","source_version":"<version>","size":5,"sha256":"<64-hex>","recovery_file":"C:/IndependentRecovery/source.txt","retain_until":"<future RFC3339>","retention_evidence":"<retention responsibility and evidence>"}]
}
```

每个精确对象均绑定位置、key、源版本、大小/摘要和保留证据；`recovery_file` 与 `immutable_version` 二选一。独立文件核验实际字节，不可变版本核验实际字节、非 null version 和服务端 Object Lock COMPLIANCE retain-until。缺失、篡改、重复或未声明材料返回 `backup_incomplete`。GC pin 和“已备份”的声明不能作为独立恢复证明。S3 bucket/prefix 冲突阻断整体 ready。

```powershell
task backend:migrate:resume CLI_ARGS='--operation-dir C:/Migration/run-01 --offline'
task backend:migrate:rollback CLI_ARGS='--operation-dir C:/Migration/run-01 --offline'
task backend:migrate:storage CLI_ARGS='plan --config C:/Migration/current.yaml --operation-dir C:/Migration/storage-01'
task backend:migrate:storage CLI_ARGS='plan --config C:/Migration/current.yaml --operation-dir C:/Migration/storage-02 --manifest C:/Migration/published-manifest.json'
task backend:migrate:storage CLI_ARGS='apply --operation-dir C:/Migration/storage-01 --offline'
```

Storage plan 接受当前完整账号/业务格式，已有完整 SourceRevision 保留，仅为尚未导入的 legacy 引用制定子计划。原 ManifestVersion=1 只读，新的日志绑定原摘要和 operation ID。逐资源事务、确定 Blob 身份和代次保证恢复不重复转换；存储 rollback 拒绝后续编辑、新任务快照、pin 或代次变化。版本 rollback 保留所有源、目标和材料，给出恢复时间点，由操作者选择源发布和原目录/DSN；新部署后写入不回灌旧 schema。

通用实例备份已从统一迁移 CLI、Task 和 catalog 路线中移除。迁移自身仍会在升级/存储导入前生成并核验与 operation 绑定的恢复材料；`internal/storagebackup` 继续维护已发布 manifest version 1、对象引用/密钥校验和 BackupPin 消费链。B1 的独立维护入口尚未交付，不能把迁移工具当作当前实例备份命令使用。

验收命令为 `backend:migrate:test`、`backend:migrate:test:binary`、`backend:migrate:check-dependencies` 和后端 format/lint/test/race/build。真实 PostgreSQL 发布夹具需要三个专用空库环境变量 `LINGUAFLOW_MIGRATION_TEST_POSTGRES_SOURCE`、`_TARGET`、`_REHEARSAL`；不配置会明确 SKIP/unverified。实例与材料必须专用隔离，不能使用维护者的现存路径。交接给发布维护者：唯一离线产物及校验和、正式目标版本、部署目录/DSN切换和原管理员/项目/资源/任务/凭据的业务验收。

本次 Windows 后端 format/lint/test/race/build、迁移专项、独立进程 SQLite serve/local 和服务依赖隔离均已通过，实际结果随 acceptance.json 编译入 `list`。另可用专用隔离库 `LINGUAFLOW_MIGRATION_TEST_POSTGRES_CATALOG_DSN` 验证 enum/domain/function 拒绝，DDL 在事务中回滚；未配置为 SKIP，不能提高 PostgreSQL 支持状态。

历史冻结树为 v0.14.0 commit `4bc4577cd669d9ccbbc4980f19e259b5beff2b18`，v0.13.0 peeled commit `99cb87dea19a7fcc61b6c267f916bfe046b2d355`。当前 v013 正文和 journal 读取保留于 `upgrade/v013` 且仍拒绝；只有精确可获取可运行的归档产物及中断恢复验收通过，才能删除正文。Git 历史链接不能替代这两项证明。
