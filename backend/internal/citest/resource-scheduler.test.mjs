import assert from 'node:assert/strict';
import test from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { ResourceScheduler, resourcePolicy } from './resource-scheduler.mjs';
import { bindControllerCPU, cpuIds, createResourceMonitor, GiB, linuxLimits, windowsSnapshot } from './resources.mjs';

const exec = promisify(execFile);
const hosted = { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted' };
const jobs = [{ id: 'worker-remainder' }, { id: 'dense-r1' }, { id: 'synthetic-r1' }];

function fixture({ memory = 12 * GiB, cores = 4, cpu = () => .1, complete = true, failMetricsAt = Infinity, emitFailure = false, env = hosted, wslHost = null, sharedBusyCores = 0 } = {}) {
  let time = 0, pid = 100, restored = 0;
  const running = new Map(), launches = [], evidence = [];
  const clock = { now: () => time, sleep: async (ms, signal) => {
    // Drain promise completion/cleanup before advancing the simulated timer,
    // matching a real cancellable timeout losing Promise.race to completion.
    await new Promise(resolve => setImmediate(resolve));
    if (!signal?.aborted) time += ms;
  } };
  const monitor = { sample: async groups => {
    if (time >= failMetricsAt) throw new Error('physical metric fixture failure');
    for (const [id, run] of [...running]) if (time >= run.until) { running.delete(id); run.finish(0); }
    const host = typeof wslHost === 'function' ? wslHost(time) : wslHost;
    const availableBytes = Math.min(typeof memory === 'function' ? memory(time, groups) : memory, host?.availableBytes ?? Infinity);
    return { atMs: time, availableBytes, totalBytes: 16 * GiB, cpuQuota: cores, allowedIds: Array.from({ length: cores }, (_, i) => i), cpuLoadKnown: true, sharedBusyCores, busyCores: sharedBusyCores, wslHost: host, owned: groups.map(group => ({ id: group.id, pid: group.pid, rssBytes: 64 * 1024 ** 2, cpuCores: cpu(time, group), complete })), shared: [] };
  } };
  const scheduler = new ResourceScheduler({ env, monitor, clock, bind: async (_monitor, snapshot, { reserve }) => {
    const width = Math.floor(snapshot.cpuQuota - reserve);
    return { ids: snapshot.allowedIds.slice(0, width), cores: width, method: 'fixture-controller-inherit', restore: async () => { restored++; } };
  }, emit: event => { evidence.push(event); if (emitFailure && event.kind === 'complete') throw new Error('completion evidence write failed'); } });
  const execute = (job, budget) => new Promise(resolve => {
    launches.push({ id: job.id, atMs: time, gomaxprocs: budget.gomaxprocs });
    budget.onSpawn({ pid: ++pid });
    let done = false;
    const finish = code => {
      if (done) return;
      done = true; budget.signal.removeEventListener('abort', abort);
      resolve({ id: job.id, exitCode: code, cleanup: { confirmedGone: true }, validation: { issues: code ? ['fixture aborted'] : [] } });
    };
    const abort = () => { running.delete(job.id); finish(1); };
    budget.signal.addEventListener('abort', abort, { once: true });
    running.set(job.id, { until: time + (job.duration ?? 120000), finish });
  });
  return { scheduler, execute, launches, evidence, running, now: () => time, restored: () => restored };
}

test('CPU ids and nested cgroup limits reject damaged data and honor ancestor physical/quota limits', async () => {
  assert.deepEqual(cpuIds('2,4-6'), [2, 4, 5, 6]);
  for (const value of ['', '3-2', 'NaN', '9999999999999999999999999999999999', '0-100000']) assert.throws(() => cpuIds(value));
  const files = new Map([
    ['/proc/self/cgroup', '0::/parent/child\n'],
    ['/proc/self/mountinfo', '27 23 0:25 /parent /sys/fs/cgroup rw - cgroup2 cgroup rw\n'],
    ['/sys/fs/cgroup/child/cpu.max', '200000 100000'],
    ['/sys/fs/cgroup/child/cpu.stat', 'usage_usec 1000000\n'],
    ['/sys/fs/cgroup/child/memory.max', String(4 * GiB)],
    ['/sys/fs/cgroup/child/memory.current', String(GiB)],
    ['/sys/fs/cgroup/cpu.max', '400000 100000'],
    ['/sys/fs/cgroup/memory.max', String(8 * GiB)],
    ['/sys/fs/cgroup/memory.current', String(7 * GiB)],
  ]);
  const limits = await linuxLimits(async file => files.get(file) ?? null);
  assert.equal(limits.cpuQuota, 2);
  assert.equal(limits.memoryHeadroom, GiB);
  assert.equal(limits.quotaSource, '/sys/fs/cgroup/child');
  files.delete('/sys/fs/cgroup/child/memory.current');
  await assert.rejects(linuxLimits(async file => files.get(file) ?? null), /Missing cgroup memory/);
});

test('CI=true alone cannot grant hosted scope or disable shared-host CPU reserve', () => {
  assert.equal(resourcePolicy({ CI: 'true' }).scope, 'shared-host');
  assert.equal(resourcePolicy(hosted).scope, 'github-hosted-job');
  assert.equal(resourcePolicy({ ...hosted, RUNNER_ENVIRONMENT: 'self-hosted' }).scope, 'shared-host');
  assert.equal(resourcePolicy({ ...hosted, WSL_DISTRO_NAME: 'fixture' }).scope, 'shared-host');
  assert.equal(resourcePolicy({ LINGUAFLOW_CI_CPU_RESERVE: '0' }).cpuReserve, 0);
  assert.throws(() => resourcePolicy({ LINGUAFLOW_CI_CPU_RESERVE: '-1' }), /at least 0/);
  assert.throws(() => resourcePolicy({ LINGUAFLOW_CI_MIN_FREE_MIB: '1000' }), /at least 1024/);
});

test('bulk records CPU multiplier target separately from shared physical-memory effective slots', async () => {
  const ci = fixture();
  await ci.scheduler.initialize();
  const ciBudget = await ci.scheduler.bulkBudget(8, 4);
  assert.equal(ciBudget.effectiveP, 8);
  assert.equal(ciBudget.gomaxprocs, 1);
  assert.equal(ciBudget.pool.cores, 4);
  await ci.scheduler.close();
  const desktop = fixture({ memory: 2.6 * GiB, cores: 16, env: {} });
  await desktop.scheduler.initialize();
  const budget = await desktop.scheduler.bulkBudget(32, 4);
  assert.equal(budget.requestedP, 32);
  assert.equal(budget.effectiveP, 1);
  assert.equal(budget.gomaxprocs, 4);
  assert.equal(budget.pool.cores, 14);
  await desktop.scheduler.close();
  const fullCPU = fixture({ memory: 2.6 * GiB, cores: 16, sharedBusyCores: 1.5, env: { LINGUAFLOW_CI_CPU_RESERVE: '0' } });
  await fullCPU.scheduler.initialize();
  const fullBudget = await fullCPU.scheduler.bulkBudget(32, 4);
  assert.equal(fullBudget.pool.cores, 16);
  assert.equal(fullBudget.effectiveP, 1);
  assert.equal(fullBudget.gomaxprocs, 4);
  await fullCPU.scheduler.close();
});

test('WSL accepts explicit zero reserve but host CPU load and physical memory still gate admission', async () => {
  const state = fixture({ env: { LINGUAFLOW_CI_CPU_RESERVE: '0' }, wslHost: { availableBytes: 8 * GiB, cpuCores: 16, busyCores: 15.5 } });
  await state.scheduler.initialize();
  assert.equal(state.scheduler.policy.cpuReserve, 0);
  assert.equal(state.scheduler.pool.cores, 4);
  await state.scheduler.run(jobs.map(job => ({ ...job, duration: 40000 })), state.execute);
  assert.deepEqual(state.launches.map(launch => launch.atMs), [0, 40000, 80000]);
  await state.scheduler.close();
  const memory = fixture({ env: { LINGUAFLOW_CI_CPU_RESERVE: '0' }, wslHost: time => ({ availableBytes: time < 20000 ? .75 * GiB : 8 * GiB, cpuCores: 16, busyCores: 0 }) });
  await memory.scheduler.initialize();
  assert.equal(memory.now(), 20000, 'guest free memory must not bypass Windows physical guard');
  assert.equal(memory.launches.length, 0);
  await memory.scheduler.close();
});

test('missing Linux taskset starts no mutator and falls back to proved serial admission', async () => {
  const state = fixture();
  let calls = 0;
  state.scheduler.bind = (monitor, snapshot, options) => bindControllerCPU(monitor, snapshot, { ...options, platform: 'linux', execute: async () => {
    calls++; throw Object.assign(new Error('taskset unavailable'), { code: 'ENOENT' });
  } });
  await state.scheduler.initialize();
  assert.equal(calls, 1, 'a missing executable must not trigger a fictional restoration attempt');
  assert.equal(state.scheduler.pool.hardBound, false);
  const budget = await state.scheduler.bulkBudget(8, 4);
  assert.equal(budget.effectiveP, 1);
  assert.equal(budget.gomaxprocs, 1);
  await state.scheduler.close();
});

test('longest scene launches with four cores; a stable idle window admits all three without a fixed two-slot cap', async () => {
  const state = fixture();
  await state.scheduler.initialize();
  const result = await state.scheduler.run(jobs.map(job => ({ ...job, duration: job.id === 'dense-r1' ? 15000 : 120000 })), state.execute);
  assert.equal(state.launches[0].id, 'synthetic-r1');
  assert.equal(state.launches[0].gomaxprocs, 4);
  assert.ok(state.launches[1].atMs >= 30000);
  assert.equal(state.launches.length, 3);
  assert.ok(state.evidence.some(event => event.kind === 'sample' && event.owned.length === 3));
  assert.deepEqual(result.map(record => record.id), jobs.map(job => job.id));
  assert.equal(state.running.size, 0);
  await state.scheduler.close();
  assert.equal(state.restored(), 1);
});

test('startup CPU peaks expire; later thirty seconds of spare resource can admit another long scene', async () => {
  const state = fixture({ cpu: time => time < 10000 ? 3 : .1 });
  await state.scheduler.initialize();
  await state.scheduler.run(jobs, state.execute);
  assert.ok(state.launches[1].atMs >= 60000);
  assert.ok(state.launches[1].atMs < 120000, 'historic startup peak must not reserve four CPUs forever');
  await state.scheduler.close();
});

test('physical headroom or unproved RSS prevents expansion; completion releases resources without another observation delay', async () => {
  for (const options of [{ memory: 2.6 * GiB }, { complete: false }]) {
    const state = fixture(options);
    await state.scheduler.initialize();
    await state.scheduler.run(jobs.map(job => ({ ...job, duration: 40000 })), state.execute);
    assert.deepEqual(state.launches.map(launch => launch.atMs), [0, 40000, 80000]);
    await state.scheduler.close();
  }
});

test('projected inflight growth pauses additions and lets all started shards finish', async () => {
  const state = fixture({ memory: time => time >= 70000 ? 2 * GiB : 12 * GiB });
  await state.scheduler.initialize();
  const results = await state.scheduler.run(jobs, state.execute);
  assert.equal(state.launches.length, 3);
  assert.ok(results.every(result => result.exitCode === 0));
  const defer = state.evidence.find(event => event.kind === 'projected-memory-defer');
  assert.equal(defer.running, 3);
  assert.equal(defer.pending, 0);
  assert.ok(defer.availableBytes > GiB);
  assert.ok(defer.forecast.memoryHeadroom < 0);
  assert.ok(!state.evidence.some(event => event.kind === 'physical-guard-stop'));
  assert.equal(state.running.size, 0);
  assert.equal(state.scheduler.active.size, 0);
  await state.scheduler.close();
  const pending = fixture({ memory: time => time >= 10000 && time < 50000 ? 2 * GiB : 12 * GiB });
  await pending.scheduler.initialize();
  const pendingResults = await pending.scheduler.run(jobs.map(job => ({ ...job, duration: 40000 })), pending.execute);
  assert.ok(pendingResults.every(result => result.exitCode === 0));
  assert.equal(pending.launches[1].atMs, 50000, 'new work waits for headroom while the first shard finishes normally');
  assert.ok(pending.evidence.some(event => event.kind === 'projected-memory-defer' && event.pending === 2));
  await pending.scheduler.close();
});

test('actual physical guard cancels and joins only owned tasks even with no pending shards', async () => {
  const state = fixture({ memory: time => time >= 70000 ? .75 * GiB : 12 * GiB, env: { LINGUAFLOW_CI_CPU_RESERVE: '0' } });
  await state.scheduler.initialize();
  await assert.rejects(state.scheduler.run(jobs, state.execute), /Actual available physical memory crossed/);
  assert.equal(state.launches.length, 3);
  const stop = state.evidence.find(event => event.kind === 'physical-guard-stop');
  assert.equal(stop.reason, 'actual-physical-floor');
  assert.equal(stop.owned.length, 3);
  assert.ok(stop.availableBytes < GiB);
  assert.equal(state.running.size, 0);
  assert.equal(state.scheduler.active.size, 0);
  await state.scheduler.close();
});

test('hosted physical low memory warns and pauses new work while started shards finish', async () => {
  const state = fixture({ memory: time => time >= 70000 ? .75 * GiB : 12 * GiB });
  await state.scheduler.initialize();
  const results = await state.scheduler.run(jobs, state.execute);
  assert.equal(state.scheduler.policy.scope, 'github-hosted-job');
  assert.equal(state.launches.length, 3);
  assert.ok(results.every(result => result.exitCode === 0));
  assert.ok(state.evidence.some(event => event.kind === 'physical-low-memory' && event.running === 3 && event.action === 'defer-new'));
  assert.ok(!state.evidence.some(event => event.kind === 'physical-guard-stop'));
  assert.equal(state.running.size, 0);
  await state.scheduler.close();
  const pending = fixture({ memory: time => time >= 10000 && time < 50000 ? .75 * GiB : 12 * GiB });
  await pending.scheduler.initialize();
  const pendingResults = await pending.scheduler.run(jobs.map(job => ({ ...job, duration: 40000 })), pending.execute);
  assert.ok(pendingResults.every(result => result.exitCode === 0));
  assert.equal(pending.launches[1].atMs, 50000);
  assert.ok(pending.evidence.some(event => event.kind === 'physical-low-memory' && event.pending === 2));
  await pending.scheduler.close();
});

test('source, metric and evidence failures stop new launches and join every started task before restoration', async () => {
  const source = fixture();
  await source.scheduler.initialize();
  await assert.rejects(source.scheduler.run(jobs, source.execute, { beforeLaunch: job => { if (job.id !== 'synthetic-r1') throw new Error('source changed'); } }), /source changed/);
  assert.deepEqual(source.launches.map(launch => launch.id), ['synthetic-r1']);
  assert.equal(source.running.size, 0);
  await source.scheduler.close();
  const metric = fixture({ failMetricsAt: 5000 });
  metric.scheduler.policy.metricFailureMs = 10000;
  await metric.scheduler.initialize();
  await assert.rejects(metric.scheduler.run(jobs, metric.execute), /metrics unavailable/);
  assert.equal(metric.running.size, 0);
  assert.equal(metric.launches.length, 1);
  await metric.scheduler.close();
  const evidence = fixture({ emitFailure: true });
  await evidence.scheduler.initialize();
  await assert.rejects(evidence.scheduler.run(jobs.map(job => ({ ...job, duration: 10000 })), evidence.execute), /evidence write failed/);
  assert.equal(evidence.running.size, 0);
  await evidence.scheduler.close();
});

test('RSS excludes the parent Task ancestry and includes every owned compiler descendant', async () => {
  const node = process.pid;
  const rows = [
    { pid: 100, parent: 1, birth: 1, name: 'task.exe', rssBytes: 20, cpuSeconds: 0 },
    { pid: node, parent: 100, birth: 2, name: 'node.exe', rssBytes: 30, cpuSeconds: 0 },
    { pid: 300, parent: node, birth: 3, name: 'task.exe', rssBytes: 40, cpuSeconds: 0 },
    { pid: 301, parent: 300, birth: 4, name: 'go.exe', rssBytes: 50, cpuSeconds: 0 },
    { pid: 302, parent: 301, birth: 5, name: 'compile.exe', rssBytes: 60, cpuSeconds: 0 },
    { pid: 400, parent: 100, birth: 6, name: 'go.exe', rssBytes: 70, cpuSeconds: 0 },
  ];
  const monitor = createResourceMonitor({ platform: 'win32', windows: async () => ({ availableBytes: 2.6 * GiB, totalBytes: 16 * GiB, mask: 'F', processes: rows }) });
  const snapshot = await monitor.sample([{ id: 'synthetic-r1', pid: 300 }]);
  assert.equal(snapshot.owned[0].rssBytes, 150);
  assert.equal(snapshot.owned[0].processCount, 3);
  assert.deepEqual(snapshot.shared.map(row => row.pid), [400]);
  assert.equal(snapshot.shared[0].rssBytes, 70);
});

test('native controller affinity is inherited before a lightweight child and restored after it exits', async t => {
  if (!['win32', 'linux'].includes(process.platform) || process.env.WSL_DISTRO_NAME) return t.skip('requires supported native host with known physical/CPU budget');
  const monitor = createResourceMonitor();
  const snapshot = await monitor.sample();
  assert.ok(snapshot.availableBytes > 0);
  const pool = await bindControllerCPU(monitor, snapshot, { reserve: snapshot.cpuQuota > 1 ? 1 : 0 });
  try {
    if (process.platform === 'win32') {
      const result = await exec('pwsh.exe', ['-NoProfile', '-NonInteractive', '-Command', '(Get-Process -Id $PID).ProcessorAffinity.ToInt64().ToString("X")'], { windowsHide: true });
      const expected = pool.ids.reduce((bits, id) => bits | (1n << BigInt(id)), 0n).toString(16);
      assert.equal(result.stdout.trim().toLowerCase(), expected);
    } else {
      const result = await exec(process.execPath, ['-e', 'console.log(require("node:fs").readFileSync("/proc/self/status","utf8").match(/^Cpus_allowed_list:\\s*(.+)$/m)[1])']);
      assert.deepEqual(cpuIds(result.stdout.trim()), pool.ids);
    }
  } finally { await pool.restore(); }
  if (process.platform === 'win32') assert.equal((await windowsSnapshot()).mask, snapshot.mask);
});
