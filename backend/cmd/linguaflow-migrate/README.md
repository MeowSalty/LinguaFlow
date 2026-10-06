# v0.13.0 一次性迁移

`linguaflow-migrate` 单独构建，支持 v0.13.0 PostgreSQL/serve、SQLite/serve 和 SQLite/local。
正常 `linguaflow` 程序不包含历史转换入口；迁移工具也不启动 HTTP 服务。
迁移保留用户 ID、密码、角色和数据归属，将旧明文 provider key 转成加密凭据，
并转换历史任务快照、实例初始化状态及旧配置。历史任务保留自己的原密钥。

运行前停止旧服务和所有连接该数据目录的进程。PostgreSQL 必须先有可恢复的备份或托管快照；
SQLite 由工具在发布时完整保留原目录作为备份。
已经迁移、混合版本或无法可靠解释的数据会被拒绝，不会重置账户或猜测历史值。

以下命令从仓库根目录运行。`CLI_ARGS=...` 写法兼容 PowerShell 下的 Task 包装器。
不要把密码、DSN 或密钥内容写进命令参数。

## PostgreSQL / serve

通过环境变量提供连接，或使用只包含连接字符串的私有文件：

```powershell
$Env:LINGUAFLOW_DATABASE_DRIVER = 'postgres'
$Env:LINGUAFLOW_DATABASE_DSN_FILE = 'C:\private\linguaflow-dsn'
task -t backend/Taskfile.yml migrate:v013 'CLI_ARGS=--data-dir "C:\LinguaFlow\data"'
task -t backend/Taskfile.yml migrate:v013:apply 'CLI_ARGS=--data-dir "C:\LinguaFlow\data"'
```

也支持 `LINGUAFLOW_DATABASE_DSN`，但不可与 `_FILE` 同时设置。
文件读取仅移除一个末尾 LF 或 CRLF，不裁剪其他空白。
不读取 `.env` 或服务 YAML；运行前清除 `LINGUAFLOW_SERVER_CONFIG`。

默认预演使用数据库迁移锁，并在 SERIALIZABLE 事务内执行 schema 和数据转换后回滚。
**预演会持锁，且可能消耗 PostgreSQL 序列值**；不会创建密钥文件。
只有 `--apply` 或 `migrate:v013:apply` 才提交。

密钥选择与输出：

- `--data-dir` 优先，其次 `LINGUAFLOW_DATA_DIR`，默认当前目录下 `data`。
- 普通部署可提供 `LINGUAFLOW_CREDENTIALS_MASTER_KEY` 或 `LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE`，
  内容为标准 Base64 编码的随机 32 字节密钥。两者互斥；显式空值、非规范 Base64 或长度错误都会失败。
  `_FILE` 只移除一个末尾 LF/CRLF。迁移直接使用该密钥，不创建 keyring 文件；
  **迁移后启动服务、失败后重试及恢复数据库时必须提供同一密钥**。
- 主密钥与显式 `--keyring-file` 或 `LINGUAFLOW_CREDENTIALS_KEYRING_FILE` 互斥，
  即使其中的 keyring 路径为空也会报错。数据目录里未显式指定的旧 keyring 不会替代主密钥。
- 未提供主密钥时，keyring 优先使用 `--keyring-file`，其次 `LINGUAFLOW_CREDENTIALS_KEYRING_FILE`，
  两者均未指定时使用 `<data-dir>/credentials-keyring.json`。显式空路径报错；已有文件必须有效，绝不自动覆盖。
- 可以提供至少 32 字节的 `LINGUAFLOW_JWT_SECRET` 或 `_FILE`；两者冲突、显式空值或短值都会失败。
  未配置时复用或创建 `<data-dir>/jwt-secret`。
- 正式迁移在提交前发布本次缺少且需要生成的 keyring 和 JWT 文件。若提交失败，
  **保留这些文件，用相同路径重跑**；不要重新生成，否则已提交或待确认数据可能无法解密。
- 成功后输出启动所需的文件路径和变量名，不输出 DSN、主密钥、JWT 或 provider key。
  使用主密钥时只提示继续提供同一密钥，不提示不存在的 keyring 路径。

使用主密钥文件的示例（先在文件中准备安全随机密钥，勿每次迁移重新生成）：

```powershell
$Env:LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE = 'C:\private\credentials-master-key'
task -t backend/Taskfile.yml migrate:v013 'CLI_ARGS=--data-dir "C:\LinguaFlow\data"'
task -t backend/Taskfile.yml migrate:v013:apply 'CLI_ARGS=--data-dir "C:\LinguaFlow\data"'
```

随后以输出的环境变量运行 `task backend:dev`。把所用主密钥或完整 keyring 与数据库备份一起妥善保存。
迁移后的实例已有初始化标记和原管理员，不需再次初始化账户。

## SQLite / serve

`serve` 是服务器运行模式，既可以使用 PostgreSQL，也可以使用 SQLite。
在自己的电脑上运行 `serve` 仍然是服务器模式，不应使用要求 `local` 用户的本地模式迁移。
SQLite 入口必须显式选择模式，不根据数据库类型、文件位置或账号名称猜测。

以下示例迁移 `backend/data/linguaflow.db`，保留所有原用户的 ID、用户名、密码、角色和数据归属。
只要求原数据库有启用的管理员，不要求管理员名为 `local`，也不会创建替代管理员。

```powershell
task backend:migrate:v013:sqlite 'CLI_ARGS=--mode serve --data-dir data'
task backend:migrate:v013:sqlite:apply 'CLI_ARGS=--mode serve --data-dir data'
```

服务器密钥与 PostgreSQL/serve 使用相同的显式来源和互斥规则：

- `LINGUAFLOW_CREDENTIALS_MASTER_KEY` / `_FILE` 优先；指定主密钥时不创建额外 keyring。
- 否则使用 `--keyring-file`，其次 `LINGUAFLOW_CREDENTIALS_KEYRING_FILE`，默认 `<data-dir>/credentials-keyring.json`。
- JWT 使用 `LINGUAFLOW_JWT_SECRET` / `_FILE`；未指定时复用或创建 `<data-dir>/jwt-secret`。
- 不自动加载 `.env` 或服务配置文件。若开发服务通过 `backend/.env` 提供主密钥和 JWT，
  迁移进程也应显式提供同一配置；否则须在迁移后按输出改用生成的文件，不能继续用另一把主密钥启动。
- 此入口只处理数据目录内的 `linguaflow.db`。应取消 `LINGUAFLOW_SERVER_CONFIG`、
  `LINGUAFLOW_DATABASE_DSN` / `_FILE`；数据库 driver 若显式提供，只能为 `sqlite`。
- Windows 数据目录和 keyring 应使用标准磁盘路径；拒绝扩展前缀、8.3 短路径及映射盘别名，
  防止同一目录被误判为外部密钥位置。此限制仅针对迁移路径，不改变通用私有文件创建能力。

例如使用已有秘密文件时，先设置文件路径，再执行上面的预演与正式迁移命令：

```powershell
$Env:LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE = 'C:\private\credentials-master-key'
$Env:LINGUAFLOW_JWT_SECRET_FILE = 'C:\private\jwt-secret'
```

预演只在私有副本内生成缺少的目录内密钥，结束后删除副本；不会改写原数据库或发布外部 keyring。
正式迁移完成转换及解密验证后，才发布缺少的外部 keyring，并切换数据目录。
如果外部密钥已发布而后续切换失败，保留同一密钥文件用于重试，不覆盖、不重新生成。
自定义 keyring 放在数据目录内时，随副本一起发布；不在切换前写入原目录。

目录备份、SQLite/WAL 快照和恢复规则与下方 SQLite/local 共用。
切换记录绑定运行模式和密钥配置；用错误模式、不同主密钥/JWT 或已更换的 keyring 恢复会被拒绝。
两次重命名之间中断时，目录内的 `_FILE` 输入会从经过身份、收据和密钥校验的副本读取，
配置中的原始路径不变。恢复原目录后需再次执行迁移。

成功后按输出的 `LINGUAFLOW_DATABASE_DRIVER=sqlite`、数据目录及密钥来源启动 `serve`。
无需再次初始化管理员；已有初始化状态会保留迁移前的账户权限。

## SQLite / local

必须显式指定原 local 数据目录，其中数据库名为 `linguaflow.db`。
目录内密钥固定为 `credentials-keyring.json` 和 `instance-secret`。
先清除会覆盖密钥来源的 `LINGUAFLOW_CREDENTIALS_KEYRING_FILE`、
`LINGUAFLOW_CREDENTIALS_MASTER_KEY`、`LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE`、
`LINGUAFLOW_JWT_SECRET`、`LINGUAFLOW_JWT_SECRET_FILE` 及 `LINGUAFLOW_SERVER_CONFIG`。
即使变量值为空也必须取消设置；local 迁移始终使用目录内密钥，保持备份与恢复目录自包含。
数据库环境变量不参与 local 迁移。

```powershell
task -t backend/Taskfile.yml migrate:v013:local 'CLI_ARGS=--data-dir "C:\Users\me\AppData\Roaming\LinguaFlow"'
task -t backend/Taskfile.yml migrate:v013:local:apply 'CLI_ARGS=--data-dir "C:\Users\me\AppData\Roaming\LinguaFlow"'
```

也可使用 `migrate:v013:sqlite 'CLI_ARGS=--mode local --data-dir PATH'`。
原 `v013 local` 命令仍保留相同的本地模式限制。

迁移在同父目录建立私有副本，通过 SQLite Backup API 包含已提交的 WAL，
并复制普通资源文件。原数据库及 WAL、SHM、journal 不作为普通文件复制。
时间转换保留纳秒、时区含义和 NULL，只接受明确支持的带时区文本；
无时区、数字编码或无法识别的时间会失败。仅在完整转换成功后设置新时间格式标记。
local 身份必须是原有、启用且为管理员的 `local` 用户；工具不创建替代用户或提权。

副本通过数据库完整性、外键、凭据解密、本地身份和引用文件校验后：

- 预演删除临时副本，保留原目录。
- 正式迁移关闭文件句柄，持久化收据和目录外切换记录；将原目录改名为唯一备份目录，
  再将副本放回原路径。成功后输出备份路径，保留旧目录供恢复。

切换只支持同卷本地普通目录，拒绝符号链接、junction、特殊文件及越界资源路径。
Windows 副本目录和密钥显式设置私有 ACL；文件被占用时迁移会失败并保留可恢复数据。
两次目录重命名不构成断电级原子操作。

中断后以相同参数重跑：工具读取切换记录和目录身份，识别尚未发布、已恢复或已发布状态。
仅在原路径空缺且备份身份吻合时恢复原目录；恢复完成后需要再次运行迁移。
已发布的新目录不会自动回滚。状态矛盾时按错误给出的路径和恢复步骤处理，
不要删除记录或逐文件覆盖原目录。

迁移完成后仍使用原路径：

```powershell
$Env:LINGUAFLOW_DATA_DIR = 'C:\Users\me\AppData\Roaming\LinguaFlow'
task backend:local:dev
```

## 独立构建及验证

```powershell
task -t backend/Taskfile.yml migrate:build
task -t backend/Taskfile.yml check-dependencies
task -t backend/Taskfile.yml test
```

产物为 `backend/bin/linguaflow-migrate.exe`（Windows）或 `backend/bin/linguaflow-migrate`。
`migrate:build` 支持 `GOOS`、`GOARCH` 与可选 `MIGRATE_BINARY` 输出路径，交叉构建不代表目标系统上的原生测试。
直接使用二进制时命令为：

```text
linguaflow-migrate v013 postgres [--apply] [--data-dir PATH] [--keyring-file PATH]
linguaflow-migrate v013 sqlite --mode serve|local --data-dir PATH [--apply] [--keyring-file PATH]
linguaflow-migrate v013 local --data-dir PATH [--apply]
```

PostgreSQL 集成测试需设置 `LINGUAFLOW_TEST_POSTGRES_DSN` 指向专用测试实例，
测试会创建并清理独立 schema；未设置时跳过 PostgreSQL 集成部分。

历史转换、固定默认值和提示词集中于 `internal/migration/v013`，来源为 v0.13.0 标签。
目标执行 schema/defaults 固定为 version 1，兼容声明变化时必须更新迁移样本和测试。
工具复用共享加密存储与私有文件能力，不调用当前执行默认值解析器。
发布工作流尚未包含独立工具，当前通过以上 Task 本地构建。
