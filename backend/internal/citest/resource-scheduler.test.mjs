import assert from 'node:assert/strict';
import test from 'node:test';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { ResourceScheduler, resourcePolicy } from './resource-scheduler.mjs';
import { cpuIds, createResourceMonitor, GiB, linuxLimits, windowsSnapshotScript } from './resources.mjs';

const exec = promisify(execFile);
const hosted = { GITHUB_ACTIONS: 'true', RUNNER_ENVIRONMENT: 'github-hosted' };
const jobs = [{ id: 'worker-remainder' }, { id: 'dense-r1' }, { id: 'synthetic-r1' }];
const memoryAdmission = {};

function fixture({ memory = 12 * GiB, cores = 4, cpu = () => .1, cpuLoadKnown = true, complete = true, failMetricsAt = Infinity, emitFailure = false, env = hosted, wslHost = null, sharedBusyCores = 0 } = {}) {
  let time = 0, pid = 100;
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
    return { atMs: time, availableBytes, totalBytes: 16 * GiB, cpuQuota: cores, allowedIds: Array.from({ length: cores }, (_, i) => i), cpuLoadKnown, sharedBusyCores, busyCores: sharedBusyCores, wslHost: host, owned: groups.map(group => ({ id: group.id, pid: group.pid, rssBytes: 64 * 1024 ** 2, cpuCores: cpu(time, group), complete })), shared: [] };
  } };
  const scheduler = new ResourceScheduler({ env, monitor, clock, emit: event => { evidence.push(event); if (emitFailure && event.kind === 'complete') throw new Error('completion evidence write failed'); } });
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
  return { scheduler, execute, launches, evidence, running, now: () => time };
}

test('default admission is identical on desktop and hosted CI apart from reserved memory', () => {
  assert.equal(resourcePolicy({}).admissionMode, 'memory');
  assert.equal(resourcePolicy(memoryAdmission).admissionMode, 'memory');
  assert.equal(resourcePolicy({ ...hosted, ...memoryAdmission }).admissionMode, 'memory');
  assert.equal(resourcePolicy({ ...hosted, ...memoryAdmission, RUNNER_ENVIRONMENT: 'self-hosted' }).admissionMode, 'memory');
  const { scope: localScope, scopeEvidence: localEvidence, minFreeBytes: localReserve, ...local } = resourcePolicy({});
  const { scope: ciScope, scopeEvidence: ciEvidence, minFreeBytes: ciReserve, ...ci } = resourcePolicy(hosted);
  assert.deepEqual(local, ci);
  assert.equal(localReserve, GiB);
  assert.equal(ciReserve, 0);
  assert.equal(resourcePolicy({ CI: 'true' }).minFreeBytes, GiB);
  assert.equal(resourcePolicy({ ...hosted, LINGUAFLOW_CI_MIN_FREE_MIB: '1024' }).minFreeBytes, GiB);
  assert.equal(resourcePolicy({ LINGUAFLOW_CI_MIN_FREE_MIB: '0' }).minFreeBytes, 0);
  assert.throws(() => resourcePolicy({ LINGUAFLOW_CI_MIN_FREE_MIB: '-1' }), /at least 0/);
});

test('default zero CI reserve still charges controller and active growth, and rejects exhausted or unknown physical memory', async () => {
  const ci = fixture({ env: hosted, memory: 1.5 * GiB, cores: 0, cpuLoadKnown: false });
  await ci.scheduler.initialize();
  const budget = await ci.scheduler.bulkBudget(8, 4);
  assert.equal(budget.effectiveP, 1);
  assert.equal(budget.forecast.memoryHeadroom, 1.25 * GiB);
  await ci.scheduler.run(jobs, ci.execute);
  assert.ok(ci.launches[1].atMs >= 120000, 'zero reserve does not spend active unknown growth or controller allowance');
  await ci.scheduler.close();
  for (const memory of [0, NaN, Infinity]) {
    const invalid = fixture({ env: hosted, memory, cores: 0, cpuLoadKnown: false });
    await assert.rejects(invalid.scheduler.initialize(), /Physical memory availability/);
    assert.equal(invalid.launches.length, 0);
  }
  const exhausted = fixture({ env: hosted, memory: time => time >= 5000 ? 0 : 1.5 * GiB, cores: 0, cpuLoadKnown: false });
  await exhausted.scheduler.initialize();
  await assert.rejects(exhausted.scheduler.run(jobs, exhausted.execute), /Physical memory availability/);
  assert.equal(exhausted.running.size, 0);
  assert.equal(exhausted.scheduler.active.size, 0);
  await exhausted.scheduler.close();
});

test('memory admission expands despite unknown or saturated CPU without binding affinity', async () => {
  for (const counters of [{ cores: 0, cpuLoadKnown: false, cpu: () => NaN, sharedBusyCores: NaN }, { cores: 4, cpu: () => 4, sharedBusyCores: 4 }]) {
    const f = fixture({ ...counters, memory: 8 * GiB, env: memoryAdmission });
    await f.scheduler.initialize();
    assert.equal(f.scheduler.pool.method, 'memory-admission-no-affinity');
    assert.equal(f.scheduler.policy.admissionMode, 'memory');
    await f.scheduler.run(jobs, f.execute, { maxGoProcs: 4 });
    assert.equal(f.launches.length, 3);
    assert.ok(f.launches.every(job => job.atMs < 120000), JSON.stringify(f.launches));
    assert.ok(f.launches.every(job => job.atMs === 0), 'fresh proven memory must not wait thirty seconds between jobs');
    assert.ok(f.launches.every(job => job.gomaxprocs === 4));
    assert.ok(f.evidence.filter(row => row.kind === 'launch').every(row => row.forecast.cpuAdmission === false));
    await f.scheduler.close();
  }
  const legacy = fixture({ memory: 8 * GiB, cpu: () => 4 });
  await legacy.scheduler.initialize();
  await legacy.scheduler.run(jobs, legacy.execute);
  assert.equal(legacy.launches[1].atMs, 0, 'hosted CI uses the same immediate memory admission');
  await legacy.scheduler.close();
});

test('memory package batches resolve p against remaining memory and reserve the complete Go worker tree', async () => {
  const f = fixture({ memory: 8 * GiB, cores: 0, cpuLoadKnown: false, env: memoryAdmission });
  await f.scheduler.initialize();
  const bulk = await f.scheduler.bulkBudget(12, 4);
  assert.equal(bulk.effectiveP, 6);
  assert.equal(bulk.gomaxprocs, 4);
  assert.equal(bulk.memoryReservationBytes, 6 * GiB);
  await f.scheduler.run([{ id: 'synthetic-r1' }, { id: 'packages-b001', bulkPackageParallelism: 12 }], f.execute);
  const launch = f.evidence.find(row => row.kind === 'launch' && row.id === 'packages-b001');
  assert.equal(launch.effectiveP, 5, 'active synthetic unknown growth must be subtracted');
  assert.equal(launch.memoryReservationBytes, 5 * GiB);
  assert.ok(launch.forecast.memoryHeadroom >= launch.memoryReservationBytes);
  await f.scheduler.close();
});

test('gomaxprocs scheduler propagates runtime default or explicit values independently of CPU quota', async () => {
  for (const value of [null, 16, 4]) {
    const f = fixture({ env: {}, memory: 8 * GiB, cores: 1 });
    await f.scheduler.initialize();
    const budget = await f.scheduler.bulkBudget(12, value);
    assert.equal(budget.gomaxprocs, value);
    await f.scheduler.run([{ id: 'packages-b001', bulkPackageParallelism: 3 }], f.execute, { maxGoProcs: value });
    assert.equal(f.launches[0].gomaxprocs, value);
    await f.scheduler.close();
  }
  const f = fixture({ env: {}, memory: 8 * GiB, cores: 0, cpuLoadKnown: false });
  await f.scheduler.initialize();
  assert.equal((await f.scheduler.bulkBudget(3)).gomaxprocs, null);
  await f.scheduler.run(jobs, f.execute);
  assert.ok(f.launches.every(launch => launch.gomaxprocs === null));
  await f.scheduler.close();
});

test('memory admission preserves unknown growth and incomplete RSS instead of spending it on more jobs', async () => {
  for (const options of [{ memory: 2.9 * GiB }, { complete: false }]) {
    const f = fixture({ ...options, cores: 0, cpuLoadKnown: false, env: memoryAdmission });
    await f.scheduler.initialize();
    await f.scheduler.run(jobs, f.execute);
    assert.ok(f.launches[1].atMs >= 120000, JSON.stringify(f.launches));
    assert.ok(f.evidence.some(row => row.kind === 'defer' && ['owned-RSS-unproved', 'projected-memory-admission-budget'].includes(row.reason)));
    await f.scheduler.close();
  }
});

test('memory admission crossing the physical floor cancels and joins only already started jobs', async () => {
  const f = fixture({ memory: time => time >= 5000 ? .9 * GiB : 8 * GiB, cores: 0, cpuLoadKnown: false, env: memoryAdmission });
  await f.scheduler.initialize();
  const pending = [...jobs, ...Array.from({ length: 5 }, (_, i) => ({ id: `extra-${i}` }))];
  await assert.rejects(f.scheduler.run(pending, f.execute), /physical memory crossed/);
  assert.ok(f.launches.length > 0 && f.launches.length < pending.length, 'physical floor must leave unsupported pending jobs unlaunched');
  assert.equal(f.running.size, 0);
  assert.equal(f.scheduler.active.size, 0);
  await f.scheduler.close();
});

test('memory Windows monitoring accepts missing affinity diagnostics but still rejects unknown physical memory', async () => {
  const windows = async () => ({ availableBytes: 8 * GiB, totalBytes: 16 * GiB, mask: null, processes: [] });
  await assert.rejects(createResourceMonitor({ platform: 'win32', windows }).sample(), /affinity unavailable/);
  const sample = await createResourceMonitor({ platform: 'win32', windows, cpuDiagnosticsOnly: true }).sample();
  assert.equal(sample.availableBytes, 8 * GiB);
  assert.equal(sample.cpuQuota, null);
  assert.equal(sample.cpuLoadKnown, false);
  await assert.rejects(createResourceMonitor({ platform: 'win32', windows: async () => ({ ...await windows(), availableBytes: NaN }), cpuDiagnosticsOnly: true }).sample(), /Physical memory/);
  const readFile = async file => ({ '/proc/meminfo': 'MemAvailable: 8388608 kB\nMemTotal: 16777216 kB\n', '/proc/self/status': 'Cpus_allowed_list: 0-3\n' })[file] ?? '';
  const windowsHost = async () => ({ availableBytes: 8 * GiB, hostCpuCores: null, hostBusyCores: null });
  const guest = { platform: 'linux', wsl: true, readFile, processes: async () => [], windows: windowsHost };
  await assert.rejects(createResourceMonitor(guest).sample(), /WSL Windows host physical\/CPU budget unavailable/);
  const memoryGuest = await createResourceMonitor({ ...guest, cpuDiagnosticsOnly: true }).sample();
  assert.equal(memoryGuest.availableBytes, 8 * GiB);
  assert.equal(memoryGuest.wslHost.cpuCores, null);
  for (const availableBytes of [NaN, Infinity]) await assert.rejects(createResourceMonitor({ ...guest, windows: async () => ({ ...await windowsHost(), availableBytes }), cpuDiagnosticsOnly: true }).sample(), /WSL Windows host physical/);
});

test('memory Linux monitoring isolates CPU list and cgroup errors while physical and cgroup memory remain strict', async () => {
  const files = new Map([
    ['/proc/meminfo', 'MemAvailable: 8388608 kB\nMemTotal: 16777216 kB\n'],
    ['/proc/self/status', 'Cpus_allowed_list: invalid\n'],
    ['/proc/self/cgroup', '0::/task\n'],
    ['/proc/self/mountinfo', '27 23 0:25 / /sys/fs/cgroup rw - cgroup2 cgroup rw\n'],
    ['/sys/fs/cgroup/task/cpu.max', '100000 0'],
    ['/sys/fs/cgroup/task/memory.max', String(4 * GiB)],
    ['/sys/fs/cgroup/task/memory.current', String(GiB)],
  ]);
  const options = { platform: 'linux', wsl: false, readFile: async file => files.get(file) ?? null, processes: async () => [] };
  await assert.rejects(createResourceMonitor(options).sample(), /CPU/);
  let sample = await createResourceMonitor({ ...options, cpuDiagnosticsOnly: true }).sample();
  assert.equal(sample.availableBytes, 3 * GiB);
  assert.equal(sample.cpuQuota, null);
  assert.equal(sample.cpuLoadKnown, false);
  files.set('/proc/self/status', 'Cpus_allowed_list: 0-3\n');
  await assert.rejects(createResourceMonitor(options).sample(), /CPU period/);
  sample = await createResourceMonitor({ ...options, cpuDiagnosticsOnly: true }).sample();
  assert.match(sample.cpuError, /CPU period/);
  assert.equal(sample.availableBytes, 3 * GiB);
  files.delete('/proc/self/status');
  assert.equal((await createResourceMonitor({ ...options, cpuDiagnosticsOnly: true }).sample()).availableBytes, 3 * GiB);
  files.set('/sys/fs/cgroup/task/memory.current', 'corrupt');
  await assert.rejects(createResourceMonitor({ ...options, cpuDiagnosticsOnly: true }).sample(), /BigInt/);
  files.set('/sys/fs/cgroup/task/memory.current', String(GiB));
  files.delete('/proc/meminfo');
  await assert.rejects(createResourceMonitor({ ...options, cpuDiagnosticsOnly: true }).sample(), /Physical memory/);
});

test('memory PowerShell snapshot keeps physical facts after CPU query failure and still rejects physical query failure', async t => {
  if (process.platform !== 'win32') return t.skip('Windows PowerShell snapshot contract');
  const mock = `function Get-CimInstance {
    param([string]$ClassName,[string[]]$Property,[string]$Filter)
    switch($ClassName){
      'Win32_OperatingSystem' { if($env:MEMORY_FIXTURE_BAD_PHYSICAL -eq '1'){throw 'fixture physical query failed'}; [pscustomobject]@{FreePhysicalMemory=8388608;TotalVisibleMemorySize=16777216} }
      'Win32_Process' { @() }
      default { throw 'fixture CPU query failed' }
    }
  }\n`;
  const run = env => exec('pwsh.exe', ['-NoProfile', '-NonInteractive', '-Command', mock + windowsSnapshotScript], { windowsHide: true, timeout: 4000, env: { ...process.env, LINGUAFLOW_RESOURCE_OWNER: '', ...env } });
  await assert.rejects(run({ LINGUAFLOW_RESOURCE_CPU_DIAGNOSTICS_ONLY: '' }), /fixture CPU query failed/);
  const result = JSON.parse((await run({ LINGUAFLOW_RESOURCE_CPU_DIAGNOSTICS_ONLY: '1' })).stdout);
  assert.equal(result.availableBytes, 8 * GiB);
  assert.equal(result.hostCpuCores, null);
  assert.equal(result.hostBusyCores, null);
  assert.match(result.cpuError, /fixture CPU query failed/);
  await assert.rejects(run({ LINGUAFLOW_RESOURCE_CPU_DIAGNOSTICS_ONLY: '1', MEMORY_FIXTURE_BAD_PHYSICAL: '1' }), /fixture physical query failed/);
});

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







test('physical headroom or unproved RSS prevents expansion; completion releases resources without another observation delay', async () => {
  for (const options of [{ memory: 2.6 * GiB }, { complete: false }]) {
    const state = fixture({ ...options, env: {} });
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
  const pending = fixture({ memory: time => time < 10000 ? 1.5 * GiB : time < 50000 ? .75 * GiB : 12 * GiB });
  await pending.scheduler.initialize();
  const pendingResults = await pending.scheduler.run(jobs.map(job => ({ ...job, duration: 40000 })), pending.execute);
  assert.ok(pendingResults.every(result => result.exitCode === 0));
  assert.equal(pending.launches[1].atMs, 50000, 'new work waits for headroom while the first shard finishes normally');
  assert.ok(pending.evidence.some(event => event.kind === 'projected-memory-defer' && event.pending === 2));
  await pending.scheduler.close();
});

test('actual physical guard cancels and joins only owned tasks even with no pending shards', async () => {
  const state = fixture({ memory: time => time >= 70000 ? .75 * GiB : 12 * GiB, env: {} });
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
  assert.equal(metric.launches.length, 3);
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
