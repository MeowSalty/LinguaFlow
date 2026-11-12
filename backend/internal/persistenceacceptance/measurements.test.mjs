import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initializeSamples, validateMeasurements, validateCapacityMeasurements } from './measurements.mjs';

function timings(operations, phases) {
  const result = { pending_owner_peak: 10, coordinator_intent_peak: 10, normal_queue_peak: 16, reliable_queue_peak: 32, completion_peak: 10 };
  for (const operation of operations) for (const phase of phases) result[operation + '.' + phase] = { count: 1, p50_ms: 0, p95_ms: 0 };
  return result;
}

function measured(sample) {
  return { sql: 0, begins: 0, commits: 0, rollbacks: 0, elapsed_ms: 0, sql_p50_ms: 0, sql_p95_ms: 0, txn_p50_ms: 0, txn_p95_ms: 0, ...sample };
}

function fullSamples() {
  const extract = delay => ({
    name: 'load7x10x300', driver: 'sqlite3',
    extra: {
      delay_ms: delay, resources: 7, batches: 70,
      segments: 21000, completed: 21000, total: 21000,
      provider_calls: 70, provider_peak: 10, usage_records: 70,
      resource_completed: Array(7).fill(3000), failures: null,
      timings: timings(['reserve', 'sent', 'received', 'completed', 'checkpoint', 'stage_initialize'], ['save_queue', 'query', 'exec']),
    },
  });
  const translate = delay => ({
    name: 'translation7x2x300', driver: 'sqlite3',
    extra: {
      delay_ms: delay, resources: 7, source_batches: 14,
      source_batch_size: 300, default_candidate_segments: 256,
      segments: 4200, completed: 4200, total: 4200,
      body_updated: 4200, facts: 4200, candidate_records: 4200,
      provider_calls: 42, provider_peak: 10,
      request_records: 45, completed_request_records: 42,
      aborted_request_records: 3, invalid_aborted_requests: 0,
      actual_llm_batches: 42, usage_records: 42,
      resource_completed: Array(7).fill(600), failures: [],
      invalid_requests: 0, duplicate_or_missing_members: 0,
      request_size_distribution: { 100: 42 }, model_size_distribution: { 100: 42 },
      commit_correlations: { events: 4200, missing: 0, request_count: 42 },
      timings: timings(['reserve', 'sent', 'response_handoff', 'member_promotion', 'commit', 'stage_initialize'], ['save_queue', 'transaction', 'query', 'exec']),
    },
  });
  return [0, 1, 2, 5].map(extract).concat([0, 5].map(translate)).map(measured);
}

const options = { driver: 'sqlite3', protocol: false, bench: true };

test('complete extraction and split translation matrices pass', () => {
  assert.doesNotThrow(() => validateMeasurements(fullSamples(), options));
});

test('missing/duplicate delay and wrong driver cannot satisfy acceptance', () => {
  const samples = fullSamples();
  assert.throws(() => validateMeasurements(samples.slice(1), options), /exactly one/);
  assert.throws(() => validateMeasurements([...samples, samples[0]], options), /exactly one/);
  samples[0].driver = 'postgres';
  assert.throws(() => validateMeasurements(samples, options), /driver/);
});

test('partial progress, excess queue and mismatched member/call facts fail', () => {
  for (const change of [
    samples => { samples[0].extra.completed--; },
    samples => { samples[0].extra.timings.reliable_queue_peak++; },
    samples => { samples[4].extra.usage_records--; },
    samples => { samples[4].extra.body_updated--; },
    samples => { samples[4].extra.invalid_aborted_requests++; },
    samples => { samples[4].extra.commit_correlations.missing++; },
    samples => { samples[4].extra.model_size_distribution = { 100: 41 }; },
    samples => { samples[4].extra.request_size_distribution = samples[4].extra.model_size_distribution = { 99: 42 }; },
    samples => { delete samples[4].extra.timings['commit.transaction']; },
  ]) {
    const samples = fullSamples();
    change(samples);
    assert.throws(() => validateMeasurements(samples, options), /Invalid persistence measurement/);
  }
});

test('protocol acceptance requires both representative production paths', () => {
  const samples = [0, 1, 2, 5].map(delay => ({ name: 'reserve300', driver: 'sqlite3', extra: { delay_ms: delay, pool: 0 } }));
  for (const name of ['pool-advance-reserve300', 'seal5000', 'checkpoint300', 'calibrate', 'commit-observations']) samples.push({ name, driver: 'sqlite3', extra: {} });
  samples.find(sample => sample.name === 'commit-observations').extra.timings = timings(['commit'], ['query']);
  samples.push({ name: 'extraction7x2x300', driver: 'sqlite3', extra: { resources: 7, batches: 14, segments: 4200, completed: 4200, total: 4200, provider_calls: 14, usage_records: 14, provider_peak: 10 } });
  samples.at(-1).extra.timings = timings(['reserve'], ['query']);
  const translation = fullSamples()[4];
  translation.name = 'translation2x1x300';
  Object.assign(translation.extra, { resources: 2, source_batches: 2, segments: 600, completed: 600, total: 600, body_updated: 600, facts: 600, candidate_records: 600, provider_calls: 6, request_records: 9, completed_request_records: 6, actual_llm_batches: 6, usage_records: 6, resource_completed: [300, 300], request_size_distribution: { 100: 6 }, model_size_distribution: { 100: 6 }, commit_correlations: { events: 600, missing: 0, request_count: 6 } });
  samples.push(translation);
  const protocol = { ...options, protocol: true, bench: false };
  assert.doesNotThrow(() => validateMeasurements(samples.map(measured), protocol));
  assert.throws(() => validateMeasurements(samples.slice(0, -1).map(measured), protocol), /translation2x1x300/);
});

test('missing, null, non-finite or unsafe measured numbers and extra delay samples fail', () => {
  for (const change of [
    samples => { delete samples[0].sql; },
    samples => { samples[0].elapsed_ms = null; },
    samples => { samples[0].sql = Number.MAX_SAFE_INTEGER + 1; },
    samples => { samples[0].sql_p95_ms = -1; },
    samples => { samples[0].extra.timings['reserve.query'].p50_ms = NaN; },
    samples => { delete samples[0].extra.timings['reserve.query'].p95_ms; },
    samples => { samples[0].extra.timings['reserve.query'].count = null; },
    samples => { samples[0].extra.resource_completed[0] = JSON.parse('1e999'); },
    samples => { const extra = structuredClone(samples[0]); extra.extra.delay_ms = 3; samples.push(extra); },
  ]) {
    const samples = fullSamples();
    change(samples);
    assert.throws(() => validateMeasurements(samples, options), /Invalid persistence measurement/);
  }
  const samples = fullSamples();
  const reserve = measured({ name: 'reserve300', driver: 'sqlite3', extra: { delay_ms: 0, pool: 0 } });
  assert.throws(() => validateMeasurements([...samples, reserve, reserve], options), /exactly one/);
});

test('reusing a report directory cannot carry samples into a new run', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'persistence-report-test-'));
  try {
    const path = join(directory, 'samples.jsonl');
    await writeFile(path, fullSamples().map(sample => JSON.stringify(sample)).join('\n'));
    await initializeSamples(path);
    assert.equal(await readFile(path, 'utf8'), '');
    assert.throws(() => validateMeasurements([], options), /empty report/);
  } finally { await rm(directory, { recursive: true, force: true }); }
});

test('focused capacity acceptance rejects small, missing and duplicate actual facts', () => {
  function report() {
    const translation = fullSamples()[4];
    translation.name = 'translation2x1x300';
    Object.assign(translation.extra, { resources: 2, source_batches: 2, segments: 600, completed: 600, total: 600, body_updated: 600, facts: 600, candidate_records: 600, provider_calls: 6, usage_records: 6, actual_llm_batches: 6, completed_request_records: 6, request_records: 9, resource_completed: [300, 300], request_size_distribution: { 100: 6 }, model_size_distribution: { 100: 6 }, commit_correlations: { events: 600, missing: 0, request_count: 6 } });
    const measurements = ['seal5000', 'checkpoint300', 'calibrate'].map(name => measured({ name, driver: 'sqlite3' })).concat(translation);
    const facts = [false, true].map(fullJ => ({ name: 'ruby-growth-' + fullJ, facts: { driver: 'sqlite', members: 32, initial_bytes: 512 * 1024, candidate_capacity: 33 * 512 * 1024, grown_first: 750 * 1024, provider_calls: 32, provider_peak: 8, future_raw: fullJ, remaining_members: fullJ ? 1 : 0 } }));
    for (const [name, members, batch, peak, handoff, segments, concurrency] of [
      ['default_batch10_complete10_J64', 256, 10, 25, 64, 256, 32],
      ['default_batch10_complete10_J16', 256, 10, 3, 16, 256, 32],
      ['explicit_batch1_J16', 65, 1, 12, 16, 256, 32],
      ['complete10_S321_C32', 321, 10, 32, 64, 321, 32],
      ['complete10_S256_C4', 41, 10, 4, 64, 256, 4],
    ]) facts.push({ name: 'TestRubyHandoffAcceptanceHTTPHandoffCapacity/' + name, facts: { driver: 'sqlite', members, batch, provider_calls: Math.ceil(members / batch), provider_peak: peak, handoff_capacity: handoff * 1024 * 1024, candidate_segments: segments, requested_main: concurrency } });
    return { measurements, facts };
  }
  const valid = report();
  assert.doesNotThrow(() => validateCapacityMeasurements(valid.measurements, valid.facts));
  for (const change of [
    value => { value.facts[0].facts.members = 4; },
    value => { value.facts.pop(); },
    value => { value.facts[1] = value.facts[0]; },
    value => { value.facts[2].facts.provider_peak--; },
    value => { value.measurements[3].extra.source_batch_size = 20; },
    value => { value.measurements[3].extra.completed--; },
  ]) {
    const value = report();
    change(value);
    assert.throws(() => validateCapacityMeasurements(value.measurements, value.facts), /Invalid persistence measurement/);
  }
});
