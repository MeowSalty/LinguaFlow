import assert from 'node:assert/strict'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'
import { beforeAll, test as base } from 'vitest'
import { buildConfigurationBackend } from './fixtures/configuration-backend.mjs'
import { createTaskHistoryBackend } from './fixtures/task-history-backend.mjs'

let metadata
beforeAll(() => {
  metadata = buildConfigurationBackend()
})
const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const backend = await createTaskHistoryBackend(metadata, signal)
    onTestFinished(() => backend.writeReport(task))
    try {
      await backend.start()
      await use(backend)
      assert.deepEqual(backend.upstreamErrors, [])
    } finally {
      await backend.close()
    }
  },
})
const target = (kind, id, project) => ({ kind, id: String(id), project_id: project.id })

test('H1 real admin settings preserve independent registration, preview while disabled, and reject stale revisions', async ({
  backend,
}) => {
  const ordinary = await backend.identity('ordinary')
  const secondAdmin = await backend.identity('second-admin', 'admin')
  for (const route of ['/admin/settings', '/admin/task-retention/status'])
    await backend.request('GET', route, undefined, 403, ordinary.access_token)
  await backend.request(
    'POST',
    '/admin/task-retention/preview',
    { retention_days: 1 },
    403,
    ordinary.access_token,
  )
  const first = (await backend.request('GET', '/admin/settings')).settings
  assert.deepEqual(first.task_retention, { enabled: false, retention_days: 30, revision: 1 })
  const preview = await backend.request('POST', '/admin/task-retention/preview', {
    retention_days: 1,
  })
  assert.equal(preview.retention_days, 1)
  assert.equal(preview.partial, false)
  assert.equal(preview.by_type.translation.expired.deletable, 0)
  assert.equal((await backend.request('GET', '/admin/task-retention/status')).state, 'disabled')
  for (const days of [null, 0, -1, 1.5, 3651])
    await backend.request('POST', '/admin/task-retention/preview', { retention_days: days }, 400)
  const second = await backend.request('PATCH', '/admin/settings', {
    settings: { task_retention: { enabled: false, retention_days: 7, expected_revision: 1 } },
  })
  assert.equal(second.settings.registration_enabled, first.registration_enabled)
  assert.equal(second.settings.task_retention.revision, 2)
  const conflict = await backend.request(
    'PATCH',
    '/admin/settings',
    { settings: { task_retention: { enabled: true, retention_days: 1, expected_revision: 1 } } },
    409,
    secondAdmin.access_token,
  )
  assert.equal(conflict.error_code, 'settings_conflict')
  const registered = await backend.request('PATCH', '/admin/settings', {
    settings: { registration_enabled: true },
  })
  assert.deepEqual(registered.settings.task_retention, second.settings.task_retention)
  backend.recordObservation('Real CAS and separated settings fields', {
    revisions: [1, 2],
    staleStatus: 409,
    registrationUnchangedByRetention: true,
    previewWhileDisabled: true,
  })
})

test('H2 personal owner can delete settled history; active and paused jobs cannot be deleted', async ({
  backend,
}) => {
  const owner = await backend.identity('owner')
  const work = await backend.workflow(owner)
  backend.holds.add(work.model)
  const active = await work.startJob()
  await backend.until(
    () => work.own('GET', `/jobs/${active.id}`),
    (job) => job.status === 'running',
    'active translation',
  )
  const running = await work.own('GET', `/jobs/${active.id}`)
  assert.equal(running.can_delete, false)
  assert.equal(
    (await work.own('DELETE', `/jobs/${active.id}`, undefined, 409)).error_code,
    'task_not_terminal',
  )
  await work.own('POST', `/jobs/${active.id}/pause`)
  backend.release(work.model)
  const paused = await backend.until(
    () => work.own('GET', `/jobs/${active.id}`),
    (job) => job.status === 'paused',
    'paused translation',
  )
  assert.equal(paused.can_delete, false)
  await work.own('DELETE', `/jobs/${active.id}`, undefined, 409)
  await work.own('POST', `/jobs/${active.id}/cancel`)
  await backend.until(
    () => work.own('GET', `/jobs/${active.id}`),
    (job) => job.can_delete,
    'cancelled task settled',
  )
  await backend.request('DELETE', `/jobs/${active.id}`, undefined, 403)
  assert.equal(await work.own('DELETE', `/jobs/${active.id}`, undefined, 204), undefined)
  await work.own('DELETE', `/jobs/${active.id}`, undefined, 404)
  backend.recordObservation('Personal owner and unrelated platform administrator', {
    ownerDeleted: true,
    unrelatedSystemAdminStatus: 403,
    activeAndPausedStatus: 409,
    repeatedDeleteStatus: 404,
  })
})

test('H3 organization owner/admin/member permissions are projected and rechecked for both history types', async ({
  backend,
}) => {
  const owner = await backend.identity('org-owner')
  const admin = await backend.identity('org-admin')
  const member = await backend.identity('org-member')
  const outsider = await backend.identity('outsider')
  const org = await backend.request(
    'POST',
    '/orgs',
    { name: 'History roles', slug: 'history-roles' },
    201,
    owner.access_token,
  )
  for (const [actor, role] of [
    [admin, 'admin'],
    [member, 'member'],
  ])
    await backend.request(
      'POST',
      `/orgs/${org.id}/members`,
      { username: actor.user.username, role },
      201,
      owner.access_token,
    )
  const work = await backend.workflow(owner, org)
  const job = await work.readyJob()
  const { task } = await work.sync()
  const paths = [`/jobs/${job.id}`, `/projects/${work.project.id}/sync-tasks/${task.task_id}`]
  for (const route of paths) {
    for (const actor of [owner, admin])
      assert.equal(
        (await backend.request('GET', route, undefined, 200, actor.access_token)).can_delete,
        true,
      )
    assert.equal(
      (await backend.request('GET', route, undefined, 200, member.access_token)).can_delete,
      false,
    )
    for (const actor of [member, outsider, backend.authSession])
      await backend.request('DELETE', route, undefined, 403, actor.access_token)
  }
  await backend.request(
    'PUT',
    `/orgs/${org.id}/members/${admin.user.id}`,
    { role: 'member' },
    200,
    owner.access_token,
  )
  await backend.request('DELETE', paths[0], undefined, 403, admin.access_token)
  assert.equal(
    (await backend.request('GET', paths[0], undefined, 200, admin.access_token)).can_delete,
    false,
  )
  await backend.request(
    'PUT',
    `/orgs/${org.id}/members/${admin.user.id}`,
    { role: 'admin' },
    200,
    owner.access_token,
  )
  await backend.request('DELETE', paths[0], undefined, 204, admin.access_token)
  await backend.request('DELETE', paths[1], undefined, 204, owner.access_token)
  backend.recordObservation('Organization role matrix', {
    ownerAndAdminCanDelete: true,
    memberOutsiderSystemAdminDenied: true,
    demotedAdminCanStillRead: true,
    bothKindsDeleted: true,
  })
})

test('H4 batch deletion removes execution data while preserving source, translations, glossary, TM, exports, usage and audit', async ({
  backend,
}) => {
  const work = await backend.workflow()
  const job = await work.readyJob()
  const { task } = await work.sync()
  assert.equal(
    String(job.id),
    task.task_id,
    'isolated task tables start at the same numeric identity',
  )
  const exportArtifact = await backend.exportSnapshot(work)
  await backend.seedLegacyMemory(work.project)
  const beforeSegments = await work.segments()
  const originalBytes = async () => {
    const response = await fetch(
      `${backend.apiBase}/projects/${work.project.id}/resources/${work.resource.id}/download`,
      { headers: { Authorization: `Bearer ${work.token}` }, signal: AbortSignal.timeout(15_000) },
    )
    assert.equal(response.status, 200)
    return Buffer.from(await response.arrayBuffer()).toString('base64')
  }
  const beforeOriginal = await originalBytes()
  const beforeGlossary = await work.own('GET', `/projects/${work.project.id}/glossary`)
  const beforeStats = await work.own('GET', '/stats/summary')
  const beforeActivities = await work.own('GET', '/activity?limit=100')
  assert.ok((await work.own('GET', `/jobs/${job.id}/events`)).items.length > 0)
  const databaseSnapshot = () =>
    backend.withDatabase((database) => {
      const scalar = (query) => Number(database.prepare(query).get().count)
      return {
        tm: database.prepare('SELECT * FROM tm_entries ORDER BY id').all(),
        rounds: scalar('SELECT count(*) AS count FROM job_rounds'),
        resources: scalar('SELECT count(*) AS count FROM job_resources'),
        events: scalar('SELECT count(*) AS count FROM sse_events'),
        checkpoints: scalar('SELECT count(*) AS count FROM job_round_segments'),
        credentialReferences: scalar('SELECT count(*) AS count FROM credential_job_references'),
      }
    })
  const beforeDatabase = await databaseSnapshot()
  assert.ok(beforeDatabase.tm.length > 0, 'legacy memory fixture must be present before deletion')
  assert.ok(beforeDatabase.rounds > 0 && beforeDatabase.resources > 0)
  const items = [
    target('translation', job.id, work.project),
    target('glossary_sync', task.task_id, work.project),
  ]
  const response = await backend.request('POST', '/operations/batch-delete', {
    items: [...items, items[0]],
  })
  assert.deepEqual(
    response.items,
    items.map((item) => ({ ...item, status: 'deleted' })),
  )
  await work.own('GET', `/jobs/${job.id}`, undefined, 404)
  await work.own('GET', `/jobs/${job.id}/events`, undefined, 404)
  await work.own('POST', `/jobs/${job.id}/retry`, undefined, 404)
  await work.own('GET', `/projects/${work.project.id}/sync-tasks/${task.task_id}`, undefined, 404)
  assert.deepEqual(await work.segments(), beforeSegments)
  assert.deepEqual(await work.own('GET', `/projects/${work.project.id}/glossary`), beforeGlossary)
  assert.equal(await exportArtifact.download(), exportArtifact.bytes)
  assert.equal(await originalBytes(), beforeOriginal)
  const afterStats = await work.own('GET', '/stats/summary')
  for (const key of [
    'api_calls',
    'input_tokens',
    'output_tokens',
    'usage_records',
    'segment_count',
  ])
    assert.equal(afterStats[key], beforeStats[key])
  assert.equal(afterStats.completed_jobs, beforeStats.completed_jobs - 1)
  const afterActivities = await work.own('GET', '/activity?limit=100')
  for (const activity of beforeActivities.items)
    assert.ok(afterActivities.items.some((item) => item.id === activity.id))
  const afterDatabase = await databaseSnapshot()
  assert.deepEqual(afterDatabase.tm, beforeDatabase.tm)
  for (const key of ['rounds', 'resources', 'events', 'checkpoints', 'credentialReferences'])
    assert.equal(afterDatabase[key], 0)
  const storage = (
    await work.own('GET', `/operations?state=all&project_id=${work.project.id}`)
  ).items.filter((item) => item.task_type === 'storage')
  assert.ok(storage.length > 0)
  assert.ok(storage.every((item) => item.can_delete === false && item.finished_at === null))
  await backend.request(
    'POST',
    '/operations/batch-delete',
    {
      items: [{ kind: 'storage', id: String(exportArtifact.task.id), project_id: work.project.id }],
    },
    400,
  )
  backend.recordObservation('Retained project and independent facts', {
    jobId: job.id,
    syncTaskId: task.task_id,
    tmEntries: beforeDatabase.tm.length,
    preservedExportId: exportArtifact.task.result_artifact_id,
    usageBefore: beforeStats,
    usageAfter: afterStats,
    executionRowsBefore: { ...beforeDatabase, tm: undefined },
    executionRowsAfter: { ...afterDatabase, tm: undefined },
  })
})

// Product testing is explicit: HTTP tests stay runnable without a prebuilt preview server.
// Run with LINGUAFLOW_TEST_PRODUCT_URL=http://127.0.0.1:4173 after frontend:build/preview.
test.skipIf(!process.env.LINGUAFLOW_TEST_PRODUCT_URL)(
  'H6 two real browser identities converge after deletion of translation and glossary histories',
  async ({ backend }) => {
    const owner = await backend.identity('browser-owner')
    const administrator = await backend.identity('browser-admin')
    const org = await backend.request(
      'POST',
      '/orgs',
      { name: 'Browser retention', slug: 'browser-retention' },
      201,
      owner.access_token,
    )
    await backend.request(
      'POST',
      `/orgs/${org.id}/members`,
      { username: administrator.user.username, role: 'admin' },
      201,
      owner.access_token,
    )
    const work = await backend.workflow(owner, org)
    const job = await work.readyJob()
    const { task } = await work.sync()
    const browser = await chromium.launch({ headless: true })
    const failures = []
    const observations = []
    try {
      const pages = []
      for (const identity of [owner, administrator]) {
        const context = await browser.newContext({ viewport: { width: 1440, height: 960 } })
        const page = await context.newPage()
        page.on('pageerror', (error) => failures.push(error.message))
        await context.addInitScript(
          ({ access, refresh }) => {
            localStorage.setItem('linguaflow.api_base_url', '/api/v1')
            localStorage.setItem('linguaflow.access_token', access)
            localStorage.setItem('linguaflow.refresh_token', refresh)
          },
          { access: identity.access_token, refresh: identity.refresh_token },
        )
        // Transport forwarding only. Every status and response body comes from the real
        // isolated backend; contexts hold different authenticated users and no HAR/trace.
        await page.route('**/api/v1/**', async (route) => {
          const url = new URL(route.request().url())
          try {
            const response = await route.fetch({
              url: `${backend.apiBase}${url.pathname.slice('/api/v1'.length)}${url.search}`,
              timeout: 45_000,
            })
            observations.push({
              userId: identity.user.id,
              method: route.request().method(),
              path: url.pathname,
              status: response.status(),
            })
            await route.fulfill({ response })
          } catch (error) {
            // Closing/replacing a page legitimately cancels its outstanding reads.
            if (!/closed|cancel|aborted|disposed/i.test(error.message)) failures.push(error.message)
            await route.abort().catch(() => {})
          }
        })
        pages.push(page)
      }
      const [deleting, observing] = pages
      for (const [kind, id] of [
        ['translation', String(job.id)],
        ['glossary_sync', task.task_id],
      ]) {
        const route = `/operations?state=terminal&task_type=${kind}&task_id=${id}&project_id=${work.project.id}`
        await Promise.all(
          pages.map((page) => page.goto(`${process.env.LINGUAFLOW_TEST_PRODUCT_URL}${route}`)),
        )
        for (const page of pages)
          await expect(
            page.locator('.n-drawer').getByRole('button', { name: '更多任务操作', exact: true }),
          ).toBeVisible()
        const mutationPath = `/api/v1${kind === 'translation' ? `/jobs/${id}` : `/projects/${work.project.id}/sync-tasks/${id}`}`
        const beforeDelete = observations.length
        await deleting
          .locator('.n-drawer')
          .getByRole('button', { name: '更多任务操作', exact: true })
          .click()
        await deleting.getByText('删除任务记录', { exact: true }).last().click()
        await expect(deleting.getByText('删除这条任务记录？', { exact: true })).toBeVisible()
        await deleting.getByRole('button', { name: '删除 1 条记录', exact: true }).click()
        await expect(deleting.getByText('本次已删除', { exact: true })).toBeVisible()
        await expect
          .poll(
            () =>
              observations
                .slice(beforeDelete)
                .filter(
                  (item) =>
                    item.userId === administrator.user.id &&
                    item.path === mutationPath &&
                    item.status === 404,
                ).length,
            { timeout: 45_000, intervals: [100, 250, 500, 1000] },
          )
          .toBeGreaterThan(0)
        await expect(
          observing.getByText('记录不存在，可能已被删除', { exact: false }).first(),
        ).toBeVisible()
        await expect(
          observing.locator('.n-drawer').getByRole('button', { name: '更多任务操作', exact: true }),
        ).toHaveCount(0)
        assert.equal(
          observations
            .slice(beforeDelete)
            .filter((item) => item.method === 'DELETE' && item.path === mutationPath).length,
          1,
          'one user confirmation sends exactly one destructive request',
        )
        await observing.screenshot({
          path: path.join(backend.runDir, `${kind}-second-browser.png`),
        })
      }
      assert.deepEqual(failures, [])
      await work.own('GET', `/projects/${work.project.id}`)
      backend.recordObservation('Real product two-context convergence', {
        users: [owner.user.id, administrator.user.id],
        taskKinds: ['translation', 'glossary_sync'],
        businessResponsesMocked: false,
        deletionInitiatedThroughUI: true,
        secondaryViewConvergedThroughTerminalRecheck: true,
        requests: observations.filter((item) => /\/jobs\/|\/sync-tasks\//.test(item.path)),
      })
    } finally {
      await browser.close()
    }
  },
)

test('H5 automatic retention expires both aged histories and leaves fresh history and project contents intact', async ({
  backend,
}) => {
  const work = await backend.workflow()
  const expiredJob = await work.readyJob()
  const { task } = await work.sync()
  const freshJob = await work.readyJob()
  const beforeSegments = await work.segments()
  const items = [
    target('translation', expiredJob.id, work.project),
    target('glossary_sync', task.task_id, work.project),
  ]
  await backend.age(items)
  const preview = await backend.request('POST', '/admin/task-retention/preview', {
    retention_days: 1,
  })
  assert.equal(preview.by_type.translation.expired.deletable, 1)
  assert.equal(preview.by_type.glossary_sync.expired.deletable, 1)
  const baseline = (await backend.request('GET', '/admin/settings')).settings.task_retention
  await backend.request('PATCH', '/admin/settings', {
    settings: {
      task_retention: { enabled: true, retention_days: 1, expected_revision: baseline.revision },
    },
  })
  const state = await backend.until(
    () => backend.request('GET', '/admin/task-retention/status'),
    (value) => value.last_scan?.completed && value.last_scan.deleted === 2,
    'automatic retention removes both expired task types',
    30_000,
  )
  await work.own('GET', `/jobs/${expiredJob.id}`, undefined, 404)
  await work.own('GET', `/projects/${work.project.id}/sync-tasks/${task.task_id}`, undefined, 404)
  assert.equal((await work.own('GET', `/jobs/${freshJob.id}`)).status, 'completed')
  assert.deepEqual(await work.segments(), beforeSegments)
  assert.equal(state.last_scan.policy_revision, baseline.revision + 1)
  backend.recordObservation('Automatic cleanup after isolated age seed', {
    deleted: state.last_scan.deleted,
    policyRevision: state.last_scan.policy_revision,
    freshJobId: freshJob.id,
    contentsUnchanged: true,
  })
})
