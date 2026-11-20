import { bindControllerCPU, createResourceMonitor } from './resources.mjs';

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
    cpuReserve: env.LINGUAFLOW_CI_CPU_RESERVE == null || env.LINGUAFLOW_CI_CPU_RESERVE === '' ? null : number('LINGUAFLOW_CI_CPU_RESERVE', 0, 0),
    sampleMs: number('LINGUAFLOW_CI_SAMPLE_MS', 5000, 100),
    stableMs: number('LINGUAFLOW_CI_STABLE_MS', 30000, 0),
    metricFailureMs: 30000,
    minFreeBytes: number('LINGUAFLOW_CI_MIN_FREE_MIB', 1024, 1024) * 1024 ** 2,
    unknownPeakBytes: number('LINGUAFLOW_CI_PROCESS_PEAK_MIB', 1024, 1) * 1024 ** 2,
    controllerGrowthBytes: 256 * 1024 ** 2,
    growthFactor: number('LINGUAFLOW_CI_GROWTH_FACTOR', 1.5, 1),
    cpuGrowthCores: .25,
    calibration: 'Unknown process reservation is a configurable admission allowance, not an observed peak. Sampled RSS/CPU growth forecasts are heuristics, not hard memory limits.',
  };
}

export class ResourceScheduler {
  constructor({ env = {}, monitor = createResourceMonitor(), bind = bindControllerCPU, clock = defaultClock, emit = () => {}, signal, policy = {} } = {}) {
    this.monitor = monitor; this.bind = bind; this.clock = clock; this.emit = emit;
    this.policy = { ...resourcePolicy(env), ...policy };
    this.signal = signal; this.active = new Map(); this.trusted = new Map(); this.history = [];
    this.peaks = new Map(); this.pool = null; this.failureSince = null; this.stableSince = null;
    this.readySince = new Map();
  }

  async sample() {
    try {
      const snapshot = await this.monitor.sample([...this.active.values()]);
      if (!Number.isFinite(snapshot.availableBytes) || snapshot.availableBytes < 0 || !Number.isFinite(snapshot.cpuQuota) || snapshot.cpuQuota < 0 || !Number.isFinite(snapshot.sharedBusyCores) || !Array.isArray(snapshot.owned) || !Array.isArray(snapshot.shared)) throw new Error('Resource sample does not prove physical/CPU availability');
      if (snapshot.owned.some(item => !Number.isFinite(item.rssBytes) || item.rssBytes < 0 || !Number.isFinite(item.cpuCores) || item.cpuCores < 0) || snapshot.shared.some(item => !Number.isFinite(item.rssBytes) || item.rssBytes < 0 || !Number.isFinite(item.peakRSSBytes))) throw new Error('Owned/shared resource counters invalid');
      this.failureSince = null;
      for (const owned of snapshot.owned) {
        const old = this.peaks.get(owned.id) ?? { rssBytes: 0, cpuCores: 0 };
        this.peaks.set(owned.id, { rssBytes: Math.max(old.rssBytes, owned.complete ? owned.rssBytes : 0), cpuCores: Math.max(old.cpuCores, owned.cpuCores), observedFrom: old.observedFrom ?? (owned.complete ? snapshot.atMs : undefined), observedUntil: owned.complete ? snapshot.atMs : old.observedUntil });
      }
      const complete = snapshot.cpuLoadKnown && [...this.active.keys()].every(id => snapshot.owned.some(owned => owned.id === id && owned.complete));
      if (!complete) this.stableSince = null;
      else this.stableSince ??= this.clock.now();
      this.history.push(snapshot);
      this.history = this.history.filter(item => item.atMs >= snapshot.atMs - this.policy.stableMs);
      this.emit({ kind: 'sample', ...snapshot, owner: snapshot.owner ? { pid: snapshot.owner.pid, birth: snapshot.owner.birth, rssBytes: snapshot.owner.rssBytes } : null });
      return snapshot;
    } catch (error) {
      this.stableSince = null;
      this.failureSince ??= this.clock.now();
      this.emit({ kind: 'sample-error', atMs: this.clock.now(), message: error.message });
      if (this.clock.now() - this.failureSince >= this.policy.metricFailureMs) throw new Error(`Resource metrics unavailable for ${this.policy.metricFailureMs}ms; no unproved task launch: ${error.message}`);
      return null;
    }
  }

  async initialize() {
    let snapshot, sharedAtStart;
    while (true) {
      if (this.signal?.aborted) throw new Error('CI resource admission cancelled');
      snapshot = await this.sample();
      if (snapshot?.cpuLoadKnown) {
        if (snapshot.wslHost) {
          this.policy.scope = 'shared-host'; this.policy.scopeEvidence = 'WSL guest and Windows host budget';
        }
        const defaultReserve = this.policy.scope === 'github-hosted-job' ? 0 : snapshot.cpuQuota >= 8 ? 2 : 1;
        this.policy.cpuReserve ??= defaultReserve;
        sharedAtStart = this.policy.scope === 'shared-host' && this.policy.cpuReserve !== 0 ? Math.floor(snapshot.sharedBusyCores) : 0;
        if (snapshot.cpuQuota - this.policy.cpuReserve - sharedAtStart >= 1 && snapshot.availableBytes >= this.policy.minFreeBytes + this.policy.controllerGrowthBytes + this.policy.unknownPeakBytes) break;
        this.emit({ kind: 'initial-defer', atMs: this.clock.now(), reason: 'physical-memory-or-shared-CPU-budget' });
      }
      await this.clock.sleep(snapshot?.cpuLoadKnown ? this.policy.sampleMs : Math.min(250, this.policy.sampleMs));
    }
    try {
      this.binding = await this.bind(this.monitor, snapshot, { reserve: this.policy.cpuReserve + sharedAtStart });
      this.pool = { ids: this.binding.ids, cores: this.binding.cores, method: this.binding.method, hardBound: true };
    } catch (error) {
      if (error.restoreFailure) throw new Error(`Failed CPU bind also failed to restore original affinity: ${error.restoreFailure}`);
      const cores = Math.floor(snapshot.cpuQuota - this.policy.cpuReserve - sharedAtStart);
      if (cores < 1) throw new Error(`No provable CPU budget after reserve: ${error.message}`);
      this.pool = { ids: [], cores, method: 'serial-fallback', hardBound: false, diagnostic: error.message };
    }
    this.emit({ kind: 'cpu-pool', atMs: this.clock.now(), policy: this.policy, pool: this.pool, initialSharedBusyCores: sharedAtStart });
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
    const activeCPU = total([...this.active.values()].map(run => {
      const recentPeak = Math.max(0, ...this.history.flatMap(item => item.owned.filter(owned => owned.id === run.id).map(owned => owned.cpuCores)));
      return Math.min(run.gomaxprocs, Math.max(1, recentPeak * policy.growthFactor + policy.cpuGrowthCores));
    }));
    const sharedCPU = Math.max(snapshot.sharedBusyCores, ...this.history.map(item => item.sharedBusyCores));
    const hostCPU = snapshot.wslHost ? Math.min(...this.history.filter(item => item.wslHost).map(item => item.wslHost.cpuCores - Math.max(0, item.wslHost.busyCores - total(item.owned.map(owned => owned.cpuCores))) - this.policy.cpuReserve)) : Infinity;
    return { memoryHeadroom, availableCPU: Math.min(Math.min(this.pool.cores, snapshot.cpuQuota) - sharedCPU, hostCPU) - activeCPU, activeMemoryGrowth, sharedMemoryGrowth, activeCPU, sharedCPU, hostAvailableCPU: Number.isFinite(hostCPU) ? hostCPU : null };
  }

  async bulkBudget(requestedP, maxGoProcs) {
    while (true) {
      if (this.signal?.aborted) throw new Error('CI bulk admission cancelled');
      const snapshot = await this.sample();
      if (snapshot?.cpuLoadKnown) {
        const forecast = this.forecast(snapshot);
        let p = Math.min(requestedP, Math.floor(forecast.memoryHeadroom / this.policy.unknownPeakBytes));
        if (!this.pool.hardBound) p = Math.min(p, 1);
        if (p >= 1 && forecast.availableCPU > 0) {
          const gomaxprocs = this.pool.hardBound ? Math.max(1, Math.min(maxGoProcs, Math.floor(this.pool.cores / p))) : 1;
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

  async run(jobs, run, { beforeLaunch = async () => {}, fixedBudget = null, maxGoProcs = 4 } = {}) {
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
        if (snapshot && snapshot.availableBytes < this.policy.minFreeBytes) {
          if (this.policy.scope === 'github-hosted-job') {
            this.emit({ kind: 'physical-low-memory', action: 'defer-new', atMs: this.clock.now(), availableBytes: snapshot.availableBytes, running: this.active.size, pending: pending.length });
          } else if (inflightForecast) {
            this.emit({ kind: 'physical-guard-stop', reason: 'actual-physical-floor', atMs: this.clock.now(), availableBytes: snapshot.availableBytes, forecast: inflightForecast, owned: snapshot.owned });
            throw new Error('Actual available physical memory crossed the 1GiB guard; stopping only this runner owned tasks without retries');
          }
        }
        if (inflightForecast?.memoryHeadroom < 0) {
          this.emit({ kind: 'projected-memory-defer', atMs: this.clock.now(), availableBytes: snapshot.availableBytes, forecast: inflightForecast, running: this.active.size, pending: pending.length });
        }
        let launched = false;
        if (snapshot?.cpuLoadKnown && pending.length) {
          const job = pending[0], trusted = this.trusted.get(job.trustKey ?? job.id);
          const forecast = this.forecast(snapshot);
          const memoryReservationBytes = fixedBudget?.memoryReservationBytes ?? (trusted ? trusted.rssBytes * this.policy.growthFactor : this.policy.unknownPeakBytes);
          const knownRSS = [...this.active.keys()].every(id => snapshot.owned.some(item => item.id === id && item.complete));
          const gomaxprocs = fixedBudget?.gomaxprocs ?? (!this.pool.hardBound ? 1 : !this.active.size ? Math.min(maxGoProcs, this.pool.cores) : Math.max(1, Math.min(maxGoProcs, Math.floor(forecast.availableCPU / pending.length))));
          const resourcesReady = knownRSS && forecast.memoryHeadroom >= memoryReservationBytes && forecast.availableCPU > (this.active.size ? 1 - Number.EPSILON : 0);
          if (!resourcesReady) this.readySince.delete(job.id);
          else if (!this.readySince.has(job.id)) this.readySince.set(job.id, this.clock.now());
          const stable = trusted || !this.active.size || (this.stableSince != null && this.clock.now() - this.stableSince >= this.policy.stableMs && this.clock.now() - this.readySince.get(job.id) >= this.policy.stableMs);
          if (knownRSS && stable && forecast.memoryHeadroom >= memoryReservationBytes && forecast.availableCPU > (this.active.size ? 1 - Number.EPSILON : 0) && (this.pool.hardBound || !this.active.size)) {
            await beforeLaunch(job);
            if (fatal || this.signal?.aborted || abort.signal.aborted) throw fatal ?? new Error('CI cancelled before shard launch');
            pending.shift(); launched = true;
            const running = { id: job.id, pid: null, startedAt: this.clock.now(), gomaxprocs, memoryReservationBytes };
            this.emit({ kind: 'launch', atMs: this.clock.now(), id: job.id, gomaxprocs, memoryReservationBytes, forecast, reusedPeak: Boolean(trusted), pool: this.pool });
            this.active.set(job.id, running);
            const promise = Promise.resolve().then(() => {
              if (abort.signal.aborted || this.signal?.aborted) throw new Error('CI cancelled before owned process spawn');
              return run(job, { ...fixedBudget, gomaxprocs, memoryReservationBytes, pool: this.pool, signal: abort.signal, onSpawn: process => { running.pid = process.pid; } });
            }).then(result => {
              completed.set(job.id, result);
              const peak = this.peaks.get(job.id);
              if (peak?.rssBytes > 0 && peak.observedUntil - peak.observedFrom >= this.policy.sampleMs && (result.exitCode === 0 || result.code === 0) && !result.validation?.issues?.length) this.trusted.set(job.trustKey ?? job.id, { ...peak, gomaxprocs });
              this.emit({ kind: 'complete', atMs: this.clock.now(), id: job.id, peak: peak ?? null });
            }).catch(error => { fatal ??= error; abort.abort(); }).finally(() => { this.active.delete(job.id); promises.delete(promise); });
            promises.add(promise);
          } else this.emit({ kind: 'defer', atMs: this.clock.now(), id: job.id, reason: !knownRSS ? 'owned-RSS-unproved' : forecast.memoryHeadroom < memoryReservationBytes ? 'projected-memory-admission-budget' : forecast.availableCPU < 1 ? 'CPU-budget' : !stable ? 'observation-window' : 'affinity-fallback-serialization', forecast });
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
    if (this.active.size) throw new Error('Cannot restore CPU affinity with active owned tasks');
    await this.binding?.restore();
    this.closed = true;
    this.emit({ kind: 'cpu-pool-restored', atMs: this.clock.now(), method: this.pool?.method ?? null });
  }
}
