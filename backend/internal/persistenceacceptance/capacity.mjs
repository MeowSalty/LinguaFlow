import { mkdir, mkdtemp, open, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { run } from '../authacceptance/prepare.mjs';
import { initializeSamples, validateCapacityMeasurements } from './measurements.mjs';

// This runner explicitly owns SQLite fixtures. The full PostgreSQL protocol
// and load matrix remain in test:persistence:postgres.
if (process.argv.length > 2) throw new Error('Capacity acceptance takes no flags');
const backendDir = resolve(fileURLToPath(new URL('../..', import.meta.url)));
const sourceRoot = resolve(backendDir, '..');
const reportRoot = process.env.LINGUAFLOW_PERSISTENCE_OUTPUT || await mkdtemp(join(tmpdir(), 'linguaflow-capacity-'));
await mkdir(reportRoot, { recursive: true });
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('LINGUAFLOW_') && !key.startsWith('PG')));
env.LINGUAFLOW_PERSISTENCE_DRIVER = 'sqlite3';
env.LINGUAFLOW_PERSISTENCE_REPORT = join(reportRoot, 'samples.jsonl');
await initializeSamples(env.LINGUAFLOW_PERSISTENCE_REPORT);
const parents = ['TestPersistenceSealAndCheckpointCosts', 'TestPersistenceTranslationContract', 'TestRubyHandoffAcceptanceHTTPRecoveryGrowthThirtyTwo', 'TestRubyHandoffAcceptanceHTTPHandoffCapacity'];
const command = ['backend:test', 'CLI_ARGS=-count=1 -timeout=15m -v -json -run \'^(' + parents.join('|') + ')$\' ./internal/worker'];
let status = 'failed';
let results = [];
let facts = [];
try {
  const output = await open(join(reportRoot, 'tests.jsonl'), 'w', 0o600);
  try { await run('task', command, { cwd: sourceRoot, env, stdio: ['ignore', output.fd, output.fd] }); }
  finally { await output.close(); }
  results = (await readFile(join(reportRoot, 'tests.jsonl'), 'utf8')).split(/\r?\n/).flatMap(line => { try { return [JSON.parse(line)]; } catch { return []; } });
  if (results.some(event => event.Action === 'skip' && event.Test)) throw new Error('Required capacity test skipped');
  for (const name of parents) if (!results.some(event => event.Action === 'pass' && event.Test === name)) throw new Error('Required capacity test did not pass: ' + name);
  facts = results.filter(event => event.Action === 'output').flatMap(event => {
    const prefix = 'capacity_fact=';
    const index = event.Output?.indexOf(prefix) ?? -1;
    return index < 0 ? [] : [JSON.parse(event.Output.slice(index + prefix.length).trim())];
  });
  const measurements = (await readFile(env.LINGUAFLOW_PERSISTENCE_REPORT, 'utf8')).trim().split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
  validateCapacityMeasurements(measurements, facts);
  status = 'passed';
} finally {
  await writeFile(join(reportRoot, 'facts.json'), JSON.stringify(facts, null, 2) + '\n');
  await writeFile(join(reportRoot, 'run.json'), JSON.stringify({ tested_at: new Date().toISOString(), status, scope: 'capacity', driver: 'sqlite3', source: sourceRoot, command: ['task', ...command], samples: env.LINGUAFLOW_PERSISTENCE_REPORT, test_results: results.filter(event => event.Test && ['pass', 'fail', 'skip'].includes(event.Action)).map(event => ({ test: event.Test, action: event.Action })) }, null, 2) + '\n');
  process.stdout.write('Capacity acceptance ' + status + ': ' + reportRoot + '\n');
}
