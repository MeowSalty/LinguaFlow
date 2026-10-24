# 统一迁移工具交付合同

本目录随源码入库；`list` 内置 [验收记录](acceptance.json) 与逐库/模式路线，退役依据见 [COMPAT register](compatibility.md)。完整维护手册与执行记录在 `backend/docs/migration.md`、`backend/docs/migration-acceptance.md`；即使本地文档被忽略，本目录仍保留必要支持范围、操作步骤和恢复责任。

发布基线核对及策略实施日期 2026-10-24，最近发布为 [v0.16.0](https://github.com/MeowSalty/LinguaFlow/releases/tag/v0.16.0)，commit `e2284dea6ccbc69513e7b38aa0755d19271e5af7`。源发布基线仍为 [v0.15.0](https://github.com/MeowSalty/LinguaFlow/releases/tag/v0.15.0)，固定 commit `6b264ad5d8b88295c397204eacaa0838cfc97a69`。目标固定 AuthFormatVersion=1、CurrentDataVersion=3。源 schema/DTO 在 `upgrade/v015/source` 冻结；v2 操作绑定源身份/指纹、账号映射、配置/密钥/原件摘要、业务基线、目标 schema 和执行二进制 SHA-256。

| 数据库/模式 | 操作 | 策略 | 数据库保护 | 状态 |
| --- | --- | --- | --- | --- |
| SQLite serve | upgrade | inplace | auto | supported |
| SQLite local | upgrade | inplace | auto | supported |
| SQLite serve | upgrade | clone | auto | supported |
| SQLite local | upgrade | clone | auto | supported |
| PostgreSQL 18.6 serve | upgrade | inplace | external | supported |
| PostgreSQL 18.6 serve | upgrade | inplace | auto | supported |
| PostgreSQL 18.6 serve | upgrade | clone | auto | supported |
| SQLite serve | storage | inplace | auto | supported |
| SQLite local | storage | inplace | auto | supported |
| PostgreSQL 18.6 serve | storage | inplace | external | supported |
| PostgreSQL 18.6 serve | storage | inplace | auto | supported |

本轮 version 2 的上述格均有 Windows 10 amd64 / Go 1.26.10 实跑证据；SQLite 原路径/WAL 恢复、PG 18.6 普通角色四场景和同 OID 实际恢复分别通过。2026-10-23 version 1 clone 历史结果保留于 acceptance.json。其他平台编译不等于原生执行，其他 PG 主版本未经现场验证。真实 S3 对象恢复和 COMPLIANCE 版本保留均为 `unverified`。local 模式使用 SQLite。v0.13/v0.14 历史路线为 `archived_unverified`，不承诺完整升级链。

唯一离线程序为 `linguaflow-migrate`，旧第二二进制、旧 Task 和旧独立 CLI 包已直接删除。正常 `linguaflow` 不依赖 `internal/migration`。在项目根目录执行以下 Task；`CLI_ARGS` 中所有文件路径必须显式选择并使用绝对路径，示例仅为占位值。

访问源数据库前必须显式选择配置文件、数据库 DSN 或数据目录；相关环境变量也属于显式输入。没有这些输入时，工具在打开数据库及读取无关凭据前拒绝，不会选中默认 `backend/data`。`list` 和帮助命令无需数据库输入。

```powershell
task backend:migrate:list
task backend:migrate:inspect CLI_ARGS='--mode serve --config C:/Migration/source.yaml'
task backend:migrate:build
task backend:migrate:plan CLI_ARGS='--mode serve --config C:/Migration/source.yaml --operation-dir C:/Migration/run-01 --account-map C:/Migration/accounts.json'
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

省略策略和目标参数默认 `inplace`；提供既有 target 参数而省略策略默认 `clone`；显式 inplace 与 target 参数冲突会拒绝。SQLite 和 clone 固定 `--database-backup auto`，PG inplace 默认为 external。PG 首次 apply 必须提供 `--backup-reference <运维恢复材料引用>`，resume 使用已经绑定的 reference、确认时间和原始数据库身份，全程不调用 dump/restore。PG auto 只需要与源服务器同主版本的 pg_dump；不以 pg_restore 安装或实际恢复作为捕获前提，失败不会降级为声明。

SQLite clone 的 `target-data-dir` 必须完全不存在，工具在同卷独立 staging 验证后无覆盖发布。inplace 沿用原数据库路径和文件根；operation 与数据库和引用根必须独立。链接、junction、短路径别名和路径身份变化被拒绝。PG clone 使用 `--target-config` 指定独立空目标库及新文件根；需要匹配主版本的 dump/restore，可用 `LINGUAFLOW_MIGRATION_POSTGRES_BIN` 指定工具目录。PG `--rehearsal-config` 可指向独立空库，或明确提供与原始基线匹配的人工恢复副本；缺省只影响演练，不阻断 inplace apply。工具不切换生产 DSN。

PostgreSQL 有限路线要求同一个明确 Host/Port，允许该端点的 TLS fallback，拒绝多主机或无法在外部 dump/restore 精确复现的连接选项；继承的 PGHOSTADDR/service 等不会进入子进程。空目标与源探测拒绝额外类型、函数、扩展及其他 catalog 对象。源指纹包含 serial/identity 序列归属、参数和实际分配状态；SQLite 同样绑定 sqlite_sequence。固定 CHECK 以保留逻辑分组的有限语法核对，不能通过追加 OR true 放宽原发布约束。

apply/rehearse/resume/rollback 都需要 `--offline`，表示正常服务、worker、GC 和其他写者已停。新 operation 的 plan/journal/report 为 version 2；数据库屏障仍为 version 1。停写声明、操作锁和数据库锁后重新核对源，先持久化 baseline 与保护材料，再建立 preparing。schema/accounts/history/storage/ready 每步变更与绑定 baseline 的回执同事务，ready 保存排除迁移表的业务完成摘要。已有所属屏障时按步骤前后条件恢复部分格式，DB 已提交但 journal 丢失可重建进度。正常服务启动在自动 schema/data 写入前拒绝 preparing、abandoned 和未知状态。

新 inplace 升级和 storage 导入只保护数据库及必要配置/keyring，保留原件引用并执行来源/摘要检查，不复制全部对象。报告分别使用 `captured`、`operator_declared`，演练和实际恢复结果另列。clone 继续要求独立对象保护；源已丢失或来源不可证明时保留明确领域事实，不能宣称原件已恢复。clone 的 S3 引用须用 `--external-backup-receipt` 提供版本 1 外部 JSON，字段如下：

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

Storage plan 接受当前完整账号/业务格式，固定 inplace，共用 database-backup/backup-reference。已有完整 SourceRevision 保留，仅为尚未导入的 legacy 引用制定子计划。原 ManifestVersion=1 只读，新的日志绑定原摘要和 operation ID。逐资源事务、确定 Blob 身份和代次保证恢复不重复转换；storage rollback 拒绝后续编辑、新任务快照、pin 或代次变化。

SQLite inplace rollback 先保全失败库及原 WAL，在原库同卷准备恢复文件；关闭连接后按身份无覆盖发布，验证原始基线/密钥/恢复身份才报 rolled_back。ready 后出现业务写入时拒绝自动覆盖。PG rollback 只输出 restore-instructions.json 并保持 restore_required；操作者实际恢复同一原库，重新验证原始基线及 OID/端点后才报 rolled_back。OID/端点变化需独立恢复验收。clone rollback 保留原库和独立目标，由操作者选择切换。

Version 1 plan/journal/report DTO、字段顺序和摘要冻结，新工具只读 status，其他操作明确指向计划绑定的原二进制。原 v0.16 工具的真实中断夹具及原始 JSON/摘要保留于 backend/bin/legacy-v016，不转换旧文件或补入新声明。

通用实例备份已从统一迁移 CLI、Task 和 catalog 路线中移除。迁移自身仍会在升级/存储导入前生成并核验与 operation 绑定的恢复材料；`internal/storagebackup` 继续维护已发布 manifest version 1、对象引用/密钥校验和 BackupPin 消费链。B1 的独立维护入口尚未交付，不能把迁移工具当作当前实例备份命令使用。

验收命令为 `backend:migrate:test`、`backend:migrate:test:binary`、`backend:migrate:check-dependencies` 和后端 format/lint/test/race/build。`backend:migrate:test:postgres` 使用固定 SHA-256 的 EDB PostgreSQL 18.6 Windows 包，启动私有目录、回环地址和临时端口的隔离实例；不注册系统服务，迁移角色 NOSUPERUSER/NOCREATEDB。SOURCE/TARGET/REHEARSAL × INPLACE/AUTO/CLONE 九个专用库供升级、storage 和演练实际验收；脚本要求四个场景全部 PASS 且无 SKIP，finally 停止实例并保留脱敏证据。普通 test 缺少此环境的条件跳过不提高 PG 支持状态。

`backend:migrate:release` 提供 linux/darwin/windows × amd64/arm64 六个平台的迁移工具及独立 `.sha256` 文件；产物位于 backend/bin/release。本分支交付产物和 main 发布接入说明，正式发布由 main 后续接入。平台编译与 Windows 实际执行分别记录。当前 Task 结果随 acceptance.json 编译入 `list`，不会把历史检查写成本轮新运行结果。

历史冻结树为 v0.14.0 commit `4bc4577cd669d9ccbbc4980f19e259b5beff2b18`，v0.13.0 peeled commit `99cb87dea19a7fcc61b6c267f916bfe046b2d355`。当前 v013 正文和 journal 读取保留于 `upgrade/v013` 且仍拒绝；只有精确可获取可运行的归档产物及中断恢复验收通过，才能删除正文。Git 历史链接不能替代这两项证明。
