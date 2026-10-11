import assert from 'node:assert/strict'
import { beforeAll, test as base } from 'vitest'
import {
  buildConfigurationBackend,
  createConfigurationBackend,
} from './fixtures/configuration-backend.mjs'
import {
  assertRubyResults,
  makeRubyProject,
  makeRubyWorkflow,
  observeRubyRequest,
  rubyResponse,
} from './fixtures/ruby-backend.mjs'

let metadata
beforeAll(() => {
  metadata = buildConfigurationBackend()
})
const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const fixture = await createConfigurationBackend(metadata, signal, {
      category: 'ruby-alignment-integration',
      upstreamTimeoutSeconds: 120,
      respond: rubyResponse,
      observeRequest: observeRubyRequest,
    })
    onTestFinished(() => fixture.writeReport(task))
    try {
      await fixture.start()
      await use(fixture)
      assert.deepEqual(fixture.upstreamErrors, [])
    } finally {
      await fixture.close()
    }
  },
})

test('ruby concurrency presence, durable stage observations and pause/resume over real HTTP', async ({
  backend,
}) => {
  const { request, until, getJob, terminalJob, holds, release, calls } = backend
  const flow = await backend.makeWorkflow('ruby-main')
  const alignment = await request(
    'POST',
    '/backends',
    backend.backendBody('Alignment', 'ruby-align', backend.endpoint, flow.credential.id),
    201,
  )
  await request('PUT', `/execution-profiles/${flow.profile.id}`, {
    config: { schema_version: 1, ruby: { enabled: true, preserve_kinds: ['creative'] } },
  })
  const retry = { enabled: true, backend_id: alignment.id, max_attempts: 2 }
  await request('PUT', `/execution-plan-templates/${flow.plan.id}`, { ruby_retry: retry })
  assert.equal(
    (await request('GET', `/execution-plan-templates/${flow.plan.id}`)).ruby_retry.concurrency,
    undefined,
  )
  await request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
    ruby_retry: { ...retry, concurrency: 2 },
  })
  assert.equal(
    (await request('GET', `/execution-plan-templates/${flow.plan.id}`)).ruby_retry.concurrency,
    2,
  )
  await request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
    ruby_retry: { ...retry, enabled: false, concurrency: 2 },
  })
  const disabledPlan = await request('GET', `/execution-plan-templates/${flow.plan.id}`)
  backend.recordObservation('disabled ruby configuration response', {
    returned: disabledPlan.ruby_retry ?? null,
    roundTripSupported: disabledPlan.ruby_retry?.concurrency === 2,
    limitation: disabledPlan.ruby_retry
      ? null
      : 'Backend omits ruby_retry when disabled; dormant values cannot be read back by the frontend.',
  })
  for (const value of [0, -1, 1.5]) {
    const result = await request(
      'PUT',
      `/execution-plan-templates/${flow.plan.id}`,
      {
        ruby_retry: { ...retry, enabled: false, concurrency: value },
      },
      null,
    )
    assert.ok([400, 422].includes(result.status), `invalid concurrency ${value}: ${result.status}`)
  }
  await request('PUT', `/execution-plan-templates/${flow.plan.id}`, { ruby_retry: retry })
  const project = await request(
    'POST',
    '/projects',
    {
      name: 'Ruby stages',
      source_lang: 'ja',
      target_lang: 'en',
      glossary_enabled: false,
    },
    201,
  )
  const upload = new FormData()
  upload.append(
    'files',
    new Blob(
      [
        JSON.stringify({
          one: '<ruby>勇者<rt>ヒーロー</rt></ruby> one',
          two: '<ruby>勇者<rt>ヒーロー</rt></ruby> two',
          three: '<ruby>勇者<rt>ヒーロー</rt></ruby> three',
        }),
      ],
      { type: 'application/json' },
    ),
    'ruby.json',
  )
  await request('POST', `/projects/${project.id}/resources`, upload)
  holds.add('ruby-align')
  const job = await backend.startJob(project, flow.plan)
  const aligning = await until(
    () => getJob(job.id),
    (value) =>
      value.progress.stages.alignment_requests === 1 && value.progress.stages.pending_alignment > 0,
    'alignment request in flight',
  )
  assert.equal(aligning.status, 'running')
  assert.equal(
    aligning.progress.progress_completed,
    0,
    'main candidates are not confirmed translations',
  )
  const pausedResponse = await request('POST', `/jobs/${job.id}/pause`)
  assert.equal(pausedResponse.status, 'pausing')
  const pausing = await getJob(job.id)
  assert.equal(pausing.status, 'pausing')
  assert.ok(pausing.progress.stages.draining_requests >= 1)
  assert.ok((await request('GET', '/jobs?status=pausing')).items.some((item) => item.id === job.id))
  assert.equal((await request('GET', '/jobs/summary')).pausing, 1)
  const operations = await request('GET', '/operations?state=active')
  const summary = await request('GET', '/operations/summary')
  const operation = operations.items.find(
    (item) => item.task_type === 'translation' && String(item.task_id) === String(job.id),
  )
  assert.equal(operation?.status, 'pausing', 'active discovery includes the draining job')
  assert.ok(operation.supported_actions.includes('view'))
  assert.ok(operation.supported_actions.includes('cancel'))
  assert.equal(operation.can_delete, false)
  assert.equal(summary.total.pausing, 1)
  assert.equal(summary.total.running, 0)
  assert.equal(summary.total.paused, 0)
  assert.equal(summary.by_type.translation.pausing, 1)
  for (const type of ['translation', 'glossary_sync', 'storage'])
    assert.equal(typeof summary.by_type[type].pausing, 'number')
  const filtered = await request('GET', '/operations?status=pausing')
  assert.ok(filtered.items.some((item) => String(item.task_id) === String(job.id)))
  assert.ok(filtered.items.every((item) => item.status === 'pausing'))
  backend.recordObservation('verified operations pausing contract', {
    discovered: true,
    pausingCount: summary.total.pausing,
    filtered: true,
  })
  const streamAbort = new AbortController()
  const stream = await fetch(
    `${backend.apiBase}/jobs/${job.id}/stream?access_token=${encodeURIComponent(backend.authSession.access_token)}`,
    { signal: streamAbort.signal },
  )
  assert.equal(stream.status, 200)
  const captured = []
  const reading = (async () => {
    const reader = stream.body.getReader(),
      decoder = new TextDecoder()
    let pending = ''
    try {
      while (true) {
        const chunk = await reader.read()
        if (chunk.done) return
        pending += decoder.decode(chunk.value, { stream: true })
        const frames = pending.split(/\r?\n\r?\n/)
        pending = frames.pop()
        for (const frame of frames) {
          const data = frame.split(/\r?\n/).find((line) => line.startsWith('data:'))
          if (data) captured.push(JSON.parse(data.slice(5)))
        }
      }
    } catch (error) {
      if (!streamAbort.signal.aborted) throw error
    } finally {
      reader.releaseLock()
    }
  })()
  try {
    release('ruby-align')
    const paused = await until(
      () => getJob(job.id),
      (value) => value.status === 'paused',
      'safe pause',
    )
    assert.equal(paused.progress.stages.draining_requests, 0)
    await until(
      () => captured,
      (events) => events.some((event) => event.type === 'stage_counts'),
      'SSE stage observation',
    )
    assert.ok(
      captured.some((event) => event.type === 'stage_counts' && event.metadata?.stages?.as_of),
    )
    await request('POST', `/jobs/${job.id}/resume`)
    const completed = await terminalJob(job.id)
    assert.equal(completed.status, 'completed', completed.error_message)
    assert.equal(completed.progress.stages.confirmed_work, 3)
    assert.equal(completed.progress.progress_completed, 3)
    assert.equal(
      calls.filter((call) => call.model === 'ruby-main').length,
      3,
      'resume must not regenerate durable candidates',
    )
    assert.deepEqual(
      calls.filter((call) => call.model === 'ruby-align').map((call) => call.memberCount),
      [1, 1, 1],
      'omitted batch configuration retains single-paragraph requests',
    )
    backend.recordObservation('ruby stages and safe pause', {
      aligning: aligning.progress.stages,
      pausing: pausing.progress.stages,
      paused: paused.progress.stages,
      completed: completed.progress.stages,
      sseTypes: [...new Set(captured.map((event) => event.type))],
      mainRequests: calls.filter((call) => call.model === 'ruby-main').length,
      alignmentRequests: calls.filter((call) => call.model === 'ruby-align').length,
    })
  } finally {
    streamAbort.abort()
    await reading
  }
})

test('P2 HTTP configuration preserves omission and zero and validates limits even when disabled', async ({
  backend,
}) => {
  const flow = await makeRubyWorkflow(backend)
  const baseRetry = { enabled: true, backend_id: flow.alignment.id, max_attempts: 2 }
  const fields = ['batch_size', 'max_words_per_batch', 'batch_wait_ms']
  const valid = [
    {},
    { batch_size: 4, max_words_per_batch: 0, batch_wait_ms: 100 },
    { batch_size: 0, max_words_per_batch: 1000, batch_wait_ms: 0 },
    { max_words_per_batch: 0, batch_wait_ms: 0 },
    { batch_size: 1, batch_wait_ms: 9223372036854 },
    {},
  ]
  for (const [index, config] of valid.entries()) {
    const rubyRetry = { ...baseRetry, ...config }
    const created = await backend.request(
      'POST',
      '/execution-plan-templates',
      {
        name: `P2 presence ${index}`,
        profile_id: flow.profile.id,
        rounds: flow.plan.rounds,
        ruby_retry: rubyRetry,
      },
      201,
    )
    await backend.request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
      ruby_retry: rubyRetry,
    })
    for (const id of [created.id, flow.plan.id]) {
      const stored = (await backend.request('GET', `/execution-plan-templates/${id}`)).ruby_retry
      for (const field of fields) {
        assert.equal(stored[field], config[field], `${field} read back after create/update`)
        assert.equal(Object.hasOwn(stored, field), Object.hasOwn(config, field))
      }
    }
  }
  const invalid = [
    { batch_size: 0 },
    { batch_size: 0, max_words_per_batch: 0 },
    ...fields.flatMap((field) => [{ [field]: -1 }, { [field]: 1.5 }]),
    { batch_wait_ms: 9223372036855 },
  ]
  for (const enabled of [true, false])
    for (const config of invalid) {
      const rubyRetry = { ...baseRetry, enabled, ...config }
      for (const method of ['POST', 'PUT']) {
        const result = await backend.request(
          method,
          method === 'POST'
            ? '/execution-plan-templates'
            : `/execution-plan-templates/${flow.plan.id}`,
          {
            ...(method === 'POST'
              ? { name: 'Invalid P2 plan', profile_id: flow.profile.id, rounds: flow.plan.rounds }
              : {}),
            ruby_retry: rubyRetry,
          },
          null,
        )
        assert.ok(
          [400, 422].includes(result.status),
          `${method} enabled=${enabled} ${JSON.stringify(config)} returned ${result.status}`,
        )
      }
    }
  const unchanged = await backend.request('GET', `/execution-plan-templates/${flow.plan.id}`)
  for (const field of fields) assert.equal(Object.hasOwn(unchanged.ruby_retry, field), false)
  await backend.request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
    ruby_retry: {
      ...baseRetry,
      enabled: false,
      batch_size: 0,
      max_words_per_batch: 1000,
      batch_wait_ms: 0,
    },
  })
  const disabled = await backend.request('GET', `/execution-plan-templates/${flow.plan.id}`)
  backend.recordObservation('P2 configuration contract', {
    validCreateAndUpdateCases: valid.length,
    invalidCreateAndUpdateCases: invalid.length * 2,
    omissionRestoredAfterExplicitValues: true,
    maximumWaitAccepted: 9223372036854,
    disabledReadbackSupported: disabled.ruby_retry !== undefined,
    limitation: disabled.ruby_retry
      ? null
      : 'Backend omits disabled ruby_retry; persisted dormant fields cannot be read by the frontend.',
  })
})

for (const scenario of [
  { name: 'B4 full batch', count: 4, batchSize: 4, words: 0, expected: [4] },
  { name: 'B4 incomplete tail', count: 3, batchSize: 4, words: 0, expected: [3] },
  { name: 'word-only batch', count: 4, batchSize: 0, words: 1000, expected: [4] },
  { name: 'word-only oversized members', count: 4, batchSize: 0, words: 1, expected: [1, 1, 1, 1] },
])
  test(`P2 ${scenario.name} uses real request members and confirms paragraphs separately`, async ({
    backend,
  }) => {
    const flow = await makeRubyWorkflow(backend, {
      mainBatch: 4,
      retry: {
        batch_size: scenario.batchSize,
        max_words_per_batch: scenario.words,
        batch_wait_ms: 100,
      },
    })
    const { project, resource } = await makeRubyProject(backend, scenario.name, scenario.count)
    backend.holds.add('ruby-align')
    const job = await backend.startJob(project, flow.plan)
    const observing = await backend.until(
      () => backend.getJob(job.id),
      (current) =>
        current.progress.stages.alignment_requests === 1 &&
        current.progress.stages.pending_alignment === scenario.count,
      'all candidates durable while one real alignment request is blocked',
    )
    assert.equal(observing.progress.progress_completed, 0)
    assert.equal(observing.progress.stages.confirmed_work, 0)
    const first = backend.calls.filter((call) => call.model === 'ruby-align')
    assert.equal(first.length, 1, 'A=1 admits one request, independent of member count')
    assert.equal(first[0].memberCount, scenario.expected[0])
    backend.release('ruby-align')
    const completed = await backend.terminalJob(job.id)
    assert.equal(completed.status, 'completed', completed.error_message)
    assert.equal(completed.progress.progress_completed, scenario.count)
    assert.equal(completed.progress.stages.confirmed_work, scenario.count)
    const main = backend.calls.filter((call) => call.kind === 'main')
    const alignment = backend.calls.filter((call) => call.model === 'ruby-align')
    assert.equal(main.length, 1)
    assert.equal(main[0].memberCount, scenario.count)
    assert.deepEqual(
      alignment.map((call) => call.memberCount),
      scenario.expected,
    )
    assert.ok(
      alignment.every((call) => call.members.every((member) => member.missingIds.length === 1)),
    )
    const batchMembers = alignment.flatMap((call) => call.members).filter((member) => member.workId)
    assert.equal(new Set(batchMembers.map((member) => member.workId)).size, batchMembers.length)
    const verified = await assertRubyResults(
      backend,
      project,
      resource,
      scenario.count,
      main.length + alignment.length,
    )
    backend.recordObservation('P2 real HTTP batching', {
      scenario: scenario.name,
      configuration: flow.plan.ruby_retry,
      mainConcurrency: 1,
      alignmentConcurrency: 1,
      mainRequests: main.length,
      alignmentRequests: alignment.length,
      actualBatchSizes: alignment.map((call) => call.memberCount),
      blockedStages: observing.progress.stages,
      completedStages: completed.progress.stages,
      reversedResponseMembers: true,
      ...verified,
    })
  })
