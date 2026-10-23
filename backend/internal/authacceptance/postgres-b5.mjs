import { mkdir, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createRuntime, backendDir } from './runtime.mjs';
import { run } from './prepare.mjs';

const runtime = await createRuntime();
const args = './internal/service ./internal/database -run "AuthenticationB5|AuthenticationRecovery|AuthenticationB4" -count=1';
const command = "task backend:test:race CLI_ARGS='" + args + "'";
const testedAt = new Date().toISOString();
const reportPath = join(backendDir, 'docs', 'account-authentication-b5-postgres-report-' + testedAt.slice(0, 10) + '.json');
let status = 'unverified';
let reason = 'PostgreSQL runtime is not configured';
try {
  if (!process.env.LINGUAFLOW_AUTH_POSTGRES_BIN) {
    console.error('B5 PostgreSQL 未验收：请设置 LINGUAFLOW_AUTH_POSTGRES_BIN 为专用 PostgreSQL bin 目录。');
    process.exitCode = 1;
  } else {
    status = 'failed';
    reason = 'Isolated PostgreSQL startup or acceptance tests failed';
    const postgres = await runtime.postgres();
    await run('task', ['backend:test:race', 'CLI_ARGS=' + args], {
      cwd: resolve(backendDir, '..'),
      env: { ...process.env, LINGUAFLOW_TEST_POSTGRES_DSN: postgres.dsn }
    });
    status = 'passed';
    reason = '';
  }
} catch {
  console.error('B5 PostgreSQL 验收失败；隔离诊断目录：' + runtime.root);
  process.exitCode = 1;
} finally {
  // run() waits for the complete Go test process before closing the owned database.
  await runtime.close();
  await mkdir(join(backendDir, 'docs'), { recursive: true });
  await writeFile(reportPath, JSON.stringify({
    tested_at: testedAt,
    completed_at: new Date().toISOString(),
    database: 'PostgreSQL',
    stage: 'B5',
    status,
    reason,
    command,
    scope: ['configuration_save', 'registration', 'cancel', 'unlink', 'password_delete', 'authentication_recovery'],
    frontend_t16: 'pending'
  }, null, 2) + '\n');
  console.log('B5 PostgreSQL 验收脱敏报告：' + reportPath);
}
