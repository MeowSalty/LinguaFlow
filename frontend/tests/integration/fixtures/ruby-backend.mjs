import assert from 'node:assert/strict'

const rubyOutput = (member) =>
  member.missing.map((item) => ({
    id: item.id,
    base: 'alpha',
    text: 'hero',
    kind: 'creative',
    occurrence: 1,
  }))

// One real upstream implements both P2 envelopes and the default single-member v2 protocol.
export function rubyResponse(envelope) {
  if (Array.isArray(envelope.alignments))
    return {
      alignments: envelope.alignments.toReversed().map((member) => ({
        work_id: member.work_id,
        candidate_id: member.candidate_id,
        ruby_output: rubyOutput(member),
      })),
    }
  if (Array.isArray(envelope.missing)) return { ruby_output: rubyOutput(envelope) }
  assert.ok(envelope.segments, 'unrecognized model request envelope')
  return {
    translations: Object.fromEntries(
      Object.entries(envelope.segments)
        .filter(([, segment]) => segment.translate)
        .map(([id]) => [id, 'alpha']),
    ),
  }
}

// Observe identities and counts before an HTTP barrier; never persist prompts or credentials.
export function observeRubyRequest(envelope) {
  const members = envelope.alignments ?? (envelope.missing ? [envelope] : null)
  if (members)
    return {
      kind: envelope.alignments ? 'alignment_batch' : 'alignment_single',
      memberCount: members.length,
      members: members.map((member) => ({
        ...(member.work_id ? { workId: member.work_id } : {}),
        ...(member.candidate_id ? { candidateId: member.candidate_id } : {}),
        missingIds: member.missing.map((item) => item.id),
      })),
    }
  const segmentIds = Object.entries(envelope.segments)
    .filter(([, segment]) => segment.translate)
    .map(([id]) => id)
  return { kind: 'main', memberCount: segmentIds.length, segmentIds }
}

export async function makeRubyWorkflow(backend, options = {}) {
  const flow = await backend.makeWorkflow(options.mainModel ?? 'ruby-main')
  const alignment = await backend.request(
    'POST',
    '/backends',
    backend.backendBody(
      'Alignment',
      options.alignmentModel ?? 'ruby-align',
      backend.endpoint,
      flow.credential.id,
    ),
    201,
  )
  await backend.request('PUT', `/execution-profiles/${flow.profile.id}`, {
    config: { schema_version: 1, ruby: { enabled: true, preserve_kinds: ['creative'] } },
  })
  const plan = await backend.request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
    rounds: flow.plan.rounds.map((round) => ({
      ...round,
      concurrency: 1,
      translate: { ...round.translate, batch_size: options.mainBatch ?? 1 },
    })),
    ruby_retry: {
      enabled: true,
      backend_id: alignment.id,
      max_attempts: 2,
      concurrency: 1,
      ...options.retry,
    },
  })
  return { ...flow, plan, alignment }
}

export async function makeRubyProject(backend, name, segmentCount) {
  const project = await backend.request(
    'POST',
    '/projects',
    { name, source_lang: 'ja', target_lang: 'en', glossary_enabled: false },
    201,
  )
  const form = new FormData()
  form.append(
    'files',
    new Blob(
      [
        JSON.stringify(
          Object.fromEntries(
            Array.from({ length: segmentCount }, (_, index) => [
              `segment_${index + 1}`,
              `<ruby>勇者<rt>ヒーロー</rt></ruby> item${index + 1}`,
            ]),
          ),
        ),
      ],
      { type: 'application/json' },
    ),
    `${name}.json`,
  )
  const uploaded = await backend.request('POST', `/projects/${project.id}/resources`, form)
  assert.equal(uploaded.items[0].resource.total_segments, segmentCount)
  return { project, resource: uploaded.items[0].resource }
}

export async function assertRubyResults(backend, project, resource, segmentCount, requestCount) {
  const segments = await backend.request(
    'GET',
    `/projects/${project.id}/resources/${resource.id}/segments?limit=100`,
  )
  assert.equal(segments.items.length, segmentCount)
  for (const segment of segments.items) {
    assert.ok(['translated', 'approved'].includes(segment.status))
    assert.match(segment.target_text, /<ruby\b[^>]*>alpha<rt\b[^>]*>hero<\/rt><\/ruby>/)
    assert.ok(
      (segment.quality_issues ?? []).every((issue) => !issue.code.startsWith('ruby_')),
      `segment ${segment.id} must have complete, verified ruby`,
    )
  }
  const usage = await backend.request('GET', '/stats/summary')
  // The HTTP fixture returns one input and one output token per invocation, including batches.
  for (const field of ['api_calls', 'usage_records', 'input_tokens', 'output_tokens'])
    assert.equal(usage[field], requestCount, `${field} is counted once per actual model invocation`)
  return {
    committedRubyParagraphs: segments.items.length,
    rubyQualityWarnings: 0,
    apiCalls: usage.api_calls,
    inputTokens: usage.input_tokens,
    outputTokens: usage.output_tokens,
  }
}
