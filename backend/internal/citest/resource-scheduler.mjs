import { createResourceMonitor } from './resources.mjs';

const total = values => values.reduce((a, b) => a + b, 0);
const defaultClock = { now: Date.now, sleep: (ms, signal) => new Promise(resolve => {
  let timer;
  const done = () => { clearTimeout(timer); signal?.removeEventListener('abort', done); resolve(); };
  timer = setTimeout(done, ms);
  signal?.addEventListener('abort', done, { once: true });
  if (signal?.aborted) done();
}) };

export function resourcePolicy(env = {}) {
  const hosted = env.GITHUB_ACTIONS === 'true' && env.RUNNER_ENVIRONMENT === 'github-hosted' && !env.WSL_DISTRO_NAME;
  const number = (name, fallback, minimum) => {
    const value = env[name] == null || env[name] === '' ? fallback : Number(env[name]);
    if (!Number.isFinite(value) || value < minimum) throw new Error(`${name} must be at least ${minimum}`);
    return value;
  };
  return {
    scope: hosted ? 'github-hosted-job' : 'shared-host',
    scopeEvidence: hosted ? 'GITHUB_ACTIONS=true and RUNNER_ENVIRONMENT=github-hosted' : 'desktop/self-hosted/unknown host',
    admissionMode: 'memory',
    sampleMs: number('LINGUAFLOW_CI_SAMPLE_MS', 5000, 100),
    metricFailureMs: 30000,
    minFreeBytes: number('LINGUAFLOW_CI_MIN_FREE_MIB', hosted ? 0 : 1024, 0) * 1024 ** 2,
    unknownPeakBytes: number('LINGUAFLOW_CI_PROCESS_PEAK_MIB', 1024, 1) * 1024 ** 2,
    controllerGrowthBytes: 256 * 1024 ** 2,
    growthFactor: number('LINGUAFLOW_CI_GROWTH_FACTOR', 1.5, 1),
    calibration: 'Unknown process reservation is a configurable admission allowance, not an observed peak. Sampled RSS growth forecasts are heuristics, not hard memory limits; CPU measurements are diagnostics and initial batch hints.',
  };
}

export class ResourceScheduler {
  constructor({ env = {}, monitor, clock = defaultClock, emit = () => {}, signal, policy = {} } = {}) {
    this.policy = { ...resourcePolicy(env), ...policy };
    this.monitor = monitor ?? createResourceMonitor({ cpuDiagnosticsOnly: true }); this.clock = clock; this.emit = emit;
    this.signal = signal; this.active = new Map(); this.trusted = new Map();
    this.peaks = new Map(); this.pool = null; this.failureSince = null;
  }

  async sample() {
    try {
      const snapshot = await this.monitor.sample([...this.active.values()]);
      if (!Number.isFinite(snapshot.availableBytes) || snapshot.availableBytes <= 0) throw Object.assign(new Error('Physical memory availability is unknown or exhausted'), { physicalMemoryInvalid: true });
      if (!Array.isArray(snapshot.owned) || !Array.isArray(snapshot.shared) || snapshot.owned.some(item => !Number.isFinite(item.rssBytes) || item.rssBytes < 0) || snapshot.shared.some(item => !Number.isFinite(item.rssBytes) || item.rssBytes < 0 || !Number.isFinite(item.peakRSSBytes))) throw new Error('Owned/shared memory counters invalid');
      this.failureSince = null;
      for (const owned of snapshot.owned) {
        const old = this.peaks.get(owned.id) ?? { rssBytes: 0, cpuCores: 0 };
        this.peaks.set(owned.id, { rssBytes: Math.max(old.rssBytes, owned.complete ? owned.rssBytes : 0), cpuCores: Number.isFinite(owned.cpuCores) ? Math.max(old.cpuCores, owned.cpuCores) : old.cpuCores, observedFrom: old.observedFrom ?? (owned.complete ? snapshot.atMs : undefined), observedUntil: owned.complete ? snapshot.atMs : old.observedUntil });
      }
      this.emit({ kind: 'sample', ...snapshot, owner: snapshot.owner ? { pid: snapshot.owner.pid, birth: snapshot.owner.birth, rssBytes: snapshot.owner.rssBytes } : null });
      return snapshot;
    } catch (error) {
      this.failureSince ??= this.clock.now();
      this.emit({ kind: 'sample-error', atMs: this.clock.now(), message: error.message });
      if (error.physicalMemoryInvalid) throw error;
      if (this.clock.now() - this.failureSince >= this.policy.metricFailureMs) throw new Error(`Resource metrics unavailable for ${this.policy.metricFailureMs}ms; no unproved task launch: ${error.message}`);
      return null;
    }
  }

  async initialize() {
    let snapshot;
    while (true) {
      if (this.signal?.aborted) throw new Error('CI resource admission cancelled');
      snapshot = await this.sample();
      if (snapshot) {
        if (snapshot.wslHost) {
          this.policy.scope = 'shared-host'; this.policy.scopeEvidence = 'WSL guest and Windows host budget';
        }
        if (snapshot.availableBytes >= this.policy.minFreeBytes + this.policy.controllerGrowthBytes + this.policy.unknownPeakBytes) break;
        this.emit({ kind: 'initial-defer', atMs: this.clock.now(), reason: 'physical-memory-budget' });
      }
      await this.clock.sleep(snapshot ? this.policy.sampleMs : Math.min(250, this.policy.sampleMs));
    }
    this.pool = { ids: snapshot.allowedIds ?? [], cores: snapshot.cpuQuota ?? null, method: 'memory-admission-no-affinity', hardBound: false };
    this.emit({ kind: 'cpu-pool', atMs: this.clock.now(), policy: this.policy, pool: this.pool, cpuAdmission: false });
    return this.pool;
  }

  forecast(snapshot) {
    const policy = this.policy;
    const activeMemoryGrowth = total(snapshot.owned.map(owned => {
      const running = this.active.get(owned.id);
      if (!running) return 0;
      const peak = this.peaks.get(owned.id)?.rssBytes ?? 0;
      const prediction = Math.max(running.memoryReservationBytes, peak * policy.growthFactor);
      return Math.max(0, prediction - owned.rssBytes);
    }));
    const unobserved = [...this.active.values()].filter(run => !snapshot.owned.some(owned => owned.id === run.id));
    const sharedMemoryGrowth = total(snapshot.shared.map(item => Math.max(0, Math.max(policy.unknownPeakBytes, item.peakRSSBytes * policy.growthFactor) - item.rssBytes)));
    const memoryHeadroom = snapshot.availableBytes - policy.minFreeBytes - policy.controllerGrowthBytes - activeMemoryGrowth - sharedMemoryGrowth - total(unobserved.map(run => run.memoryReservationBytes));
    return { memoryHeadroom, activeMemoryGrowth, sharedMemoryGrowth, cpuAdmission: false };
  }

  async bulkBudget(requestedP, maxGoProcs = null) {
    while (true) {
      if (this.signal?.aborted) throw new Error('CI bulk admission cancelled');
      const snapshot = await this.sample();
      if (snapshot) {
        const forecast = this.forecast(snapshot);
        const p = Math.min(requestedP, Math.floor(forecast.memoryHeadroom / this.policy.unknownPeakBytes));
        if (p >= 1) {
          const gomaxprocs = maxGoProcs;
          const budget = { effectiveP: p, requestedP, gomaxprocs, memoryReservationBytes: p * this.policy.unknownPeakBytes, forecast, pool: this.pool };
          this.emit({ kind: 'bulk-admission', atMs: this.clock.now(), ...budget });
          return budget;
        }
      }
      await this.clock.sleep(this.policy.sampleMs);
    }
  }

  async executeOne(job, budget, run, beforeLaunch = async () => {}) {
    const results = await this.run([job], run, { beforeLaunch, fixedBudget: budget });
    return results[0];
  }

  async run(jobs, run, { beforeLaunch = async () => {}, fixedBudget = null, maxGoProcs = null } = {}) {
    if (jobs.some(job => job.bulkPackageParallelism != null && (!Number.isSafeInteger(job.bulkPackageParallelism) || job.bulkPackageParallelism < 1))) throw new Error('Bulk package parallelism must be a positive safe integer');
    const pending = [...jobs].sort((a, b) => {
      const rank = job => job.id.startsWith('synthetic-') ? 0 : job.id === 'worker-remainder' ? 1 : 2;
      return rank(a) - rank(b);
    });
    const completed = new Map(), promises = new Set();
    const abort = new AbortController();
    const onAbort = () => abort.abort();
    this.signal?.addEventListener('abort', onAbort, { once: true });
    let fatal;
    try {
      while (pending.length || this.active.size) {
        if (this.signal?.aborted || abort.signal.aborted) throw fatal ?? new Error('CI resource execution cancelled');
        const snapshot = await this.sample();
        if (fatal || this.signal?.aborted || abort.signal.aborted) throw fatal ?? new Error('CI resource execution cancelled');
        const inflightForecast = snapshot && this.active.size ? this.forecast(snapshot) : null;
        if (snapshot && snapshot.availableBytes < this.policy.minFreeBytes && inflightForecast) {
          this.emit({ kind: 'physical-guard-stop', reason: 'actual-physical-floor', atMs: this.clock.now(), availableBytes: snapshot.availableBytes, forecast: inflightForecast, owned: snapshot.owned });
          throw new Error(`Actual available physical memory crossed the ${this.policy.minFreeBytes} byte reserve; stopping only this runner owned tasks without retries`);
        }
        if (inflightForecast?.memoryHeadroom < 0) {
          this.emit({ kind: 'projected-memory-defer', atMs: this.clock.now(), availableBytes: snapshot.availableBytes, forecast: inflightForecast, running: this.active.size, pending: pending.length });
        }
        let launched = false;
        if (snapshot && pending.length) {
          const job = pending[0], trusted = this.trusted.get(job.trustKey ?? job.id);
          const forecast = this.forecast(snapshot);
          const bulk = job.bulkPackageParallelism != null;
          const effectiveP = bulk ? Math.min(job.bulkPackageParallelism, Math.floor(forecast.memoryHeadroom / this.policy.unknownPeakBytes)) : fixedBudget?.effectiveP;
          const memoryReservationBytes = fixedBudget?.memoryReservationBytes ?? (bulk ? Math.max(1, effectiveP) * this.policy.unknownPeakBytes : trusted ? trusted.rssBytes * this.policy.growthFactor : this.policy.unknownPeakBytes);
          const knownRSS = [...this.active.keys()].every(id => snapshot.owned.some(item => item.id === id && item.complete));
          const gomaxprocs = fixedBudget?.gomaxprocs ?? maxGoProcs;
          const resourcesReady = knownRSS && (!bulk || effectiveP >= 1) && forecast.memoryHeadroom >= memoryReservationBytes;
          if (resourcesReady) {
            await beforeLaunch(job);
            if (fatal || this.signal?.aborted || abort.signal.aborted) throw fatal ?? new Error('CI cancelled before shard launch');
            pending.shift(); launched = true;
            const running = { id: job.id, pid: null, startedAt: this.clock.now(), gomaxprocs, memoryReservationBytes };
            this.emit({ kind: 'launch', atMs: this.clock.now(), id: job.id, gomaxprocs, effectiveP, memoryReservationBytes, forecast, reusedPeak: Boolean(trusted), pool: this.pool });
            this.active.set(job.id, running);
            const promise = Promise.resolve().then(() => {
              if (abort.signal.aborted || this.signal?.aborted) throw new Error('CI cancelled before owned process spawn');
              return run(job, { ...fixedBudget, ...(effectiveP != null ? { effectiveP } : {}), gomaxprocs, memoryReservationBytes, pool: this.pool, signal: abort.signal, onSpawn: process => { running.pid = process.pid; } });
            }).then(result => {
              completed.set(job.id, result);
              const peak = this.peaks.get(job.id);
              if (peak?.rssBytes > 0 && peak.observedUntil - peak.observedFrom >= this.policy.sampleMs && (result.exitCode === 0 || result.code === 0) && !result.validation?.issues?.length) this.trusted.set(job.trustKey ?? job.id, { ...peak, gomaxprocs });
              this.emit({ kind: 'complete', atMs: this.clock.now(), id: job.id, peak: peak ?? null });
            }).catch(error => { fatal ??= error; abort.abort(); }).finally(() => { this.active.delete(job.id); promises.delete(promise); });
            promises.add(promise);
          } else this.emit({ kind: 'defer', atMs: this.clock.now(), id: job.id, reason: !knownRSS ? 'owned-RSS-unproved' : 'projected-memory-admission-budget', forecast });
        }
        if (!launched && (pending.length || this.active.size)) {
          const pause = new AbortController();
          try { await Promise.race([...promises, this.clock.sleep(this.policy.sampleMs, pause.signal)]); }
          finally { pause.abort(); }
        }
      }
      if (fatal) throw fatal;
      return jobs.map(job => completed.get(job.id));
    } catch (error) {
      abort.abort();
      await Promise.allSettled([...promises]);
      throw error;
    } finally { this.signal?.removeEventListener('abort', onAbort); }
  }

  async close() {
    if (this.closed) return;
    if (this.active.size) throw new Error('Cannot close resource scheduler with active owned tasks');
    this.closed = true;
    this.emit({ kind: 'scheduler-closed', atMs: this.clock.now(), method: this.pool?.method ?? null });
  }
}
