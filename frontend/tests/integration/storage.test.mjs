import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { request as httpRequest } from 'node:http'
import { beforeAll, test as base, vi } from 'vitest'
import { chromium } from '@playwright/test'
import { buildStorageBackend, createStorageBackend } from './fixtures/storage-backend.mjs'
import {
  fetchProject,
  fetchProjectResources,
  fetchResourceSegments,
  updateResourceSegment,
  downloadResourceResult,
  downloadProjectResource,
} from '@/api/projects'
import {
  getProjectStorage,
  listSourceVersions,
  previewSourceUpdate,
  commitSourceUpdate,
  getStorageTask,
  createStorageIntent,
  receiveStorageContent,
  createExportArtifact,
  listExportArtifacts,
  rebuildExportArtifact,
  downloadExportArtifact,
  getStorageDiagnostics,
  listStorageTasks,
  deleteExportArtifact,
} from '@/api/storage'

vi.mock('@/i18n', () => ({ t: (key) => key }))
let metadata
beforeAll(() => {
  metadata = buildStorageBackend()
})
const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const fixture = await createStorageBackend(metadata, signal)
    onTestFinished(() => fixture.writeReport(task))
    try {
      await fixture.start()
      await use(fixture)
    } finally {
      await fixture.close()
    }
  },
})
const currentVersion = async (project, resource) =>
  (await listSourceVersions(project.id, resource.id)).items.find((version) => version.current)
const commitPayload = (preview) => ({
  task_id: preview.task_id,
  expected_source_generation: preview.source_generation,
  expected_translation_generation: preview.translation_generation,
})
const currentResource = async (project, resource) =>
  (await fetchProjectResources(project.id)).items.find((item) => item.id === resource.id)

test('S1 administrator Local discovery, explicit project binding and read-only diagnostics', async ({
  backend,
}) => {
  const { project, resource, space, connection } =
    await backend.createStoredProject('Local discovery')
  assert.equal((await getProjectStorage(project.id)).binding?.space_id, space.id)
  const version = await currentVersion(project, resource)
  assert.ok(version)
  assert.equal(version.verification_state, 'verified')
  const tasksBefore = await listStorageTasks(project.id)
  const stateBefore = await fetchProject(project.id)
  await getStorageDiagnostics()
  await getStorageDiagnostics({ project_id: project.id })
  assert.deepEqual(await listStorageTasks(project.id), tasksBefore)
  assert.deepEqual(await fetchProject(project.id), stateBefore)
  assert.equal((await currentVersion(project, resource)).id, version.id)
  backend.recordObservation('Local discovery and diagnostics', {
    connectionId: connection.id,
    spaceId: space.id,
    projectId: project.id,
    sourceVersionId: version.id,
    noNewTasksOrProjectChanges: true,
  })
})

test('S2 source preview stays unpublished, stale generations conflict, original task recovers commit', async ({
  backend,
}) => {
  const { project, resource } = await backend.createStoredProject('Source update')
  const original = await currentVersion(project, resource)
  const candidate = new File(
    [JSON.stringify({ first: 'Updated first source', second: 'Second source line' })],
    'source.json',
  )
  const preview = await previewSourceUpdate(project.id, resource.id, candidate, randomUUID())
  assert.equal(
    (await currentVersion(project, resource)).id,
    original.id,
    'preview must not publish the candidate',
  )
  const prepared = await getStorageTask(project.id, preview.task_id)
  const segments = await fetchResourceSegments(project.id, resource.id)
  await updateResourceSegment(project.id, resource.id, segments.items[0].id, {
    target_text: '保存于预览之后',
  })
  await assert.rejects(
    commitSourceUpdate(project.id, resource.id, commitPayload(preview)),
    (error) => error.status === 409,
  )
  assert.equal(
    (await currentVersion(project, resource)).id,
    original.id,
    'conflicted commit must not publish',
  )
  const nextPreview = await previewSourceUpdate(project.id, resource.id, candidate, randomUUID())
  const lostResponse = await backend.dropCommitResponse(
    project.id,
    resource.id,
    commitPayload(nextPreview),
  )
  // The TCP response was cut after the backend committed: recover through the original task ID.
  const recovered = await getStorageTask(project.id, nextPreview.task_id)
  assert.equal(recovered.id, nextPreview.task_id)
  const updated = await currentVersion(project, resource)
  assert.notEqual(updated.id, original.id)
  assert.equal((await currentResource(project, resource)).id, resource.id)
  backend.recordObservation('Preview and commit', {
    preparedStatus: prepared.status,
    preparedPhase: prepared.phase,
    originalVersionId: original.id,
    publishedVersionId: updated.id,
    recoveredTaskId: recovered.id,
    actualNetworkResponseLoss: true,
    ...lostResponse,
  })
})

test('S3 same-byte repair preserves source and resource identity', async ({ backend }) => {
  const { project, resource, source, space } = await backend.createStoredProject('Same byte repair')
  const version = await currentVersion(project, resource)
  const latest = await fetchProject(project.id)
  const bytes = new Blob([source])
  const intent = await createStorageIntent(project.id, {
    kind: 'repair',
    idempotency_key: randomUUID(),
    resource_id: resource.id,
    source_revision_id: version.id,
    target_space_id: space.id,
    size: bytes.size,
    storage_generation: latest.storage_generation,
    location_generation: version.location_generation,
  })
  await receiveStorageContent(project.id, intent.id, bytes)
  const repaired = await backend.terminalTask({ projectId: project.id, taskId: intent.id })
  assert.equal(repaired.status, 'completed')
  const after = await currentVersion(project, resource)
  assert.equal(after.id, version.id)
  assert.equal(after.sha256, version.sha256)
  assert.equal((await currentResource(project, resource)).id, resource.id)
  assert.equal((await listSourceVersions(project.id, resource.id)).items.length, 1)
  backend.recordObservation('Same-byte repair', {
    sourceVersionId: after.id,
    resourceId: resource.id,
    taskId: repaired.id,
    originalObjectWasAvailable: true,
  })
})

test('S4 ready export retains its snapshot, rebuild gets a new identity, ordinary download uses current translation', async ({
  backend,
}) => {
  const { project, resource } = await backend.createStoredProject('Fixed delivery')
  const segments = await fetchResourceSegments(project.id, resource.id)
  const jobsBefore = await backend.request('GET', `/projects/${project.id}/jobs`)
  for (const segment of segments.items)
    await updateResourceSegment(project.id, resource.id, segment.id, {
      target_text: `译文-${segment.id}`,
    })
  const before = await downloadResourceResult(project.id, resource.id)
  const beforeText = await before.blob.text()
  const creation = await createExportArtifact(project.id, resource.id, randomUUID())
  const created = await backend.terminalTask({ projectId: project.id, taskId: creation.id })
  assert.equal(created.status, 'completed')
  assert.ok(created.result_artifact_id)
  const artifact = (await listExportArtifacts(project.id, resource.id)).items.find(
    (item) => item.id === created.result_artifact_id,
  )
  assert.equal(artifact.status, 'ready')
  assert.equal(
    await (await downloadExportArtifact(project.id, artifact.id)).blob.text(),
    beforeText,
  )
  await updateResourceSegment(project.id, resource.id, segments.items[0].id, {
    target_text: '后续保存的新译文',
  })
  const current = await downloadResourceResult(project.id, resource.id)
  assert.notEqual(await current.blob.text(), beforeText)
  assert.equal(
    await (await downloadExportArtifact(project.id, artifact.id)).blob.text(),
    beforeText,
  )
  const rebuilding = await rebuildExportArtifact(project.id, artifact.id, randomUUID())
  const rebuilt = await backend.terminalTask({ projectId: project.id, taskId: rebuilding.id })
  assert.equal(rebuilt.status, 'completed')
  assert.notEqual(rebuilt.result_artifact_id, artifact.id)
  assert.equal(
    await (await downloadExportArtifact(project.id, rebuilt.result_artifact_id)).blob.text(),
    beforeText,
  )
  assert.deepEqual(await backend.request('GET', `/projects/${project.id}/jobs`), jobsBefore)
  backend.recordObservation('Fixed delivery snapshots', {
    originalArtifactId: artifact.id,
    rebuiltArtifactId: rebuilt.result_artifact_id,
    immutableAfterTranslationWrite: true,
    noAdditionalTranslationJob: true,
  })
})

test('S5 real Chromium sends File and Blob bytes without setting Content-Length', async ({
  backend,
}) => {
  const { project, resource, source, space } = await backend.createStoredProject('Browser binary')
  const original = await currentVersion(project, resource)
  const latest = await fetchProject(project.id)
  const intent = await createStorageIntent(project.id, {
    kind: 'repair',
    idempotency_key: randomUUID(),
    resource_id: resource.id,
    source_revision_id: original.id,
    target_space_id: space.id,
    size: new Blob([source]).size,
    storage_generation: latest.storage_generation,
    location_generation: original.location_generation,
  })
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    await page.goto(`${backend.apiBase}/ping`)
    const result = await page.evaluate(
      async ({ apiBase, accessToken, projectId, resourceId, taskId, source, key }) => {
        const headers = new Headers({
          Authorization: `Bearer ${accessToken}`,
          'Content-Type': 'application/octet-stream',
          'Idempotency-Key': key,
        })
        const file = new File([source], 'source.json')
        const previewResponse = await fetch(
          `${apiBase}/projects/${projectId}/resources/${resourceId}/source-preview`,
          { method: 'POST', headers, body: file },
        )
        const preview = await previewResponse.json()
        headers.delete('Idempotency-Key')
        const contentResponse = await fetch(
          `${apiBase}/projects/${projectId}/storage/tasks/${taskId}/content`,
          { method: 'PUT', headers, body: new Blob([source]) },
        )
        return {
          previewStatus: previewResponse.status,
          taskId: preview.task_id,
          contentStatus: contentResponse.status,
          manuallySetLength: headers.has('Content-Length'),
          byteLength: file.size,
        }
      },
      {
        apiBase: backend.apiBase,
        accessToken: backend.authSession.access_token,
        projectId: project.id,
        resourceId: resource.id,
        taskId: intent.id,
        source,
        key: randomUUID(),
      },
    )
    assert.equal(result.previewStatus, 200)
    assert.equal(result.contentStatus, 202)
    assert.equal(result.manuallySetLength, false)
    assert.ok(result.taskId)
    assert.equal((await currentVersion(project, resource)).id, original.id)
    backend.recordObservation('Chromium File and Blob transport', result)
  } finally {
    await browser.close()
  }
})

const createIdentity = async (backend, suffix) => {
  const username = `storage-${suffix}`
  const password = `Test-${randomUUID()}!`
  await backend.request(
    'POST',
    '/admin/users',
    { username, email: `${username}@example.test`, password, role: 'user' },
    201,
  )
  return backend.request('POST', '/auth/login', { username, password }, 200, null)
}
const uploadAs = async (backend, projectId, token, source) => {
  const body = new FormData()
  body.append('files', new Blob([source], { type: 'application/json' }), 'source.json')
  const response = await fetch(`${backend.apiBase}/projects/${projectId}/resources`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
    body,
  })
  assert.equal(response.status, 200)
  const result = await response.json()
  assert.equal(result.items[0].action, 'created')
  return result.items[0].resource
}

test('S6 ordinary owner uses the real product drawer to preview and publish a File on Local storage', async ({
  backend,
}) => {
  const identity = await createIdentity(backend, 'browser-owner')
  const token = identity.access_token
  const options = await backend.request('GET', '/storage/options?scope=user', undefined, 200, token)
  const target = options.items.find((item) => item.selectable)
  assert.ok(target)
  const project = await backend.request(
    'POST',
    '/projects',
    {
      name: 'Ordinary product source update',
      source_lang: 'en',
      target_lang: 'zh',
      storage_space_id: target.space_id,
    },
    201,
    token,
  )
  const resource = await uploadAs(
    backend,
    project.id,
    token,
    JSON.stringify({ first: 'Original first', second: 'Second source' }),
  )
  const browser = await chromium.launch({ headless: true })
  try {
    const page = await browser.newPage()
    const errors = []
    page.on('pageerror', (error) => errors.push(error.message))
    await page.addInitScript(
      ({ access, refresh }) => {
        localStorage.setItem('linguaflow.api_base_url', '/api/v1')
        localStorage.setItem('linguaflow.access_token', access)
        localStorage.setItem('linguaflow.refresh_token', refresh)
      },
      { access: token, refresh: identity.refresh_token },
    )
    // Same-origin transport proxy only: all response bytes/statuses come from the isolated real server.
    await page.route('**/api/v1/**', async (route) => {
      const url = new URL(route.request().url())
      const response = await route.fetch({
        url: `${backend.apiBase}${url.pathname.slice('/api/v1'.length)}${url.search}`,
      })
      await route.fulfill({ response })
    })
    await page.goto(`http://127.0.0.1:4173/projects/${project.id}`)
    await page
      .getByRole('button', { name: '查看段落：source.json', exact: true })
      .locator('button')
      .click()
    await page.getByText('更新源文件', { exact: true }).click()
    const chooser = page.waitForEvent('filechooser')
    await page.getByRole('button', { name: '选择源文件', exact: true }).click()
    await (
      await chooser
    ).setFiles({
      name: 'source.json',
      mimeType: 'application/json',
      buffer: Buffer.from(
        JSON.stringify({ first: 'Updated first', second: 'Second source', third: 'New third' }),
      ),
    })
    await page.getByRole('button', { name: '预览变化', exact: true }).click()
    await page.getByText('预览候选已准备，尚未发布。', { exact: true }).waitFor()
    await page.getByText('新增段落', { exact: true }).waitFor()
    const before = await backend.request(
      'GET',
      `/projects/${project.id}/resources`,
      undefined,
      200,
      token,
    )
    assert.equal(before.items[0].current_source_revision_id, resource.current_source_revision_id)
    await page.getByRole('button', { name: '确认更新', exact: true }).click()
    // A synchronous commit may return running; use the product's original-task recovery button.
    await backend.until(
      async () => {
        if (await page.getByText('源文件已更新，视图已刷新。', { exact: true }).isVisible())
          return true
        const recovery = page.getByRole('button', { name: '恢复原任务结果', exact: true })
        if (await recovery.isEnabled()) await recovery.click()
        return false
      },
      (value) => value,
      'product publication confirmation',
    )
    const after = await backend.request(
      'GET',
      `/projects/${project.id}/resources`,
      undefined,
      200,
      token,
    )
    assert.notEqual(after.items[0].current_source_revision_id, resource.current_source_revision_id)
    assert.equal(after.items[0].id, resource.id)
    assert.deepEqual(errors, [])
    backend.recordObservation('Real product Local source publication', {
      ordinaryUser: true,
      projectId: project.id,
      resourceId: resource.id,
      publishedRevisionId: after.items[0].current_source_revision_id,
      realBrowserFile: true,
      transportProxyOnly: true,
      mockedResponses: false,
    })
  } finally {
    await browser.close()
  }
})

test('S7 ordinary user and organization owner/admin/member storage authorization matrix', async ({
  backend,
}) => {
  const owner = await createIdentity(backend, 'owner')
  const admin = await createIdentity(backend, 'org-admin')
  const member = await createIdentity(backend, 'member')
  const outsider = await createIdentity(backend, 'outsider')
  const org = await backend.request(
    'POST',
    '/orgs',
    { name: 'Storage role matrix', slug: 'storage-role-matrix' },
    201,
    owner.access_token,
  )
  for (const [identity, role] of [
    [admin, 'admin'],
    [member, 'member'],
  ])
    await backend.request(
      'POST',
      `/orgs/${org.id}/members`,
      { username: identity.user.username, role },
      201,
      owner.access_token,
    )
  const options = await backend.request(
    'GET',
    `/storage/options?scope=org&organization_id=${org.id}`,
    undefined,
    200,
    owner.access_token,
  )
  const target = options.items.find((item) => item.selectable)
  assert.ok(target)
  const project = await backend.request(
    'POST',
    `/orgs/${org.id}/projects`,
    {
      name: 'Role matrix project',
      storage_space_id: target.space_id,
      source_lang: 'en',
      target_lang: 'zh',
    },
    201,
    owner.access_token,
  )
  const resource = await uploadAs(
    backend,
    project.id,
    owner.access_token,
    JSON.stringify({ first: 'Role test' }),
  )
  const projectPath = `/projects/${project.id}`
  for (const identity of [owner, admin]) {
    await backend.request(
      'GET',
      `${projectPath}/storage/options?purpose=bind`,
      undefined,
      200,
      identity.access_token,
    )
    await backend.request(
      'GET',
      `/storage/options?scope=org&organization_id=${org.id}`,
      undefined,
      200,
      identity.access_token,
    )
  }
  const summary = await backend.request(
    'GET',
    `${projectPath}/storage`,
    undefined,
    200,
    member.access_token,
  )
  assert.equal(summary.project_id, project.id)
  assert.equal('reserved_bytes' in summary, false)
  assert.equal('endpoint' in (summary.binding ?? {}), false)
  for (const identity of [member, outsider, backend.authSession]) {
    for (const path of [
      `/storage/options?scope=org&organization_id=${org.id}`,
      `${projectPath}/storage/options?purpose=bind`,
    ]) {
      const denied = await backend.request('GET', path, undefined, null, identity.access_token)
      assert.ok(
        [403, 404].includes(denied.status),
        `${identity.user.role}/${path}: ${denied.status}`,
      )
    }
  }
  const deniedPreview = await fetch(
    `${backend.apiBase}${projectPath}/resources/${resource.id}/source-preview`,
    {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${member.access_token}`,
        'Content-Type': 'application/octet-stream',
        'Idempotency-Key': randomUUID(),
      },
      body: new Blob(['{}']),
    },
  )
  assert.equal(deniedPreview.status, 403)
  const management = await backend.request(
    'GET',
    `/orgs/${org.id}/storage/connections`,
    undefined,
    null,
    member.access_token,
  )
  assert.equal(management.status, 403)
  const userOptions = await backend.request(
    'GET',
    '/storage/options?scope=user',
    undefined,
    200,
    outsider.access_token,
  )
  assert.ok(userOptions.items.some((item) => item.selectable))
  backend.recordObservation('Storage authorization matrix', {
    orgId: org.id,
    ownerAndAdminDiscovery: true,
    memberSafeSummary: true,
    memberWriteAndManagementDenied: true,
    outsiderDenied: true,
    platformAdminDoesNotBypassOrgMembership: true,
    ordinaryUserDiscovery: true,
  })
})

test('S8 real HTTP rejects chunked unknown length with 411 and oversized declared payload with 413', async ({
  backend,
}) => {
  const { project, resource } = await backend.createStoredProject('Transfer limits')
  const send = (extraHeaders, writeChunk) =>
    new Promise((resolve, reject) => {
      const request = httpRequest(
        `${backend.apiBase}/projects/${project.id}/resources/${resource.id}/source-preview`,
        {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${backend.authSession.access_token}`,
            'Content-Type': 'application/octet-stream',
            'Idempotency-Key': randomUUID(),
            ...extraHeaders,
          },
        },
        (response) => {
          const chunks = []
          response.on('data', (chunk) => chunks.push(chunk))
          response.on('end', () =>
            resolve({
              status: response.statusCode,
              body: JSON.parse(Buffer.concat(chunks).toString()),
            }),
          )
          response.on('error', reject)
        },
      )
      request.on('error', reject)
      request.setTimeout(10000, () => request.destroy(new Error('Transfer validation timeout')))
      if (writeChunk) request.write('{}')
      request.end()
    })
  const chunked = await send({ 'Transfer-Encoding': 'chunked' }, true)
  assert.equal(chunked.status, 411)
  assert.equal(chunked.body.error_code, 'content_length_required')
  const oversized = await send({ 'Content-Length': String(100 * 1024 * 1024 + 1) }, false)
  assert.equal(oversized.status, 413)
  assert.equal(oversized.body.error_code, 'storage_payload_too_large')
  backend.recordObservation('Real HTTP transfer rejection', {
    chunkedStatus: chunked.status,
    oversizedDeclaredLengthStatus: oversized.status,
    noLargePayloadAllocated: true,
  })
})

test('S9 missing actual Local original rejects different-byte repair and restores identical bytes without changing revisions', async ({
  backend,
}) => {
  const { project, resource, source, space } =
    await backend.createStoredProject('Missing original repair')
  const original = await currentVersion(project, resource)
  const generationsBefore = await currentResource(project, resource)
  const segmentsBefore = await fetchResourceSegments(project.id, resource.id)
  const fault = await backend.removeOriginalObject(source)
  await assert.rejects(downloadProjectResource(project.id, resource.id))
  const createRepair = async (bytes) => {
    const current = await currentVersion(project, resource)
    const latest = await fetchProject(project.id)
    return createStorageIntent(project.id, {
      kind: 'repair',
      idempotency_key: randomUUID(),
      resource_id: resource.id,
      source_revision_id: original.id,
      target_space_id: space.id,
      size: bytes.size,
      storage_generation: latest.storage_generation,
      location_generation: current.location_generation,
    })
  }
  const wrong = new Blob([source.replace('First', 'Wrong')])
  assert.equal(wrong.size, new Blob([source]).size, 'same size must not count as identical bytes')
  const rejected = await createRepair(wrong)
  await assert.rejects(
    receiveStorageContent(project.id, rejected.id, wrong),
    (error) =>
      error.error_code === 'repair_content_mismatch' ||
      error.problem?.error_code === 'repair_content_mismatch',
  )
  const rejectedTask = await getStorageTask(project.id, rejected.id)
  assert.equal(rejectedTask.error_code, 'repair_content_mismatch')
  assert.equal((await currentVersion(project, resource)).id, original.id)
  const bytes = new Blob([source])
  const repair = await createRepair(bytes)
  await receiveStorageContent(project.id, repair.id, bytes)
  const completed = await backend.terminalTask({ projectId: project.id, taskId: repair.id })
  assert.equal(completed.status, 'completed')
  assert.equal(completed.phase, 'committed')
  assert.equal(await (await downloadProjectResource(project.id, resource.id)).blob.text(), source)
  const after = await currentResource(project, resource)
  assert.equal(after.current_source_revision_id, original.id)
  assert.equal(after.source_generation, generationsBefore.source_generation)
  assert.equal(after.translation_generation, generationsBefore.translation_generation)
  assert.deepEqual(await fetchResourceSegments(project.id, resource.id), segmentsBefore)
  assert.equal((await listSourceVersions(project.id, resource.id)).items.length, 1)
  backend.recordObservation('Actual missing Local original repaired', {
    ...fault,
    wrongSameSizeBytesRejected: true,
    repairTaskId: repair.id,
    unchangedSourceRevisionId: original.id,
    sameGenerationsAndSegments: true,
    restoredDownloadMatchesExactly: true,
  })
})

test('S10 export deletion exposes a stable tombstone task, reaches cleanup done and preserves a shared rebuilt delivery', async ({
  backend,
}) => {
  await backend.enablePromptCleanup()
  const { project, resource } = await backend.createStoredProject('Export cleanup')
  const segments = await fetchResourceSegments(project.id, resource.id)
  for (const segment of segments.items)
    await updateResourceSegment(project.id, resource.id, segment.id, {
      target_text: `固定译文-${segment.id}`,
    })
  const creating = await createExportArtifact(project.id, resource.id, randomUUID())
  const original = await backend.terminalTask({ projectId: project.id, taskId: creating.id })
  assert.equal(original.phase, 'committed')
  const artifactId = original.result_artifact_id
  assert.ok(artifactId)
  const text = await (await downloadExportArtifact(project.id, artifactId)).blob.text()
  const rebuilding = await rebuildExportArtifact(project.id, artifactId, randomUUID())
  const rebuilt = await backend.terminalTask({ projectId: project.id, taskId: rebuilding.id })
  assert.equal(rebuilt.phase, 'committed')
  assert.notEqual(rebuilt.result_artifact_id, artifactId)
  await deleteExportArtifact(project.id, artifactId)
  const tombstones = await listExportArtifacts(project.id, resource.id, { includeDeleted: true })
  const tombstone = tombstones.items.find((item) => item.id === artifactId)
  assert.equal(tombstone.status, 'deleted')
  assert.ok(tombstone.deletion_task_id)
  assert.equal(
    (await listExportArtifacts(project.id, resource.id)).items.some(
      (item) => item.id === artifactId,
    ),
    false,
  )
  await deleteExportArtifact(project.id, artifactId)
  const repeated = (
    await listExportArtifacts(project.id, resource.id, { includeDeleted: true })
  ).items.find((item) => item.id === artifactId)
  assert.equal(repeated.deletion_task_id, tombstone.deletion_task_id)
  const cleanupStates = []
  const deletion = await backend.until(
    async () => {
      const task = await getStorageTask(project.id, tombstone.deletion_task_id)
      cleanupStates.push(task.cleanup_status)
      return task
    },
    (task) => task.cleanup_status === 'done',
    'Local export physical cleanup',
  )
  assert.equal(deletion.kind, 'export_delete')
  assert.equal(deletion.status, 'completed')
  assert.equal(
    await (await downloadExportArtifact(project.id, rebuilt.result_artifact_id)).blob.text(),
    text,
  )
  backend.recordObservation('Local deletion and shared snapshot protection', {
    originalArtifactId: artifactId,
    rebuiltArtifactId: rebuilt.result_artifact_id,
    deletionTaskId: tombstone.deletion_task_id,
    stableTombstone: true,
    cleanupStates,
    finalCleanupStatus: deletion.cleanup_status,
    rebuiltDownloadSurvivesOriginalDeletion: true,
    isolatedDeletionGrace: '1ms',
  })
})
