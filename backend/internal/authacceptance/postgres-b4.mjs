import { mkdir, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createRuntime, backendDir } from './runtime.mjs';
import { run } from './prepare.mjs';

const runtime = await createRuntime();
const command = 'task backend:test:race CLI_ARGS=./internal/service -run TestAuthenticationB4 -count=1';
const reportPath = join(backendDir, 'docs', 'account-authentication-b4-postgres-report-2026-10-15.json');
let status = 'failed';
try {
  const postgres = await runtime.postgres();
  await run('task', ['backend:test:race', 'CLI_ARGS=./internal/service -run TestAuthenticationB4 -count=1'], {
    cwd: resolve(backendDir, '..'),
    env: { ...process.env, LINGUAFLOW_TEST_POSTGRES_DSN: postgres.dsn }
  });
  status = 'passed';
} catch (error) {
  console.error('B4 PostgreSQL acceptance failed. Isolated diagnostics: ' + runtime.root);
  if (process.env.LINGUAFLOW_TEST_VERBOSE) console.error(error);
  process.exitCode = 1;
} finally {
  await runtime.close();
  await mkdir(join(backendDir, 'docs'), { recursive: true });
  await writeFile(reportPath, JSON.stringify({ tested_at: new Date().toISOString(), database: 'PostgreSQL', status, command }, null, 2) + '\n');
  console.log('B4 PostgreSQL acceptance report: ' + reportPath);
}
