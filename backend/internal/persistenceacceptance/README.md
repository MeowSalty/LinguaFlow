# Persistence acceptance fixture

从仓库根目录运行：

```powershell
task backend:test:persistence
task backend:test:persistence:postgres
task backend:bench:persistence
task backend:bench:persistence 'CLI_ARGS=--postgres'
```

日常协议检查可显式选择较短范围：

```powershell
task backend:test:persistence '--' --protocol
task backend:test:persistence:postgres '--' --protocol
```

`--protocol` 保留协议、故障、原子性、版本/预算栅栏、未知提交、并发达到10与SQL成本检查，仅排除7资源抽取和翻译的长负载矩阵。它要求实际必跑测试全部pass、零skip，并检查Reserve四档与pool advance、Seal、Checkpoint、Calibrate、commit观测样本。报告明确 `scope=protocol`，不能代替完整矩阵验收。默认入口仍要求完整矩阵；`--bench` 与 `--protocol` 不能组合，未知或重复参数失败。

普通 Go 测试的 `TestPersistenceSharedConcurrencyReachesTen` 使用7资源、14次真实请求验证共享额度10、第11请求排队、最终用量/进度及额度回收。完整4200片段和18种operation/phase观测由 `TestPersistenceSharedConcurrencyCapacityObservations` 承担，需要 `LINGUAFLOW_CAPACITY_TESTS=1`。上述持久化启动器会主动设置该开关；默认与 `--protocol` 均要求此容量测试实际pass并保留 `extraction7x2x300` 样本。`--bench` 继续运行自己的完整负载矩阵。`task backend:test:capacity` 提供历史删除与共享并发两例的定向容量检查，另有 `task backend:test:persistence:capacity` 承担既有Ruby/翻译容量报告，两者验收范围不同。

PowerShell中给Task分隔符 `'--'` 加引号以免被shell消费；也可用 `'CLI_ARGS=--protocol'`。Linux shell可直接写 `task backend:test:persistence -- --protocol`。

启动器设置显式测试 driver，使用临时 SQLite 文件或随机 loopback 端口的专用 PostgreSQL 实例，退出时回收实例。Windows 可复用 `backend/bin/.migration-postgres-18.6-cache/tools/pgsql/bin`；其他工具路径由 `LINGUAFLOW_AUTH_POSTGRES_BIN` 指定，无需业务 DSN。继承的 LinguaFlow/PG 业务环境被清理，模型由固定假后端实现。

`LINGUAFLOW_PERSISTENCE_OUTPUT` 指定报告目录，输出 `run.json`、`tests.jsonl`、`samples.jsonl`。缺少必跑测试、跳过或缺少测量样本都会使入口失败。长翻译矩阵只在显式 driver 入口运行；普通测试明确跳过该慢矩阵。SQL 延迟逐条按 context 注入，计数分别记录 Query/Exec、BEGIN/COMMIT 和 Rollback 调用；Rollback 调用包含成功提交后的清理调用。

入口始终验证当前工作区，SQL、事实与队列断言不可通过旧基线环境标记跳过。

完整验收记录和原始日志由对应验收任务在仓库外保存；未通过、部分运行或协议范围结果不能证明完整负载矩阵通过。
