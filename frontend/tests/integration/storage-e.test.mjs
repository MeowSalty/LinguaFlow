import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { beforeAll, test as base, vi } from 'vitest'
import { buildStorageBackend, createStorageBackend } from './fixtures/storage-backend.mjs'
import {
  getStorageCapabilities,
  getStoragePolicy,
  setStoragePolicy,
  getStorageOptions,
  getProjectStorage,
  listStorageConnections,
  listStorageSpaces,
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
const identity = async (backend, name) => {
  const password = `Test-${randomUUID()}!`
  await backend.request(
    'POST',
    '/admin/users',
    {
      username: name,
      email: `${name}@example.test`,
      password,
      role: 'user',
    },
    201,
  )
  return backend.request('POST', '/auth/login', { username: name, password }, 200, null)
}
const policyRequest = (policy, changes = {}) => ({
  mode: policy.mode,
  default_choice: policy.default_choice,
  generation: policy.generation,
  logical_limit_bytes: policy.logical_limit_bytes,
  ...changes,
})

test('E-T01/E-T10 Local disabled capability discovery preserves exact subject boundaries', async ({
  backend,
}) => {
  const owner = await identity(backend, 'e-owner')
  const admin = await identity(backend, 'e-org-admin')
  const member = await identity(backend, 'e-member')
  const outsider = await identity(backend, 'e-outsider')
  const org = await backend.request(
    'POST',
    '/orgs',
    { name: 'E matrix', slug: 'e-matrix' },
    201,
    owner.access_token,
  )
  for (const [user, role] of [
    [admin, 'admin'],
    [member, 'member'],
  ])
    await backend.request(
      'POST',
      `/orgs/${org.id}/members`,
      { username: user.user.username, role },
      201,
      owner.access_token,
    )
  await backend.setDeploymentEnabled(false)
  for (const user of [owner, admin, member, outsider]) {
    const caps = await backend.request(
      'GET',
      '/storage/capabilities?scope=user',
      undefined,
      200,
      user.access_token,
    )
    assert.equal(caps.owner_id, user.user.id)
    assert.deepEqual(caps.runtime, { deployment_enabled: false, maintenance: false })
    assert.equal(caps.management_actions.create_connection.allowed, false)
    assert.deepEqual(caps.management_actions.create_connection.reason_codes, [
      'storage_deployment_disabled',
    ])
    assert.deepEqual(
      (await backend.request('GET', '/storage/connections', undefined, 200, user.access_token))
        .items,
      [],
    )
  }
  for (const user of [owner, admin]) {
    const caps = await backend.request(
      'GET',
      `/storage/capabilities?scope=org&organization_id=${org.id}`,
      undefined,
      200,
      user.access_token,
    )
    assert.equal(caps.owner_id, org.id)
    assert.equal(caps.scope, 'org')
  }
  for (const user of [member, outsider, backend.authSession]) {
    const denied = await backend.request(
      'GET',
      `/storage/capabilities?scope=org&organization_id=${org.id}`,
      undefined,
      null,
      user.access_token,
    )
    assert.ok([403, 404].includes(denied.status))
  }
  for (const query of [
    'scope=user&owner_id=1',
    'scope=user&organization_id=1',
    'scope=org&organization_id=0',
    'scope=site',
    'scope=user&scope=user',
  ])
    await backend.request(
      'GET',
      `/storage/capabilities?${query}`,
      undefined,
      400,
      owner.access_token,
    )
  const response = await fetch(`${backend.apiBase}/storage/capabilities?scope=user`, {
    headers: { Authorization: `Bearer ${owner.access_token}` },
  })
  assert.equal(response.headers.get('cache-control'), 'private, no-store')
  backend.recordObservation('E-T01/E-T10 subjects', {
    orgId: org.id,
    ownerAdminAllowed: true,
    memberOutsiderPlatformAdminDenied: true,
    disabledEmptyAccount200: true,
  })
})

for (const mode of ['both', 'user_required']) {
  test(`E-T03/E-T04 Local restart preserves ${mode}/user and rejects the complete restricted save`, async ({
    backend,
  }) => {
    const { project, space } = await backend.createStoredProject(`E policy ${mode}`)
    const initial = await getStoragePolicy()
    const saved = await setStoragePolicy(policyRequest(initial, { mode, default_choice: 'user' }))
    await backend.setDeploymentEnabled(false)
    const closed = await getStoragePolicy()
    assert.equal(closed.mode, mode)
    assert.equal(closed.default_choice, 'user')
    assert.equal(closed.generation, saved.generation)
    assert.equal(closed.runtime.deployment_enabled, false)
    assert.deepEqual(closed.allowed_policy_modes, ['site_only'])
    assert.deepEqual(closed.policy_restriction_codes, ['storage_deployment_disabled'])
    assert.equal((await getProjectStorage(project.id)).binding.space_id, space.id)
    const options = await getStorageOptions({ kind: 'user' })
    assert.equal(options.default_space_id, null)
    assert.equal(options.default_unavailable_reason, 'selection_required')
    if (mode === 'user_required') assert.ok(options.items.every((item) => !item.selectable))
    await assert.rejects(
      setStoragePolicy(
        policyRequest(closed, { logical_limit_bytes: closed.logical_limit_bytes + 1 }),
      ),
      (error) => error.status === 409 && error.error_code === 'storage_deployment_disabled',
    )
    assert.equal((await getStoragePolicy()).logical_limit_bytes, closed.logical_limit_bytes)
    const corrected = await setStoragePolicy(
      policyRequest(closed, { mode: 'site_only', default_choice: 'site' }),
    )
    assert.ok(corrected.generation > closed.generation)
    await assert.rejects(
      setStoragePolicy(policyRequest(closed, { mode: 'site_only', default_choice: 'site' })),
      (error) => error.status === 409 && error.error_code === 'storage_generation_conflict',
    )
    await backend.setDeploymentEnabled(true)
    const enabled = await getStoragePolicy()
    assert.equal(enabled.generation, corrected.generation)
    assert.equal(enabled.mode, 'site_only')
    assert.deepEqual(enabled.allowed_policy_modes, ['site_only', 'both', 'user_required'])
    assert.equal((await getProjectStorage(project.id)).binding.space_id, space.id)
    backend.recordObservation('E-T03/E-T04 policy restart and atomic save', {
      originalMode: mode,
      savedGeneration: saved.generation,
      correctedGeneration: corrected.generation,
      bindingRetained: true,
      quotaOnlyRejectedAtomically: true,
    })
  })
}

test('E-T05/E-T06 Local new writes and site space management remain available while deployment is disabled', async ({
  backend,
}) => {
  await backend.setDeploymentEnabled(false)
  const caps = await getStorageCapabilities({ kind: 'user' })
  assert.equal(caps.management_actions.create_connection.allowed, false)
  const { project, resource, space } = await backend.createStoredProject('E disabled Local write')
  assert.ok(resource.id > 0)
  const summary = await getProjectStorage(project.id)
  assert.equal(summary.runtime.deployment_enabled, false)
  assert.equal(summary.binding.space_id, space.id)
  const { items } = await listStorageConnections({ kind: 'site' })
  const local = items.find((item) => item.driver === 'local')
  for (const action of Object.values(local.management_actions)) assert.equal(action.allowed, false)
  const localSpace = (await listStorageSpaces(local.id)).items.find((item) => item.id === space.id)
  assert.equal(localSpace.management_actions.set_status.allowed, true)
  await backend.setDeploymentEnabled(true)
  assert.equal(
    (await getStorageCapabilities({ kind: 'user' })).management_actions.create_connection.allowed,
    true,
  )
  backend.recordObservation('E-T05/E-T06 Local disabled deployment', {
    projectId: project.id,
    resourceId: resource.id,
    explicitLocalWriteSucceeded: true,
    siteConnectionActionsDenied: true,
    siteSpaceStatusAllowed: true,
  })
})
