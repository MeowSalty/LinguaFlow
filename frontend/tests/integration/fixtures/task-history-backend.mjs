import assert from 'node:assert/strict'
import { createHash, randomUUID } from 'node:crypto'
import { realpath } from 'node:fs/promises'
import path from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { createConfigurationBackend } from './configuration-backend.mjs'

export async function createTaskHistoryBackend(metadata, signal) {
  const backend = await createConfigurationBackend(metadata, signal, {
    storage: true,
    category: 'task-history-integration',
  })
  const request = (method, route, body, expected = 200, token) =>
    backend.request(
      method,
      route,
      body,
      expected,
      token,
      route === '/operations/batch-delete' ? 45_000 : 15_000,
    )
  async function identity(suffix, role = 'user') {
    const username = `history-${suffix}`
    const password = `Test-${randomUUID()}!`
    await request(
      'POST',
      '/admin/users',
      { username, email: `${username}@example.test`, password, role },
      201,
    )
    return request('POST', '/auth/login', { username, password }, 200, null)
  }
  async function workflow(actor = backend.authSession, organization) {
    const token = actor.access_token
    const own = (method, route, body, expected = 200) =>
      request(method, route, body, expected, token)
    const suffix = randomUUID().slice(0, 8)
    const profile = await own(
      'POST',
      '/execution-profiles',
      {
        ...(organization ? { org_id: organization.id } : {}),
        name: `History ${suffix}`,
        config: {
          schema_version: 1,
          ruby: { enabled: false, preserve_kinds: [] },
          context: { enabled: false, before: 0, after: 0 },
          qa: { enabled: false, checks: [] },
        },
      },
      201,
    )
    const assets = organization ? `/orgs/${organization.id}` : ''
    const credential = await own(
      'POST',
      `${assets}/credentials`,
      { provider: 'openai', endpoint: backend.endpoint, secret: backend.tokenSecrets[0] },
      201,
    )
    const provider = await own(
      'POST',
      `${assets}/backends`,
      backend.backendBody(
        `History ${suffix}`,
        `history-${suffix}`,
        backend.endpoint,
        credential.id,
      ),
      201,
    )
    const plan = await own(
      'POST',
      '/execution-plan-templates',
      {
        name: `History ${suffix}`,
        profile_id: profile.id,
        ...(organization ? { org_id: organization.id } : {}),
        rounds: [
          {
            mode: 'translate',
            backend_id: provider.id,
            concurrency: 1,
            translate: {
              prompt_template_id: -1,
              batch_size: 1,
              max_words_per_batch: 0,
              fallback_shrink: 1,
              retry: { max_attempts: 1, backoff_ms: 0, jitter: false },
            },
          },
        ],
      },
      201,
    )
    const scope = organization ? `org&organization_id=${organization.id}` : 'user'
    const options = await own('GET', `/storage/options?scope=${scope}`)
    const space = options.items.find((item) => item.selectable)
    assert.ok(space, 'fixture requires a selectable isolated Local storage space')
    const project = await own(
      'POST',
      organization ? `/orgs/${organization.id}/projects` : '/projects',
      {
        name: `History project ${suffix}`,
        source_lang: 'en',
        target_lang: 'zh',
        glossary_enabled: true,
        storage_space_id: space.space_id,
      },
      201,
    )
    const source = JSON.stringify({
      first: 'First source line',
      second: 'Second source line',
      third: 'Third source line',
    })
    const form = new FormData()
    form.append('files', new Blob([source], { type: 'application/json' }), 'source.json')
    const uploaded = await own('POST', `/projects/${project.id}/resources`, form)
    const resource = uploaded.items[0].resource
    const startJob = () =>
      own(
        'POST',
        `/projects/${project.id}/jobs`,
        { execution_plan_id: plan.id, segment_filter: 'all' },
        202,
      )
    const readyJob = async () => {
      const job = await startJob()
      return backend.until(
        () => own('GET', `/jobs/${job.id}`),
        (value) => value.status === 'completed' && value.can_delete,
        'completed and settled translation',
      )
    }
    const sync = async (oldTarget = '测试译文', newTarget = '同步后的译文') => {
      const entry = await own(
        'POST',
        `/projects/${project.id}/glossary`,
        { source: 'source', target: oldTarget },
        201,
      )
      await own('PUT', `/projects/${project.id}/glossary/${entry.id}`, {
        source: 'source',
        target: newTarget,
      })
      const task = await own(
        'POST',
        `/projects/${project.id}/glossary/${entry.id}/sync-execute`,
        { old_target: oldTarget, new_target: newTarget },
        202,
      )
      const detail = await backend.until(
        () => own('GET', `/projects/${project.id}/sync-tasks/${task.task_id}`),
        (value) => value.status === 'completed' && value.can_delete,
        'completed and settled glossary sync',
      )
      return { task: detail, entry }
    }
    const segments = () => own('GET', `/projects/${project.id}/resources/${resource.id}/segments`)
    return {
      actor,
      token,
      own,
      project,
      resource,
      source,
      provider,
      credential,
      plan,
      startJob,
      readyJob,
      sync,
      segments,
      model: `history-${suffix}`,
    }
  }
  async function withDatabase(operation, readOnly = true) {
    return backend.withStopped(async () => {
      const root = await realpath(backend.runDir)
      const artifacts = await realpath(path.resolve('tests/artifacts/task-history-integration'))
      assert.ok(
        root.startsWith(`${artifacts}${path.sep}`),
        'database must belong to this isolated retention fixture',
      )
      const filename = await realpath(path.join(root, 'data/linguaflow.db'))
      assert.ok(
        filename.startsWith(`${root}${path.sep}`),
        'resolved database must remain in this fixture',
      )
      const database = new DatabaseSync(filename, { readOnly })
      try {
        return operation(database)
      } finally {
        database.close()
      }
    })
  }
  const age = async (targets, days = 2) => {
    assert.ok(Number.isInteger(days) && days >= 2 && days <= 4000)
    // Ent stores fixed nine-digit fractions. Preserve that encoding so SQL cursor ordering
    // compares seeded timestamps identically to timestamps written by the Go driver.
    const timestamp = new Date(Date.now() - days * 86_400_000)
      .toISOString()
      .replace(/(\.\d{3})Z$/, '$1000000Z')
    await withDatabase((database) => {
      for (const target of targets) {
        assert.ok(['translation', 'glossary_sync'].includes(target.kind))
        assert.match(String(target.id), /^[1-9][0-9]*$/)
        const table = target.kind === 'translation' ? 'jobs' : 'sync_tasks'
        const result = database
          .prepare(
            `UPDATE ${table} SET finished_at = ?, retention_anchor_at = ? WHERE id = ? AND status IN ('completed', 'failed', 'cancelled')`,
          )
          .run(timestamp, timestamp, Number(target.id))
        assert.equal(Number(result.changes), 1, 'only a completed fixture task can be aged')
      }
    }, false)
    backend.recordObservation('Controlled age seed', {
      targets,
      days,
      backendStoppedDuringWrite: true,
      onlyFinishedAtAndRetentionAnchorChanged: true,
    })
  }
  async function seedLegacyMemory(project) {
    assert.ok(Number.isSafeInteger(project.id) && project.id > 0)
    // There is no public TM write API. This explicit legacy datum proves retention of
    // existing memory; it does not claim that the current translation path creates TM.
    await withDatabase((database) => {
      assert.equal(
        Number(
          database.prepare('SELECT count(*) AS count FROM projects WHERE id = ?').get(project.id)
            .count,
        ),
        1,
      )
      const timestamp = new Date().toISOString().replace(/(\.\d{3})Z$/, '$1000000Z')
      database
        .prepare(
          'INSERT INTO tm_entries (created_at, updated_at, scope_key, source_hash, source_text, target_text, source_lang, target_lang, usage_count, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)',
        )
        .run(
          timestamp,
          timestamp,
          `project:${project.id}`,
          createHash('sha256').update('Retained source memory').digest('hex'),
          'Retained source memory',
          '保留的记忆',
          'en',
          'zh',
          0,
          project.id,
        )
    }, false)
    backend.recordObservation('Explicit legacy TM fixture', {
      projectId: project.id,
      backendStoppedDuringWrite: true,
      seededEntries: 1,
      currentPipelineTMCreationNotTested: true,
    })
  }
  async function exportSnapshot(work) {
    const response = await fetch(
      `${backend.apiBase}/projects/${work.project.id}/resources/${work.resource.id}/exports`,
      {
        method: 'POST',
        headers: { Authorization: `Bearer ${work.token}`, 'Idempotency-Key': randomUUID() },
        signal: AbortSignal.timeout(15_000),
      },
    )
    assert.equal(response.status, 202)
    const task = await response.json()
    const finished = await backend.until(
      () => work.own('GET', `/projects/${work.project.id}/storage/tasks/${task.id}`),
      (value) => value.status === 'completed',
      'export artifact ready',
    )
    assert.ok(finished.result_artifact_id)
    const download = async () => {
      const result = await fetch(
        `${backend.apiBase}/projects/${work.project.id}/exports/${finished.result_artifact_id}/download`,
        { headers: { Authorization: `Bearer ${work.token}` }, signal: AbortSignal.timeout(15_000) },
      )
      assert.equal(result.status, 200)
      return Buffer.from(await result.arrayBuffer()).toString('base64')
    }
    return { task: finished, download, bytes: await download() }
  }
  return {
    ...backend,
    request,
    identity,
    workflow,
    age,
    withDatabase,
    exportSnapshot,
    seedLegacyMemory,
    get apiBase() {
      return backend.apiBase
    },
    get endpoint() {
      return backend.endpoint
    },
    get authSession() {
      return backend.authSession
    },
  }
}
