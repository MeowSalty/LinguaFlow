import { writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createRuntime, backendDir } from './runtime.mjs';
import { run } from './prepare.mjs';

// Keep the isolated database alive for the entire B3 test process. This also
// permits focused race verification without starting browser/Keycloak fixtures.
const runtime = await createRuntime();
const command = 'task backend:test:race CLI_ARGS=./internal/service -run TestAuthenticationB3OAuth -count=1';
try {
  const postgres = await runtime.postgres();
  await run('task', ['backend:test:race', 'CLI_ARGS=./internal/service -run TestAuthenticationB3OAuth -count=1'], {
    cwd: resolve(backendDir, '..'), env: { ...process.env, LINGUAFLOW_TEST_POSTGRES_DSN: postgres.dsn }
  });
  await writeFile(join(runtime.root, 'postgres-b3-report.json'), JSON.stringify({ tested_at: new Date().toISOString(), database: 'PostgreSQL', status: 'passed', command }, null, 2));
  console.log('B3 PostgreSQL race acceptance report: ' + join(runtime.root, 'postgres-b3-report.json'));
} catch {
  await writeFile(join(runtime.root, 'postgres-b3-report.json'), JSON.stringify({ tested_at: new Date().toISOString(), database: 'PostgreSQL', status: 'failed', command }, null, 2));
  console.error('B3 PostgreSQL race acceptance failed. Isolated diagnostics: ' + runtime.root);
  process.exitCode = 1;
} finally { await runtime.close(); }
