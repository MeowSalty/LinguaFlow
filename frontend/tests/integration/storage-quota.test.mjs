import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { beforeAll, test as base, vi } from 'vitest'
import { buildStorageBackend, createStorageBackend } from './fixtures/storage-backend.mjs'
import {
  getStoragePolicy,
  setStoragePolicy,
  listStorageSpaces,
  setStorageSpaceQuota,
  setStorageSpaceState,
  getStorageDiagnostics,
  listStorageTasks,
} from '@/api/storage'
import { uploadProjectResources, downloadProjectResource } from '@/api/projects'

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
const policyBody = (policy, changes = {}) => ({
  mode: policy.mode,
  default_choice: policy.default_choice,
  generation: policy.generation,
  logical_limit_bytes: policy.logical_limit_bytes,
  default_space_capacity_bytes: policy.default_space_capacity_bytes,
  ...changes,
})
const accounted = (space) =>
  space.reserved_bytes + space.candidate_bytes + space.live_bytes + space.pending_delete_bytes
const currentSpace = async (connection, space) =>
  (await listStorageSpaces(connection.id)).items.find((item) => item.id === space.id)

for (const logical of [null, 1])
  for (const capacity of [null, 1]) {
    test(`QF-T02 real Local quota combination logical=${logical} space=${capacity}`, async ({
      backend,
    }) => {
      const { project, space, connection } = await backend.createStoredProject(
        `Quota ${logical}/${capacity}`,
      )
      const policy = await getStoragePolicy()
      await setStoragePolicy(policyBody(policy, { logical_limit_bytes: logical }))
      const before = await currentSpace(connection, space)
      const changed = await setStorageSpaceQuota(space.id, {
        capacity_bytes: capacity,
        expected_generation: before.management_generation,
      })
      assert.equal(changed.capacity_bytes, capacity)
      assert.equal(changed.available_bytes, capacity === null ? null : 0)
      assert.equal(accounted(changed), accounted(before))
      const payload = JSON.stringify({ message: `Distinct quota content ${randomUUID()}` })
      const result = await uploadProjectResources(
        project.id,
        [new File([payload], 'new.json')],
        ['new.json'],
        undefined,
        { idempotencyKey: randomUUID() },
      ).catch((error) => ({ error_code: error.error_code }))
      if (logical === null && capacity === null) assert.equal(result.items?.[0]?.action, 'created')
      else
        assert.equal(result.error_code ?? result.items?.[0]?.error_code, 'storage_quota_exceeded')
      backend.recordObservation('QF-T02 quota admission', {
        logical,
        capacity,
        outcome: result.error_code ?? result.items?.[0]?.action,
        accountedRetained: true,
      })
    })
  }

test('QF-T04/T05/T06 Local default isolation, lowering, shared status generation and stale CAS', async ({
  backend,
}) => {
  const { project, resource, space, connection } = await backend.createStoredProject('Quota CAS')
  const policy = await getStoragePolicy()
  const before = await currentSpace(connection, space)
  await setStoragePolicy(
    policyBody(policy, { logical_limit_bytes: null, default_space_capacity_bytes: 2048 }),
  )
  assert.equal((await currentSpace(connection, space)).capacity_bytes, before.capacity_bytes)
  const lowered = await setStorageSpaceQuota(space.id, {
    capacity_bytes: 1,
    expected_generation: before.management_generation,
  })
  assert.equal(accounted(lowered), accounted(before))
  assert.equal(lowered.available_bytes, 0)
  assert.ok(lowered.management_generation > before.management_generation)
  await assert.rejects(
    setStorageSpaceQuota(space.id, {
      capacity_bytes: null,
      expected_generation: before.management_generation,
    }),
    (error) => error.status === 409 && error.error_code === 'storage_generation_conflict',
  )
  const readOnly = await setStorageSpaceState(space.id, {
    status: 'read_only',
    expected_generation: lowered.management_generation,
  })
  await assert.rejects(
    setStorageSpaceQuota(space.id, {
      capacity_bytes: null,
      expected_generation: lowered.management_generation,
    }),
    (error) => error.status === 409,
  )
  const unlimited = await setStorageSpaceQuota(space.id, {
    capacity_bytes: null,
    expected_generation: readOnly.management_generation,
  })
  assert.equal(unlimited.capacity_bytes, null)
  assert.equal(unlimited.available_bytes, null)
  assert.equal(unlimited.status, 'read_only')
  await downloadProjectResource(project.id, resource.id)
  backend.recordObservation('QF-T04/T05/T06 default and CAS', {
    defaultDidNotRewriteSpace: true,
    loweringRetainedAccounted: accounted(lowered),
    generations: [
      before.management_generation,
      lowered.management_generation,
      readOnly.management_generation,
      unlimited.management_generation,
    ],
    directReadPreserved: true,
  })
})

test('QF-T07/T08 Local quota permission and safe read-only disk diagnostics', async ({
  backend,
}) => {
  const { project, space, connection } = await backend.createStoredProject('Quota access')
  const password = `Test-${randomUUID()}!`
  await backend.request(
    'POST',
    '/admin/users',
    { username: 'quota-outsider', email: 'quota-outsider@example.test', password, role: 'user' },
    201,
  )
  const user = await backend.request(
    'POST',
    '/auth/login',
    { username: 'quota-outsider', password },
    200,
    null,
  )
  const snapshot = await currentSpace(connection, space)
  const denied = await backend.request(
    'PUT',
    `/storage/spaces/${space.id}/quota`,
    { capacity_bytes: null, expected_generation: snapshot.management_generation },
    null,
    user.access_token,
  )
  assert.ok([403, 404].includes(denied.status))
  const tasks = await listStorageTasks(project.id)
  const diagnostics = await getStorageDiagnostics()
  assert.ok(Array.isArray(diagnostics.disks))
  for (const disk of diagnostics.disks) {
    assert.deepEqual(
      Object.keys(disk).sort(),
      [
        'roles',
        'state',
        'observed_at',
        'total_bytes',
        'available_bytes',
        'minimum_free_bytes',
      ].sort(),
    )
    assert.ok(['available', 'low', 'unknown'].includes(disk.state))
    assert.ok(Array.isArray(disk.roles))
  }
  assert.deepEqual(await listStorageTasks(project.id), tasks)
  const privateRead = await backend.request(
    'GET',
    '/admin/storage/diagnostics',
    undefined,
    null,
    user.access_token,
  )
  assert.equal(privateRead.status, 403)
  backend.recordObservation('QF-T07/T08 Local permissions and diagnostics', {
    quotaDenied: denied.status,
    diagnosticsDenied: privateRead.status,
    diskObservations: diagnostics.disks.length,
    noNewTasks: true,
  })
})
