import assert from 'node:assert/strict'
import { beforeAll, test as base } from 'vitest'
import {
  buildConfigurationBackend,
  createConfigurationBackend,
} from './fixtures/configuration-backend.mjs'

let metadata
beforeAll(() => {
  metadata = buildConfigurationBackend()
})
const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const fixture = await createConfigurationBackend(metadata, signal, {
      category: 'ruby-alignment-integration',
      upstreamTimeoutSeconds: 120,
      respond: (envelope) =>
        envelope.missing
          ? {
              ruby_output: envelope.missing.map((item) => ({
                id: item.id,
                base: 'alpha',
                text: 'hero',
                kind: 'creative',
                occurrence: 1,
              })),
            }
          : {
              translations: Object.fromEntries(
                Object.entries(envelope.segments)
                  .filter(([, segment]) => segment.translate)
                  .map(([id]) => [id, 'alpha']),
              ),
            },
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
