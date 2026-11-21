import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { epubLeaves, epubTop, modulePath, requiredTests, workerPackage } from './ci-test-contract.mjs';
import { mergeAtomicProfiles, parseInventory, parseJSONObjects, planShards, resolveGoMaxProcs, resolvePackageParallelism, resolveTaskBinary, runCI as nativeRunCI, runProcess, validateRound, validateShard } from './ci-test.mjs';
import { terminateOwnedProcess } from './process-cleanup.mjs';

const pipelinePackage = `${modulePath}/internal/pipeline`;
const emptyPackage = `${modulePath}/internal/empty`;
// Contract fixtures never launch Go: deterministic resources keep the existing
// smoke matrix independent of the desktop's shared memory and affinity.
function runCI(options) {
  const monitor = { sample: async groups => ({ atMs: Date.now(), availableBytes: 100 * 1024 ** 3, totalBytes: 128 * 1024 ** 3, cpuQuota: 4, allowedIds: [0, 1, 2, 3], cpuLoadKnown: true, sharedBusyCores: 0, busyCores: 0, owned: groups.map(group => ({ id: group.id, rssBytes: 64 * 1024 ** 2, cpuCores: .1, complete: true })), shared: [] }) };
  const resourceOptions = { monitor, ...options.resourceOptions };
  return nativeRunCI({ ...options, resourceOptions });
}
const id = (pkg, name) => `${pkg}::${name}`;
const jsonLines = events => events.map(event => JSON.stringify(event)).join('\n') + '\n';
const event = (pkg, action, name, extra = {}) => ({ Package: pkg, Action: action, ...(name ? { Test: name } : {}), ...extra });

test('gomaxprocs override follows Go integer parsing and defaults to the runtime', () => {
  for (const value of [undefined, null, '']) assert.equal(resolveGoMaxProcs(value), null);
  for (const [value, expected] of [['1', 1], ['4', 4], ['16', 16], ['+8', 8], ['04', 4], ['2147483647', 2147483647]]) assert.equal(resolveGoMaxProcs(value), expected);
  for (const value of ['0', '-1', '4.0', ' 4', 'invalid', '2147483648']) assert.throws(() => resolveGoMaxProcs(value), /GOMAXPROCS/);
});
function eventsFor(pkg, names, { skip, fail, elapsed = 2 } = {}) {
  const events = [event(pkg, 'start')];
  for (const name of names) {
    const top = name.split('/')[0];
    events.push(event(pkg, 'run', top));
    if (name !== top) events.push(event(pkg, 'run', name));
    if (skip) events.push(event(pkg, 'output', name, { Output: skip }));
    events.push(event(pkg, fail ? 'fail' : skip ? 'skip' : 'pass', name, { Elapsed: elapsed }));
    if (name !== top) events.push(event(pkg, fail ? 'fail' : 'pass', top, { Elapsed: elapsed }));
  }
  events.push(event(pkg, fail ? 'fail' : 'pass'));
  return events;
}
function temporary(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'linguaflow-ci-helpers-'));
  t.after(() => {
    const target = path.resolve(directory);
    assert.equal(path.dirname(target), path.resolve(os.tmpdir()));
    assert.ok(path.basename(target).startsWith('linguaflow-ci-helpers-'));
    fs.rmSync(target, { recursive: true, force: true });
  });
  return directory;
}
function inventory() {
  return new Map([
    [pipelinePackage, { name: pipelinePackage, tops: ['TestPipeline'], noTestFiles: false }],
    [emptyPackage, { name: emptyPackage, tops: [], noTestFiles: true }],
    [workerPackage, { name: workerPackage, tops: [epubTop, 'TestWorker'], noTestFiles: false }],
  ]);
}
function fakeTask({ failShard, failureExitCode = 1, wrongProfile, elapsed = 2, shardProcess, additionalPackages = [], extraStdout = '' } = {}) {
  const calls = [];
  const discovered = inventory();
  for (const pkg of additionalPackages) discovered.set(pkg.name, pkg);
  const execute = async (_binary, args, options) => {
    calls.push(args);
    assert.match(options.env.GOFLAGS, /(?:^|\s)-p=1$/);
    const name = args[0];
    const cli = args[1] ?? '';
    let stdout;
    let code = 0;
    if (name === '--version') stdout = '3.51.1\n';
    else if (name === 'backend:test:ci:environment') stdout = JSON.stringify({ GOVERSION: 'go1.26.10', GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '1', GOFLAGS: options.env.GOFLAGS, GOTOOLCHAIN: 'local' });
    else if (name === 'backend:test:ci:inventory') stdout = [...discovered.values()].map(pkg => JSON.stringify({ ImportPath: pkg.name, TestGoFiles: pkg.tops.length ? ['fixture_test.go'] : undefined })).join('\n');
    else if (cli.includes('-list')) stdout = jsonLines([...discovered.values()].flatMap(pkg => [event(pkg.name, 'start'), ...pkg.tops.map(top => event(pkg.name, 'output', null, { Output: `${top}\n` })), event(pkg.name, pkg.tops.length ? 'pass' : 'skip')]));
    else {
      const shard = cli.includes('-skip') ? 'worker-remainder' : cli.includes('dense_256') ? 'dense' : cli.includes('synthetic_book') ? 'synthetic' : 'packages';
      const names = shard === 'worker-remainder' ? ['TestWorker'] : shard === 'dense' ? [epubLeaves[0]] : [epubLeaves[1]];
      const pkg = shard === 'packages' ? pipelinePackage : workerPackage;
      stdout = jsonLines(shard === 'packages' ? [...eventsFor(pkg, ['TestPipeline'], { fail: failShard === shard }), event(emptyPackage, 'start'), event(emptyPackage, 'skip')] : eventsFor(pkg, names, { fail: failShard === shard, elapsed: typeof elapsed === 'object' ? elapsed[shard] ?? 2 : elapsed }));
      const selectedPackages = shard === 'packages' && additionalPackages.length ? [...discovered.values()].filter(pkg => cli.split(/\s+/).includes(pkg.name)) : null;
      if (selectedPackages) stdout = jsonLines(selectedPackages.flatMap(pkg => pkg.tops.length ? eventsFor(pkg.name, pkg.tops, { fail: failShard === shard }) : [event(pkg.name, 'start'), event(pkg.name, 'skip')]));
      if (shard === 'packages') stdout += extraStdout;
      code = failShard === shard ? failureExitCode : 0;
      const profile = cli.match(/-coverprofile="([^"]+)"/);
      if (profile) {
        const file = shard === 'packages' ? 'pipeline.go' : 'worker.go';
        const value = profile[1].includes('round-2') ? 99 : shard === 'dense' ? 1 : shard === 'synthetic' ? 2 : 3;
        fs.writeFileSync(profile[1], `mode: ${wrongProfile ? 'set' : 'atomic'}\n${(selectedPackages ?? [{ name: pkg }]).map(pkg => `${pkg.name}/${file}:1.1,2.1 1 ${value}\n`).join('')}`);
      }
      const nativeResult = await shardProcess?.({ shard, stdout, code, options });
      if (nativeResult) return nativeResult;
    }
    if (options.outputFile) {
      fs.writeFileSync(options.outputFile, stdout);
      if (path.basename(options.outputFile) === 'test-results.out') {
        fs.writeFileSync(path.join(path.dirname(options.outputFile), 'test-events.jsonl'), stdout);
        fs.writeFileSync(path.join(path.dirname(options.outputFile), 'task-stderr.out'), '');
      }
    }
    return { code, stdout, stderr: '' };
  };
  return { calls, execute };
}

test('gomaxprocs runner leaves defaults unset and propagates explicit 16 and 4 without clamping', async t => {
  for (const value of [undefined, '', '16', '4']) {
    const root = temporary(t);
    fs.mkdirSync(path.join(root, 'backend'));
    const fake = fakeTask();
    const expected = value ? Number(value) : null;
    let goTaskCalls = 0;
    const execute = (binary, args, options) => {
      if (args[0].startsWith('backend:')) {
        goTaskCalls++;
        if (expected == null) assert.equal(Object.hasOwn(options.env, 'GOMAXPROCS'), false, 'Go runtime default must not be replaced by an environment setting');
        else assert.equal(options.env.GOMAXPROCS, String(expected));
      }
      return fake.execute(binary, args, options);
    };
    const summary = await runCI({ root, mode: 'race', outputDir: 'gomaxprocs', execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: value == null ? {} : { GOMAXPROCS: value } });
    assert.equal(summary.status, 'passed', JSON.stringify(summary.issues));
    assert.equal(goTaskCalls, 7);
    assert.equal(summary.environment.gomaxprocs, expected);
    assert.equal(summary.environment.gomaxprocsSource, expected == null ? 'runtime-default' : 'environment-override');
    assert.ok(summary.shards.every(shard => shard.gomaxprocs === expected));
    assert.equal(summary.resources.policy.minFreeBytes, 1024 ** 3);
  }
});

test('gomaxprocs invalid overrides fail before Task discovery', async t => {
  for (const value of ['0', '-1', '4.5', 'invalid', '2147483648']) {
    const root = temporary(t);
    fs.mkdirSync(path.join(root, 'backend'));
    const fake = fakeTask();
    const summary = await runCI({ root, mode: 'race', outputDir: 'invalid-gomaxprocs', execute: fake.execute, taskBinary: process.execPath, env: { GOMAXPROCS: value } });
    assert.equal(summary.status, 'failed');
    assert.ok(summary.issues.some(issue => issue.includes('GOMAXPROCS')));
    assert.equal(fake.calls.length, 0);
  }
});

test('native Task discovery never runs a pnpm wrapper or installs a binary', t => {
  const directory = temporary(t);
  const cli = path.join(directory, 'global/5/.pnpm/@go-task+cli@3.51.1/node_modules/@go-task/cli');
  fs.mkdirSync(path.join(cli, 'bin'), { recursive: true });
  const native = path.join(cli, 'bin/task.exe');
  fs.writeFileSync(native, 'MZfixture');
  fs.writeFileSync(path.join(directory, 'task.ps1'), '$basedir/global/5/.pnpm/@go-task+cli@3.51.1/node_modules/@go-task/cli/run-task.js');
  assert.equal(resolveTaskBinary({ PATH: directory }, 'win32'), fs.realpathSync(native));
  assert.equal(resolveTaskBinary({ TASK_BINARY: native, PATH: '' }, 'win32'), fs.realpathSync(native));
  assert.throws(() => resolveTaskBinary({ TASK_BINARY: path.join(directory, 'task.ps1') }, 'win32'), /native Task/);
  fs.unlinkSync(native);
  assert.throws(() => resolveTaskBinary({ PATH: directory }, 'win32'), /no automatic download/);
  const linux = path.join(directory, 'task');
  fs.writeFileSync(linux, '#!/bin/sh\nnode installer.mjs\n', { mode: 0o755 });
  assert.throws(() => resolveTaskBinary({ PATH: directory }, 'linux'), /no automatic download/);
  fs.writeFileSync(linux, Buffer.from([0x7f, 0x45, 0x4c, 0x46]));
  assert.equal(resolveTaskBinary({ PATH: directory }, 'linux'), fs.realpathSync(linux));
});

test('inventory rejects malformed, missing and repeated package results', () => {
  assert.deepEqual(parseJSONObjects('{"a":"{x}"}\n{"b":2}'), [{ a: '{x}' }, { b: 2 }]);
  assert.throws(() => parseJSONObjects('{"a":1'), /Truncated/);
  assert.throws(() => parseJSONObjects('noise {"a":1}'), /non-JSON/);
  const records = [{ ImportPath: pipelinePackage, TestGoFiles: ['test.go'] }];
  const listed = jsonLines([event(pipelinePackage, 'output', null, { Output: 'TestPipeline\n' }), event(pipelinePackage, 'pass')]);
  assert.deepEqual(parseInventory(listed, records).get(pipelinePackage).tops, ['TestPipeline']);
  assert.throws(() => parseInventory('', records), /Missing inventory package/);
  assert.throws(() => parseInventory(listed + jsonLines([event(pipelinePackage, 'pass')]), records), /Repeated/);
});

test('native child stdout JSON remains replayable when stderr interrupts a JSON record', async t => {
  const directory = temporary(t);
  const script = 'process.stdout.write("{\\"Action\\":"); setTimeout(() => { process.stderr.write("task diagnostic\\n"); process.stdout.write("\\"pass\\",\\"Package\\":\\"fixture\\"}\\n"); }, 10);';
  const result = await runProcess(process.execPath, ['-e', script], { cwd: directory, env: process.env, outputFile: path.join(directory, 'test-results.out') });
  assert.equal(result.code, 0);
  const rawJSON = fs.readFileSync(path.join(directory, 'test-events.jsonl'), 'utf8');
  assert.deepEqual(JSON.parse(rawJSON), { Action: 'pass', Package: 'fixture' });
  assert.equal(fs.readFileSync(path.join(directory, 'task-stderr.out'), 'utf8'), 'task diagnostic\n');
  assert.ok(fs.readFileSync(path.join(directory, 'test-results.out'), 'utf8').includes('task diagnostic'));
});

test('Task stdout routes only complete current-task exit annotations and preserves every original stream', async t => {
  const directory = temporary(t);
  const annotation = "::error title=Task 'backend:test:race' failed::exit status 1";
  const unknown = [
    'unknown stdout',
    annotation.replace('backend:test:race', 'backend:test'),
    annotation.replace('failed::', 'failed,file=test.go::'),
    annotation.replace('exit status 1', 'signal: killed'),
    annotation.replace('exit status 1', 'exit status 0'),
    annotation.replace('exit status 1', 'exit status -1'),
    ` ${annotation}`,
    `${annotation} extra`,
    annotation.slice(0, -1),
  ].join('\n') + '\n';
  const goJSON = jsonLines([
    event(pipelinePackage, 'run', 'TestPipeline'),
    event(pipelinePackage, 'output', 'TestPipeline', { Output: `${annotation}\n` }),
    event(pipelinePackage, 'fail', 'TestPipeline'),
    event(pipelinePackage, 'fail'),
  ]);
  // Split both the JSON and annotation across native writes, interleave stderr,
  // and finish with a complete annotation without a newline to exercise EOF.
  const chunks = [goJSON.slice(0, 12), goJSON.slice(12) + annotation.slice(0, 23), annotation.slice(23) + '\r\n' + unknown + annotation];
  const script = `process.stdout.write(${JSON.stringify(chunks[0])}); process.stderr.write('task stderr\\n'); setTimeout(() => { process.stdout.write(${JSON.stringify(chunks[1])}); setTimeout(() => { process.stdout.write(${JSON.stringify(chunks[2])}); process.exitCode = 137; }, 10); }, 10);`;
  const result = await runProcess(process.execPath, ['-e', script], { cwd: directory, env: process.env, outputFile: path.join(directory, 'test-results.out'), taskName: 'backend:test:race' });
  assert.equal(result.code, 137);
  assert.equal(result.stdout, goJSON + unknown);
  assert.equal(result.rawStdout, chunks.join(''));
  assert.equal(result.taskDiagnostics, annotation + '\r\n' + annotation);
  assert.equal(result.stderr, 'task stderr\n');
  assert.equal(fs.readFileSync(path.join(directory, 'test-events.jsonl'), 'utf8'), result.stdout);
  assert.equal(fs.readFileSync(path.join(directory, 'task-stdout.out'), 'utf8'), result.rawStdout);
  assert.equal(fs.readFileSync(path.join(directory, 'task-diagnostics.out'), 'utf8'), result.taskDiagnostics);
  assert.equal(fs.readFileSync(path.join(directory, 'task-stderr.out'), 'utf8'), result.stderr);
  const mixed = fs.readFileSync(path.join(directory, 'test-results.out'), 'utf8');
  assert.equal(mixed.replace('task stderr\n', ''), result.rawStdout);
  const validation = validateShard(result, new Map([[pipelinePackage, inventory().get(pipelinePackage)]]), new Set([id(pipelinePackage, 'TestPipeline')]));
  assert.equal(validation.issues.filter(issue => issue === 'Non-JSON output on go test stdout').length, 9);
  assert.ok(validation.issues.includes('Task exited 137'));
  assert.ok(validation.issues.includes(`Unfinished/repeated/failed test: ${id(pipelinePackage, 'TestPipeline')}`));
});

test('owned parent/grandchild cancellation and direct-parent exit both await confirmed tree cleanup', async t => {
  const directory = temporary(t);
  const alive = pid => { try { process.kill(pid, 0); return true; } catch (error) { if (error.code === 'ESRCH') return false; throw error; } };
  for (const scenario of ['cancel', 'parent-exit']) {
    const ready = path.join(directory, `${scenario}-ready.json`);
    const grandchildReady = path.join(directory, `${scenario}-grandchild-ready`);
    const grandchildScript = `const fs=require('node:fs'); process.on('SIGTERM',()=>{}); fs.writeFileSync(process.argv[1],'ready'); setInterval(()=>{},1000);`;
    // On Windows, an ordinary Node child can exit with its parent's libuv job.
    // Detach only there to require a real outliving descendant; retain inherited
    // stdout/stderr so waiting for close instead of exit would still hang.
    const parentScript = `const fs=require('node:fs'); const {spawn}=require('node:child_process'); const kid=spawn(process.execPath,['-e',${JSON.stringify(grandchildScript)},process.argv[2]],{detached:${process.platform === 'win32'},windowsHide:true,stdio:['ignore','inherit','inherit']}); process.on('SIGTERM',()=>{process.stdout.end();process.stderr.end();process.exit(0)}); const wait=setInterval(()=>{if(fs.existsSync(process.argv[2])){clearInterval(wait);fs.writeFileSync(process.argv[1],JSON.stringify({parent:process.pid,grandchild:kid.pid}));${scenario === 'parent-exit' ? 'setTimeout(()=>{process.stdout.end();process.stderr.end();process.exit(0)},100);' : ''}}},10); setInterval(()=>{},1000);`;
    const controller = new AbortController();
    const startedAt = Date.now();
    const execution = runProcess(process.execPath, ['-e', parentScript, ready, grandchildReady], { cwd: directory, env: process.env, signal: controller.signal, outputFile: path.join(directory, `${scenario}.out`) });
    const watchdog = setTimeout(() => controller.abort(), 20000);
    // Attach a rejection handler while observing readiness; avoid an unhandled
    // rejection if the cleanup correctly rejects before this test awaits it.
    execution.catch(() => {});
    let fixture;
    try {
      const deadline = Date.now() + 5000;
      while (!fs.existsSync(ready)) {
        assert.ok(Date.now() < deadline, 'owned process fixture readiness timed out');
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      fixture = JSON.parse(fs.readFileSync(ready, 'utf8'));
      assert.ok(alive(fixture.grandchild));
      if (scenario === 'cancel') controller.abort();
      const result = await execution;
      const observed = { scenario, ...fixture, exitCode: result.code, cleanup: result.cleanup, parentAlive: alive(fixture.parent), grandchildAlive: alive(fixture.grandchild) };
      t.diagnostic(JSON.stringify(observed));
      assert.notEqual(result.code, 0, JSON.stringify(observed));
      assert.equal(result.cleanup.confirmedGone, true, JSON.stringify(result.cleanup));
      assert.equal(alive(fixture.parent), false);
      assert.equal(alive(fixture.grandchild), false);
      if (process.platform !== 'win32') assert.ok(result.cleanup.signals.includes('SIGKILL'), 'TERM-ignoring grandchild must require group KILL');
      else {
        assert.ok(result.cleanup.tracked.includes(fixture.grandchild), 'Windows fixture must exercise discovery of an actual outliving descendant');
        if (scenario === 'cancel') assert.equal(result.cleanup.taskkillExit, 0, JSON.stringify(result.cleanup));
      }
    } finally {
      clearTimeout(watchdog);
      controller.abort();
      if (fixture && (alive(fixture.parent) || alive(fixture.grandchild))) {
        await terminateOwnedProcess(fixture.parent, startedAt, Date.now()).catch(error => { throw new Error(`Fixture cleanup not confirmed: ${JSON.stringify(error.cleanup)}`); });
      }
      await execution.catch(error => { throw new Error(`Owned cleanup diagnostic: ${JSON.stringify(error.cleanup ?? error.message)}`); });
    }
  }
});

test('JSON validation distinguishes no-test packages, permitted named skips, failures and zero execution', () => {
  const inv = inventory();
  const expected = new Map([[pipelinePackage, inv.get(pipelinePackage)]]);
  const tops = new Set([id(pipelinePackage, 'TestPipeline')]);
  const rules = [{ package: pipelinePackage, test: 'TestPipeline', reason: 'fixture unavailable', condition: 'always' }];
  const good = validateShard({ code: 0, stdout: jsonLines(eventsFor(pipelinePackage, ['TestPipeline'])) }, expected, tops);
  assert.deepEqual(good.issues, []);
  const unexpectedSkip = validateShard({ code: 0, stdout: jsonLines(eventsFor(pipelinePackage, ['TestPipeline'], { skip: 'fixture unavailable' })) }, expected, tops);
  assert.ok(unexpectedSkip.issues.some(issue => issue.startsWith('Unexpected skip')));
  const permitted = validateShard({ code: 0, stdout: jsonLines(eventsFor(pipelinePackage, ['TestPipeline'], { skip: 'fixture unavailable' })) }, expected, tops, { rules });
  assert.equal(permitted.skips.length, 1);
  assert.ok(permitted.issues.includes('Zero passing terminal leaves'));
  const failed = validateShard({ code: 1, stdout: jsonLines(eventsFor(pipelinePackage, ['TestPipeline'], { fail: true })) }, expected, tops);
  assert.ok(failed.issues.some(issue => issue.startsWith('Task exited')));
  const empty = validateShard({ code: 0, stdout: jsonLines([event(pipelinePackage, 'pass')]) }, expected, tops);
  assert.ok(empty.issues.some(issue => issue.startsWith('Missing top-level')));
  const badBuild = validateShard({ code: 1, stdout: '{"Action":"build-fail","ImportPath":"broken"}\n' }, expected, tops);
  assert.ok(badBuild.issues.includes('Build failed: broken'));
  assert.ok(validateShard({ code: 0, stdout: '{invalid}\n' }, expected, tops).issues.includes('Malformed go test JSON'));
  const noTests = validateShard({ code: 0, stdout: jsonLines([event(emptyPackage, 'skip')]) }, new Map([[emptyPackage, inv.get(emptyPackage)]]), new Set());
  assert.deepEqual(noTests.issues, []);
});

test('named PostgreSQL and deployment skips require disabled environments and the fixed reason', async t => {
  const servicePackage = `${modulePath}/internal/service`;
  const auth = 'TestAuthenticationB4/postgres/setup_before_credentials_and_local_checks';
  const deployment = 'TestStorageDeploymentS3AuthorizationRevocation';
  const authReason = 'PostgreSQL authentication concurrency unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set';
  const deploymentReason = 'real deployment not verified: set LINGUAFLOW_STORAGE_ACCEPTANCE=1 and isolated test environment';
  for (const [name, reason, env, allowed] of [
    [auth, authReason, {}, true],
    [auth, authReason, { LINGUAFLOW_TEST_POSTGRES_DSN: 'configured' }, false],
    [auth.replace('/postgres/', '/sqlite3/'), authReason, {}, false],
    ['TestUnknownAuth/postgres/new_case', authReason, {}, false],
    [auth, 'different skip reason', {}, false],
    [deployment, deploymentReason, {}, true],
    [deployment, deploymentReason, { LINGUAFLOW_STORAGE_ACCEPTANCE: '1' }, false],
    [deployment, 'real deployment not verified: missing LINGUAFLOW_TEST_S3_ENDPOINT', {}, false],
    [`${deployment}/unknown_child`, deploymentReason, {}, false],
  ]) {
    const top = name.split('/')[0];
    const expected = new Map([[servicePackage, { name: servicePackage, tops: [top] }]]);
    const validation = validateShard({ code: 0, stdout: jsonLines(eventsFor(servicePackage, [name], { skip: reason })) }, expected, new Set([id(servicePackage, top)]), { env });
    assert.equal(validation.skips.length, allowed ? 1 : 0, name);
    assert.equal(validation.issues.some(issue => issue.startsWith('Unexpected skip:')), !allowed, name);
  }
  for (const critical of ['TestMemberResponseWorkMemory', 'TestOwnedDeferredMainPlanLifecycle', 'TestStagedWindowForeignScopeLoadingWaitsForClaimOrExit/claim_and_release']) {
    const top = critical.split('/')[0];
    const expected = new Map([[pipelinePackage, { name: pipelinePackage, tops: [top] }]]);
    const validation = validateShard({ code: 0, stdout: jsonLines(eventsFor(pipelinePackage, [critical], { skip: authReason })) }, expected, new Set([id(pipelinePackage, top)]));
    assert.ok(validation.issues.some(issue => issue.startsWith('Unexpected skip:')), critical);
    assert.ok(requiredTests[pipelinePackage].includes(critical), critical);
    const round = validateRound([{ id: 'critical', validation }], expected, { [pipelinePackage]: [critical] });
    assert.ok(round.issues.some(issue => issue.startsWith('Missing/failed/skipped required test:')), critical);
  }
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const summary = await runCI({ root, mode: 'plain', outputDir: 'storage-flag', execute: fakeTask().execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: { LINGUAFLOW_STORAGE_ACCEPTANCE: '1', LINGUAFLOW_TEST_POSTGRES_DSN: 'fixture-dsn-not-persisted' } });
  assert.equal(summary.environment.storageAcceptanceEnabled, true);
  assert.equal(summary.environment.postgresConfigured, true);
  assert.ok(!JSON.stringify(summary).includes('fixture-dsn-not-persisted'));
});

test('split plan preserves precise original selectors and rejects cross-shard terminal duplication', () => {
  const inv = inventory();
  const plan = planShards('race-atomic', inv, resolvePackageParallelism('auto', () => 4));
  assert.equal(plan.length, 4);
  assert.deepEqual(plan.map(shard => shard.effectiveP), [8, 1, 1, 1]);
  assert.deepEqual(planShards('race-atomic', inv, 1).map(shard => shard.effectiveP), [1, 1, 1, 1]);
  assert.deepEqual(planShards('race-atomic', inv, 2).map(shard => shard.effectiveP), [2, 1, 1, 1]);
  assert.equal(planShards('race-atomic', inv, 2, 2).length, 6);
  assert.equal(planShards('plain', inv, 2, 3).length, 4);
  assert.deepEqual(planShards('race-atomic', inv, 12).map(shard => shard.effectiveP), [12, 1, 1, 1]);
  for (const [cpus, expected] of [[1, 2], [4, 8], [6, 12]]) assert.equal(resolvePackageParallelism('auto', () => cpus), expected);
  assert.equal(resolvePackageParallelism('auto', () => 4, 1.5), 6);
  assert.equal(resolvePackageParallelism('auto', () => 3, 1.5), 5);
  assert.equal(resolvePackageParallelism(12, () => { throw new Error('Explicit value must bypass CPU detection'); }, 0), 12);
  for (const factor of [0, -1, NaN, Infinity, '1.5']) assert.throws(() => resolvePackageParallelism('auto', () => 4, factor), /PACKAGE_PARALLELISM_FACTOR/);
  for (const cpus of [0, -1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER]) assert.throws(() => resolvePackageParallelism('auto', () => cpus), /CPU|PACKAGE_PARALLELISM/);
  for (const invalid of [0, -1, 2.5, '2', NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.throws(() => planShards('plain', inv, invalid), /PACKAGE_PARALLELISM/);
  assert.deepEqual(plan[1].extra, ['-skip', `^${epubTop}$`]);
  assert.equal(plan[2].extra[1], `^${epubTop}$/^dense_256_seven_keys_512_words$`);
  assert.equal(plan[2].timeout, '15m');
  assert.equal(plan[3].timeout, '15m');
  assert.equal(planShards('plain', inv).length, 4);
  const validation = { issues: [], tops: [], skips: [], leaves: [{ name: id(pipelinePackage, 'TestPipeline'), pass: 1 }] };
  const round = validateRound([{ id: 'a', validation }, { id: 'b', validation }], new Map(), {});
  assert.ok(round.issues.some(issue => issue.includes('multiple shards')));
});

test('memory auto package batches partition every package once and reject missing or duplicated execution', () => {
  const inv = inventory();
  for (let i = 0; i < 9; i++) {
    const name = `${modulePath}/internal/extra${i}`;
    inv.set(name, { name, tops: [`TestExtra${i}`], noTestFiles: false });
  }
  const plan = planShards('race-atomic', inv, 4, 1, { initialWidth: 4, incrementWidth: 2 });
  const batches = plan.filter(shard => shard.id.startsWith('packages'));
  assert.deepEqual(batches.map(shard => shard.packages.length), [4, 2, 2, 2, 1]);
  assert.deepEqual(batches.flatMap(shard => shard.packages), [...inv.keys()].filter(name => name !== workerPackage));
  const records = plan.map(shard => {
    const expected = new Map(shard.packages.map(name => [name, inv.get(name)]));
    const events = shard.packages.flatMap(pkg => {
      const names = shard.requiredLeaf ? [shard.requiredLeaf.split('::')[1]] : inv.get(pkg).tops.filter(top => shard.tops.includes(id(pkg, top)));
      return eventsFor(pkg, names);
    });
    return { ...shard, validation: validateShard({ code: 0, stdout: jsonLines(events), stderr: '' }, expected, new Set(shard.tops)) };
  });
  assert.deepEqual(validateRound(records, inv, { [workerPackage]: epubLeaves }).issues, []);
  const missing = validateRound(records.filter(record => record.id !== batches.at(-1).id), inv, {});
  assert.ok(missing.issues.includes(`Package coverage mismatch: ${batches.at(-1).packages[0]} expected 1`));
  const empty = { id: 'duplicate-empty', validation: validateShard({ code: 0, stdout: jsonLines([event(emptyPackage, 'skip')]), stderr: '' }, new Map([[emptyPackage, inv.get(emptyPackage)]]), new Set()) };
  assert.ok(validateRound([...records, empty], inv, {}).issues.includes(`Package coverage mismatch: ${emptyPackage} expected 1`), 'no-test packages must also execute exactly once');
  assert.ok(validateRound([...records, records[0]], inv, {}).issues.some(issue => issue.includes('multiple shards')));
});

test('memory runner overlaps batches and heavy shards beyond the initial target, joins failures and rejects invalid stdout coverage', async t => {
  const additionalPackages = Array.from({ length: 9 }, (_, i) => ({ name: `${modulePath}/internal/extra${i}`, tops: [`TestExtra${i}`], noTestFiles: false }));
  for (const scenario of [{ name: 'complete' }, { name: 'hosted' }, { name: 'failed', failShard: 'packages' }, { name: 'unknown', extraStdout: 'unknown stdout\n' }]) {
    const root = temporary(t);
    fs.mkdirSync(path.join(root, 'backend'));
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const entered = new Set(), completed = new Set();
    t.after(release);
    const fake = fakeTask({ ...scenario, additionalPackages, failureExitCode: 137, shardProcess: async ({ options }) => {
      const shard = path.basename(path.dirname(options.outputFile));
      entered.add(shard);
      options.onSpawn?.({ pid: 100 + entered.size });
      assert.equal(options.env.GOMAXPROCS, '4', 'CPU diagnostics must not reduce the configured Go allowance');
      if (entered.size === 8) release();
      await gate;
      completed.add(shard);
    } });
    const monitor = { sample: async groups => ({ atMs: Date.now(), availableBytes: 100 * 1024 ** 3, totalBytes: 128 * 1024 ** 3, cpuQuota: null, allowedIds: [], cpuLoadKnown: false, sharedBusyCores: null, busyCores: null, owned: groups.map(group => ({ id: group.id, rssBytes: 64 * 1024 ** 2, cpuCores: null, complete: true })), shared: [] }) };
    const complete = ['complete', 'hosted'].includes(scenario.name);
    const summary = await runCI({ root, mode: 'race-atomic', outputDir: scenario.name, availableParallelism: () => 2, execute: fake.execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: { ...(scenario.name === 'hosted' ? { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted' } : {}), GOMAXPROCS: '4', LINGUAFLOW_CI_PERFORMANCE_ROUNDS: complete ? '1' : '2' }, resourceOptions: { monitor } });
    assert.equal(summary.status, complete ? 'passed' : 'failed', JSON.stringify(summary.issues));
    assert.equal(summary.shards.length, 8);
    assert.equal(entered.size, 8, 'all independent jobs must enter before any fake child completes');
    assert.equal(completed.size, 8, 'native failure must still join all entered jobs');
    const batches = summary.shards.filter(shard => shard.id.startsWith('packages'));
    assert.deepEqual(batches.map(shard => shard.packages.length), [4, 2, 2, 2, 1]);
    assert.equal(new Set(batches.flatMap(shard => shard.packages)).size, 11);
    assert.equal(batches.reduce((sum, shard) => sum + shard.effectiveP, 0), 11, '2/core is an initial batch target, not an aggregate running cap');
    assert.ok(batches.every(shard => shard.cliArgs.includes(`-p=${shard.effectiveP}`) && shard.resourceBudget.memoryReservationBytes === shard.effectiveP * 1024 ** 3));
    assert.equal(summary.resources.policy.admissionMode, 'memory');
    assert.equal(summary.resources.cpuPool.hardBound, false);
    assert.deepEqual(summary.identity, summary.finalIdentity);
    assert.equal(summary.resources.policy.minFreeBytes, scenario.name === 'hosted' ? 0 : 1024 ** 3);
    if (complete) {
      assert.equal(summary.coverage.profiles.length, 8);
      assert.equal(new Set(summary.coverage.profiles).size, 8);
      assert.equal(new Set(summary.shards.map(shard => shard.profilePath)).size, 8);
    } else {
      assert.equal(summary.coverage, undefined);
      assert.ok(!fs.existsSync(path.join(root, 'backend', scenario.name, 'coverage.out')));
      assert.ok(summary.issues.includes('Performance repeat round not executed because the first complete round failed'));
      if (scenario.name === 'failed') assert.ok(batches.every(shard => shard.exitCode === 137));
      else assert.ok(batches.every(shard => shard.validation.issues.includes('Non-JSON output on go test stdout')));
    }
  }
});

test('memory runner numeric override retains a single package invocation', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const summary = await runCI({ root, mode: 'race', outputDir: 'numeric', packageParallelism: 12, execute: fakeTask().execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
  assert.equal(summary.status, 'passed', JSON.stringify(summary.issues));
  const packages = summary.shards.filter(shard => shard.id.startsWith('packages'));
  assert.equal(packages.length, 1);
  assert.equal(packages[0].id, 'packages');
  assert.equal(packages[0].effectiveP, 12);
  assert.ok(packages[0].cliArgs.includes('-p=12'));
});

test('atomic profiles sum equal blocks and reject missing, wrong-mode, conflicting or overflowing data', t => {
  const directory = temporary(t);
  const make = (name, data, packages = [workerPackage]) => { const file = path.join(directory, name); fs.writeFileSync(file, data); return { id: name, path: file, packages }; };
  const a = make('a', 'mode: atomic\nworker.go:1.1,2.1 1 4\n');
  const b = make('b', 'mode: atomic\nworker.go:1.1,2.1 1 7\n');
  const output = path.join(directory, 'merged');
  mergeAtomicProfiles([a, b], output);
  assert.equal(fs.readFileSync(output, 'utf8'), 'mode: atomic\nworker.go:1.1,2.1 1 11\n');
  assert.throws(() => mergeAtomicProfiles([{ id: 'missing', path: path.join(directory, 'missing'), packages: [workerPackage] }], output), /Missing profile/);
  assert.throws(() => mergeAtomicProfiles([make('set', 'mode: set\nworker.go:1.1,2.1 1 1\n')], output), /Wrong coverage/);
  assert.throws(() => mergeAtomicProfiles([a, make('conflict', 'mode: atomic\nworker.go:1.1,2.1 2 1\n')], output), /Inconsistent/);
  assert.throws(() => mergeAtomicProfiles([a, make('partition', 'mode: atomic\nworker.go:1.1,1.5 1 1\nworker.go:1.5,2.1 1 1\n')], output), /Inconsistent/);
  assert.throws(() => mergeAtomicProfiles([make('max', 'mode: atomic\nworker.go:1.1,2.1 1 9223372036854775807\n'), b], output), /overflow/);
  const complete = make('complete', 'mode: atomic\na.go:1.1,2.1 1 1\nb.go:1.1,2.1 1 1\n');
  const missingFile = make('missing-file', 'mode: atomic\na.go:1.1,2.1 1 1\n');
  assert.throws(() => mergeAtomicProfiles([complete, missingFile], output), /package file\+block set/);
  assert.throws(() => mergeAtomicProfiles([make('empty', 'mode: atomic\n')], output), /Empty coverage/);
  const other = make('other-package', `mode: atomic\n${pipelinePackage}/pipeline.go:1.1,2.1 1 1\n`, [pipelinePackage]);
  const distinctOutput = path.join(directory, 'distinct-packages');
  mergeAtomicProfiles([complete, other], distinctOutput);
  assert.ok(fs.readFileSync(distinctOutput, 'utf8').includes('b.go:1.1,2.1 1 1'));
});

test('native zero-statement coverage blocks retain coordinates and counts', t => {
  const directory = temporary(t);
  const make = (name, line) => { const file = path.join(directory, name); fs.writeFileSync(file, `mode: atomic\n${line}\n`); return { id: name, path: file, packages: [workerPackage] }; };
  const output = path.join(directory, 'merged-zero');
  mergeAtomicProfiles([make('zero-a', 'worker.go:123.16,123.16 0 57'), make('zero-b', 'worker.go:123.16,123.16 0 3')], output);
  assert.equal(fs.readFileSync(output, 'utf8'), 'mode: atomic\nworker.go:123.16,123.16 0 60\n');
  for (const [name, line] of [
    ['nonzero-statements', 'worker.go:123.16,123.16 1 57'],
    ['reversed-lines', 'worker.go:124.16,123.16 0 57'],
    ['reversed-columns', 'worker.go:123.17,123.16 0 57'],
    ['negative', 'worker.go:123.16,123.16 0 -1'],
    ['overflow', 'worker.go:123.16,123.16 0 9223372036854775808'],
  ]) assert.throws(() => mergeAtomicProfiles([make(name, line)], output), /Malformed|Invalid|overflow/);
  const duplicate = make('duplicate-zero', 'worker.go:123.16,123.16 0 1\nworker.go:123.16,123.16 0 2');
  assert.throws(() => mergeAtomicProfiles([duplicate], output), /Duplicate/);
});

test('fake Task smoke produces a complete atomic round and keeps repeat profiles separate', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const fake = fakeTask();
  const summary = await runCI({ root, mode: 'race-atomic', outputDir: 'evidence', packageParallelism: 12, packageParallelismFactor: 0, availableParallelism: () => { throw new Error('Explicit value must bypass CPU detection'); }, execute: fake.execute, identity: async () => ({ head: 'fixture-sha', sourceSha256: 'fixture-hash' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves, [pipelinePackage]: ['TestPipeline'] }, rules: [], env: { GOFLAGS: '-tags=fixture -p=4', LINGUAFLOW_CI_PERFORMANCE_ROUNDS: '2' } });
  assert.equal(summary.status, 'passed', JSON.stringify(summary.issues));
  assert.equal(summary.shards.length, 6);
  assert.equal(summary.coverage.profiles.length, 4);
  assert.equal(summary.manifest.bulkPackageParallelism, 12);
  assert.equal(summary.manifest.bulkPackageParallelismFactor, null);
  assert.equal(summary.environment.availableCPUCount, null);
  assert.equal(summary.environment.GOFLAGS, '-tags=fixture -p=4 -p=1');
  assert.deepEqual(summary.manifest.plan.map(shard => shard.effectiveP), [12, 1, 1, 1, 1, 1]);
  assert.ok(fake.calls.find(args => args[0] === 'backend:test:ci:inventory')[1].includes('-p=12'));
  assert.ok(fake.calls.find(args => args[1]?.includes('-list'))[1].includes('-p=12'));
  for (const shard of summary.shards) assert.ok(shard.cliArgs.includes(`-p=${shard.effectiveP}`), shard.id);
  assert.deepEqual(summary.finalIdentity, summary.identity);
  assert.deepEqual(summary.lastObservedIdentity, summary.identity);
  assert.equal(summary.manifest.runnerSHA256, summary.runnerSHA256);
  assert.equal(summary.manifest.contractSHA256, summary.contractSHA256);
  assert.equal(summary.environment.actualHostOS, process.platform);
  assert.equal(summary.environment.actualHostArch, process.arch);
  assert.equal(summary.environment.hostRelease, os.release());
  assert.equal(summary.environment.nodeBinary, process.execPath);
  assert.equal(summary.environment.storageAcceptanceEnabled, false);
  const merged = fs.readFileSync(path.join(root, 'backend/evidence/coverage.out'), 'utf8');
  assert.ok(merged.includes('worker.go:1.1,2.1 1 6'));
  assert.ok(fs.readFileSync(path.join(root, 'backend/evidence/round-1/race-atomic/dense-r1/test-events.jsonl'), 'utf8').includes(epubLeaves[0]));
  assert.ok(!merged.includes('198'));
  assert.equal(fake.calls.filter(args => args[0] === 'backend:test:race').length, 6);
  assert.equal(summary.manifest.performanceRounds, 2);
  assert.ok(fake.calls.every(args => args[0] === '--version' || args[0].startsWith('backend:')));
});

test('failed first round preserves raw evidence and summary, skips repeats and never creates merged coverage', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const fake = fakeTask({ failShard: 'packages', failureExitCode: 137 });
  const summary = await runCI({ root, mode: 'race-atomic', outputDir: 'failed', execute: fake.execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted', LINGUAFLOW_CI_PERFORMANCE_ROUNDS: '2' } });
  assert.equal(summary.status, 'failed');
  assert.equal(summary.resources.policy.scope, 'github-hosted-job');
  assert.equal(summary.shards.length, 4);
  assert.equal(summary.shards[0].exitCode, 137, 'OOM-style failure must not become a passing run');
  const packageDirectory = path.join(root, 'backend/failed/round-1/race-atomic', summary.shards.find(shard => shard.id.startsWith('packages')).id);
  assert.ok(fs.existsSync(path.join(packageDirectory, 'test-results.out')));
  assert.ok(fs.existsSync(path.join(packageDirectory, 'test-events.jsonl')));
  assert.ok(fs.existsSync(path.join(root, 'backend/failed/manifest.json')));
  assert.ok(fs.existsSync(path.join(root, 'backend/failed/summary.md')));
  assert.ok(!fs.existsSync(path.join(root, 'backend/failed/coverage.out')));
  const diagnostic = { platform: 'fixture', root: 123, confirmedGone: false, diagnostic: 'owned tree could not be joined' };
  const startup = await runCI({ root, mode: 'plain', outputDir: 'cleanup-failed', execute: async () => { throw Object.assign(new Error('cleanup failed'), { cleanup: diagnostic }); }, taskBinary: process.execPath, env: {} });
  assert.equal(startup.status, 'failed');
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(root, 'backend/cleanup-failed/summary.json'), 'utf8')).cleanupFailure, diagnostic);
});

test('native Task stdout reaches runner failure reports without masking unknown output, build failures or unfinished leaves', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const annotation = "::error title=Task 'backend:test:race' failed::exit status 1\n";
  const failedJSON = jsonLines(eventsFor(pipelinePackage, ['TestPipeline'], { fail: true }));
  const passedJSON = jsonLines(eventsFor(pipelinePackage, ['TestPipeline']));
  const emptyEvents = jsonLines([event(emptyPackage, 'start'), event(emptyPackage, 'skip')]);
  const scenarios = [
    { name: 'business-failure', stdout: failedJSON + emptyEvents + annotation, code: 137, diagnostics: annotation, issue: `Unfinished/repeated/failed test: ${id(pipelinePackage, 'TestPipeline')}` },
    { name: 'unknown-stdout', stdout: passedJSON + emptyEvents + 'unknown stdout\n', code: 0, diagnostics: '', issue: 'Non-JSON output on go test stdout' },
    { name: 'build-failure', stdout: passedJSON + emptyEvents + jsonLines([{ Action: 'build-fail', ImportPath: 'broken' }]) + annotation, code: 1, diagnostics: annotation, issue: 'Build failed: broken' },
    { name: 'missing-terminal', stdout: jsonLines([event(pipelinePackage, 'start'), event(pipelinePackage, 'run', 'TestPipeline'), event(pipelinePackage, 'pass')]) + emptyEvents + annotation, code: 1, diagnostics: annotation, issue: `Unfinished/repeated/failed test: ${id(pipelinePackage, 'TestPipeline')}` },
    { name: 'zero-exit-annotation', stdout: passedJSON + emptyEvents + annotation, code: 0, diagnostics: annotation, issue: 'Task reported failure with zero exit code' },
  ];
  for (const scenario of scenarios) {
    let nativeCalls = 0;
    const stderr = 'native task stderr\n';
    const fake = fakeTask({ shardProcess: async ({ shard, options }) => {
      if (shard !== 'packages') return;
      nativeCalls++;
      assert.equal(options.taskName, 'backend:test:race', 'runner must configure the real stdout boundary');
      const cut = Math.max(1, scenario.stdout.length - 20);
      const script = `process.stdout.write(${JSON.stringify(scenario.stdout.slice(0, cut))}); process.stderr.write(${JSON.stringify(stderr)}); setTimeout(() => { process.stdout.write(${JSON.stringify(scenario.stdout.slice(cut))}); process.exitCode = ${scenario.code}; }, 10);`;
      return runProcess(process.execPath, ['-e', script], options);
    } });
    const summary = await runCI({ root, mode: 'race-atomic', outputDir: scenario.name, execute: fake.execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: { SystemRoot: process.env.SystemRoot, LINGUAFLOW_CI_PERFORMANCE_ROUNDS: '2' } });
    assert.equal(nativeCalls, 1);
    assert.equal(summary.status, 'failed', scenario.name);
    assert.equal(summary.shards.length, 4, 'failed first round must suppress all repeat shards');
    const record = summary.shards.find(shard => shard.id.startsWith('packages'));
    assert.equal(record.exitCode, scenario.code, scenario.name);
    assert.equal(record.taskDiagnostics, scenario.diagnostics, scenario.name);
    assert.ok(record.validation.issues.includes(scenario.issue), `${scenario.name}: ${JSON.stringify(record.validation.issues)}`);
    assert.equal(record.validation.issues.includes('Non-JSON output on go test stdout'), scenario.name === 'unknown-stdout', scenario.name);
    const output = path.join(root, 'backend', scenario.name);
    const directory = path.join(output, 'round-1/race-atomic', record.id);
    assert.equal(fs.readFileSync(path.join(directory, 'task-stdout.out'), 'utf8'), scenario.stdout);
    assert.equal(fs.readFileSync(path.join(directory, 'test-events.jsonl'), 'utf8'), scenario.stdout.slice(0, scenario.stdout.length - scenario.diagnostics.length));
    assert.equal(fs.readFileSync(path.join(directory, 'task-diagnostics.out'), 'utf8'), scenario.diagnostics);
    assert.equal(fs.readFileSync(path.join(directory, 'task-stderr.out'), 'utf8'), stderr);
    assert.equal(fs.readFileSync(path.join(directory, 'test-results.out'), 'utf8').replace(stderr, ''), scenario.stdout);
    assert.deepEqual(JSON.parse(fs.readFileSync(path.join(directory, 'result.json'), 'utf8')), JSON.parse(JSON.stringify(record)));
    const persisted = JSON.parse(fs.readFileSync(path.join(output, 'summary.json'), 'utf8'));
    assert.equal(persisted.status, 'failed');
    assert.equal(persisted.shards.find(shard => shard.id.startsWith('packages')).exitCode, scenario.code);
    assert.ok(summary.issues.some(issue => issue.includes(scenario.issue)));
    assert.equal(summary.coverage, undefined);
    assert.ok(fs.existsSync(record.profilePath), 'a present atomic shard profile must not override execution failure');
    assert.ok(!fs.existsSync(path.join(output, 'coverage.out')), scenario.name);
  }
});

test('invalid package parallelism never starts Task or Go discovery', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const fake = fakeTask();
  const summary = await runCI({ root, mode: 'plain', outputDir: 'invalid-parallelism', packageParallelism: 0, execute: fake.execute, taskBinary: process.execPath, env: {} });
  assert.equal(summary.status, 'failed');
  assert.ok(summary.issues.some(issue => issue.includes('PACKAGE_PARALLELISM must be auto or a positive safe integer')));
  assert.equal(fake.calls.length, 0);
});

test('automatic CI concurrency records CPU and factor while preserving first-round policy', async t => {
  for (const [mode, factor, expectedP] of [['race', undefined, 8], ['race-atomic', undefined, 8], ['plain', 1.5, 6]]) {
    const root = temporary(t);
    fs.mkdirSync(path.join(root, 'backend'));
    const fake = fakeTask({ elapsed: { synthetic: 516.86 } });
    const summary = await runCI({ root, mode, outputDir: mode, packageParallelismFactor: factor, availableParallelism: () => 4, execute: fake.execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
    assert.equal(summary.shards.length, 4);
    assert.equal(summary.manifest.performanceRounds, 1);
    assert.equal(summary.manifest.bulkPackageParallelism, expectedP);
    assert.equal(summary.manifest.bulkPackageParallelismFactor, factor ?? 2);
    assert.equal(summary.manifest.bulkPackageParallelismRequested, 'auto');
    assert.equal(summary.environment.availableCPUCount, 4);
    assert.ok(fake.calls.find(args => args[0] === 'backend:test:ci:inventory')[1].includes(`-p=${expectedP}`));
    assert.ok(fake.calls.find(args => args[1]?.includes('-list'))[1].includes(`-p=${expectedP}`));
    assert.deepEqual(summary.shards.map(shard => shard.effectiveP), [2, 1, 1, 1], 'the batch contains only two packages even when its initial target is larger');
    assert.equal(summary.status, 'passed');
    assert.equal(fake.calls.filter(args => ['backend:test:race', 'backend:test'].includes(args[0]) && !args[1]?.includes('-list')).length, 4);
  }
});

test('engineering scene boundaries preserve dense gates and native failures', async t => {
  for (const mode of ['race', 'race-atomic']) {
    for (const scenario of [
      { name: 'synthetic-boundary', elapsed: { synthetic: 540 }, passed: true },
      { name: 'synthetic-over', elapsed: { synthetic: 540.01 }, issue: `Instrumented ${mode}/synthetic EPUB scene exceeds 540s engineering target (10% margin within original 600s budget)` },
      { name: 'dense-over', elapsed: { dense: 451 }, issue: `Instrumented ${mode}/dense EPUB scene exceeds 450s engineering target (25% margin within original 600s budget)` },
      { name: 'native-fail', elapsed: { synthetic: 300 }, failShard: 'synthetic', failureExitCode: 137 },
    ]) {
      const root = temporary(t);
      fs.mkdirSync(path.join(root, 'backend'));
      const summary = await runCI({ root, mode, outputDir: scenario.name, execute: fakeTask(scenario).execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
      assert.equal(summary.manifest.performancePolicy.version, 2);
      assert.equal(summary.manifest.performancePolicy.sceneSeconds, 450);
      assert.equal(summary.manifest.performancePolicy.raceSyntheticSceneSeconds, 540);
      assert.equal(summary.manifest.performancePolicy.atomicSyntheticSceneSeconds, 540);
      assert.equal(summary.manifest.performancePolicy.responseSeconds, 90);
      assert.equal(summary.manifest.performancePolicy.originalSceneSeconds, 600);
      assert.equal(summary.manifest.performancePolicy.originalResponseSeconds, 120);
      assert.equal(summary.status, scenario.passed ? 'passed' : 'failed', `${mode}/${scenario.name}: ${JSON.stringify(summary.issues)}`);
      if (scenario.issue) assert.ok(summary.shards.some(shard => shard.validation.issues.includes(scenario.issue)), `${mode}/${scenario.name} did not retain the exact engineering gate`);
      if (scenario.failShard) {
        assert.equal(summary.shards.find(shard => shard.id === 'synthetic-r1').exitCode, 137);
        assert.ok(summary.issues.length > 0, 'A native failure must remain a failure within the engineering target');
      }
      assert.equal(fs.existsSync(path.join(root, 'backend', scenario.name, 'coverage.out')), mode === 'race-atomic' && Boolean(scenario.passed), 'Only successful atomic execution may merge coverage');
    }
  }
});

test('invalid repeat counts never start discovery and plain ignores valid opt-in repeats', async t => {
  for (const value of ['0', '-1', '1.5', 'NaN', 'Infinity', '9007199254740992']) {
    const root = temporary(t);
    fs.mkdirSync(path.join(root, 'backend'));
    const fake = fakeTask();
    const summary = await runCI({ root, mode: 'race', outputDir: 'invalid', execute: fake.execute, taskBinary: process.execPath, env: { LINGUAFLOW_CI_PERFORMANCE_ROUNDS: value } });
    assert.equal(summary.status, 'failed');
    assert.equal(fake.calls.length, 0);
  }
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  const summary = await runCI({ root, mode: 'plain', outputDir: 'plain', execute: fakeTask().execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: { LINGUAFLOW_CI_PERFORMANCE_ROUNDS: '3' } });
  assert.equal(summary.manifest.performanceRounds, 1);
  assert.equal(summary.shards.length, 4);
});

test('source changes, incorrect atomic mode and insufficient scene margin block success', async t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'backend'));
  let snapshots = 0;
  const change = await runCI({ root, mode: 'plain', outputDir: 'changed', execute: fakeTask().execute, identity: async () => ({ head: 'fixture', sourceSha256: String(snapshots++) }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
  assert.equal(change.status, 'failed');
  assert.ok(change.issues.some(issue => issue.includes('Source changed')));
  assert.notDeepEqual(change.finalIdentity, change.identity);
  const postMerge = await runCI({ root, mode: 'race-atomic', outputDir: 'post-merge-changed', execute: fakeTask().execute, identity: async (_root, outputDir) => ({ head: 'fixture', sourceSha256: fs.existsSync(path.join(outputDir, 'coverage.out')) ? 'changed-after-merge' : 'frozen' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
  assert.equal(postMerge.status, 'failed');
  assert.equal(postMerge.finalIdentity.sourceSha256, 'changed-after-merge');
  assert.ok(postMerge.issues.some(issue => issue.includes('Source changed')));
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(root, 'backend/post-merge-changed/summary.json'), 'utf8')).finalIdentity, postMerge.finalIdentity);
  for (const [name, options] of [['mode', { wrongProfile: true }], ['margin', { elapsed: 451 }]]) {
    const summary = await runCI({ root, mode: 'race-atomic', outputDir: name, execute: fakeTask(options).execute, identity: async () => ({ head: 'fixture', sourceSha256: 'fixture' }), taskBinary: process.execPath, contract: { [workerPackage]: epubLeaves }, rules: [], env: {} });
    assert.equal(summary.status, 'failed');
    assert.ok(!fs.existsSync(path.join(root, `backend/${name}/coverage.out`)));
  }
});
