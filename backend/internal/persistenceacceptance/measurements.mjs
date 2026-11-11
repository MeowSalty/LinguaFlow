import { isDeepStrictEqual } from 'node:util';
import { writeFile } from 'node:fs/promises';

export async function initializeSamples(path) {
  await writeFile(path, '', { mode: 0o600 });
}

function requireCondition(condition, message) {
  if (!condition) throw new Error('Invalid persistence measurement: ' + message);
}

function exact(extra, fields) {
  for (const [key, value] of Object.entries(fields)) requireCondition(extra[key] === value, key + ' must equal ' + value);
}

function nonnegative(value, label, integer = false) {
  requireCondition(Number.isFinite(value) && value >= 0 && (!integer || Number.isSafeInteger(value)), label + ' must be a finite nonnegative ' + (integer ? 'safe integer' : 'number'));
}

function finiteNumbers(value) {
  if (typeof value === 'number') requireCondition(Number.isFinite(value), 'non-finite numeric value');
  else if (value && typeof value === 'object') for (const child of Object.values(value)) finiteNumbers(child);
}

function measuredSample(sample) {
  requireCondition(typeof sample.name === 'string' && sample.name.length > 0, 'sample name');
  for (const key of ['sql', 'begins', 'commits', 'rollbacks']) nonnegative(sample[key], key, true);
  for (const key of ['elapsed_ms', 'sql_p50_ms', 'sql_p95_ms', 'txn_p50_ms', 'txn_p95_ms']) nonnegative(sample[key], key);
  const extra = sample.extra || {};
  if (Object.hasOwn(extra, 'delay_ms')) nonnegative(extra.delay_ms, 'delay_ms', true);
  if (sample.name === 'reserve300') nonnegative(extra.pool, 'pool', true);
  const hasTimings = ['commit-observations', 'extraction7x2x300', 'load7x10x300', 'translation2x1x300', 'translation7x2x300'].includes(sample.name);
  if (hasTimings) requireCondition(extra.timings && typeof extra.timings === 'object' && !Array.isArray(extra.timings) && Object.keys(extra.timings).length > 0, 'timings shape');
  if (extra.timings) for (const [name, timing] of Object.entries(extra.timings)) {
    if (name.endsWith('_peak')) nonnegative(timing, name, true);
    else {
      requireCondition(timing && typeof timing === 'object' && !Array.isArray(timing), name + ' timing shape');
      nonnegative(timing.count, name + '.count', true);
      nonnegative(timing.p50_ms, name + '.p50_ms');
      nonnegative(timing.p95_ms, name + '.p95_ms');
      requireCondition(timing.p50_ms <= timing.p95_ms, name + ' percentile order');
    }
  }
  finiteNumbers(sample);
}

function one(measurements, name, driver, delay) {
  const matches = measurements.filter(sample => sample.name === name && sample.driver === driver && (delay === undefined || sample.extra?.delay_ms === delay));
  requireCondition(matches.length === 1, 'expected exactly one ' + name + (delay === undefined ? '' : '/' + delay));
  return matches[0].extra || {};
}

function load(extra, resources, segmentsPerResource, operations, phases) {
  const segments = resources * segmentsPerResource;
  exact(extra, { resources, segments, completed: segments, total: segments });
  requireCondition(Object.hasOwn(extra, 'failures') && (extra.failures === null || (Array.isArray(extra.failures) && extra.failures.length === 0)), 'load failures');
  requireCondition(Number.isSafeInteger(extra.provider_peak) && extra.provider_peak > 0 && extra.provider_peak <= 10, 'provider peak');
  requireCondition(Array.isArray(extra.resource_completed) && extra.resource_completed.length === resources && extra.resource_completed.every(value => value === segmentsPerResource), 'resource completion');
  for (const [key, capacity] of Object.entries({ pending_owner_peak: 16, coordinator_intent_peak: 16, normal_queue_peak: 16, reliable_queue_peak: 32, completion_peak: 16 })) {
    requireCondition(Number.isSafeInteger(extra.timings?.[key]) && extra.timings[key] >= 0 && extra.timings[key] <= capacity, key);
  }
  for (const operation of operations) for (const phase of phases) {
    requireCondition(Number.isSafeInteger(extra.timings?.[operation + '.' + phase]?.count) && extra.timings[operation + '.' + phase].count > 0, operation + '.' + phase);
  }
}

function translation(extra, resources, batches) {
  const segments = resources * batches * 300;
  load(extra, resources, batches * 300, ['reserve', 'sent', 'response_handoff', 'member_promotion', 'commit', 'stage_initialize'], ['save_queue', 'transaction', 'query', 'exec']);
  exact(extra, { source_batches: resources * batches, source_batch_size: 300, body_updated: segments, facts: segments, candidate_records: segments, invalid_requests: 0, duplicate_or_missing_members: 0 });
  const requests = extra.actual_llm_batches;
  requireCondition(Number.isSafeInteger(requests) && requests >= resources * batches, 'actual LLM batches');
  exact(extra, { completed_request_records: requests, provider_calls: requests, usage_records: requests, invalid_aborted_requests: 0 });
  requireCondition(Number.isSafeInteger(extra.aborted_request_records) && extra.aborted_request_records >= 0 && Number.isSafeInteger(extra.request_records) && extra.request_records === requests + extra.aborted_request_records, 'completed/aborted request audit totals');
  const distribution = extra.request_size_distribution;
  requireCondition(Number.isSafeInteger(extra.default_candidate_segments) && extra.default_candidate_segments > 0, 'default candidate window');
  requireCondition(distribution && isDeepStrictEqual(distribution, extra.model_size_distribution), 'request/model size distribution');
  let members = 0;
  let calls = 0;
  for (const [size, count] of Object.entries(distribution)) {
    requireCondition(Number.isSafeInteger(Number(size)) && Number(size) > 0 && Number(size) <= Math.min(300, extra.default_candidate_segments) && Number.isSafeInteger(count) && count > 0, 'request distribution entry');
    members += Number(size) * count;
    calls += count;
  }
  requireCondition(members === segments && calls === requests, 'request member/call totals');
  exact(extra.commit_correlations || {}, { events: segments, missing: 0, request_count: requests });
}

export function validateMeasurements(measurements, { driver, protocol, bench }) {
  requireCondition(measurements.length > 0 && measurements.every(sample => sample.driver === driver), 'driver or empty report');
  const identities = new Set();
  for (const sample of measurements) {
    measuredSample(sample);
    const delay = sample.extra?.delay_ms;
    const identity = JSON.stringify([sample.name, delay ?? null]);
    requireCondition(!identities.has(identity), 'expected exactly one sample per name/delay');
    identities.add(identity);
    const delays = sample.name === 'load7x10x300' || sample.name === 'reserve300' ? [0, 1, 2, 5] : sample.name === 'translation7x2x300' ? [0, 5] : null;
    if (delays) requireCondition(delays.includes(delay), sample.name + ' delay');
  }
  if (protocol) {
    for (const delay of [0, 1, 2, 5]) one(measurements, 'reserve300', driver, delay);
    for (const name of ['pool-advance-reserve300', 'seal5000', 'checkpoint300', 'calibrate', 'commit-observations']) one(measurements, name, driver);
  } else {
    for (const delay of [0, 1, 2, 5]) {
      const extra = one(measurements, 'load7x10x300', driver, delay);
      load(extra, 7, 3000, ['reserve', 'sent', 'received', 'completed', 'checkpoint', 'stage_initialize'], ['save_queue', 'query', 'exec']);
      exact(extra, { batches: 70, provider_calls: 70, usage_records: 70 });
    }
    for (const delay of [0, 5]) translation(one(measurements, 'translation7x2x300', driver, delay), 7, 2);
  }
  if (!bench) {
    const extraction = one(measurements, 'extraction7x2x300', driver);
    exact(extraction, { resources: 7, batches: 14, segments: 4200, completed: 4200, total: 4200, provider_calls: 14, usage_records: 14, provider_peak: 10 });
    translation(one(measurements, 'translation2x1x300', driver), 2, 1);
  }
}
