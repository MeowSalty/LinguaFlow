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
    const backend = await createConfigurationBackend(metadata, signal)
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

test('A1/A6 boolean registration and real owner/admin/member authorization', async ({
  backend: fixture,
}) => {
  const { request, endpoint, tokenSecrets } = fixture
  const password = 'Integration-member-test-password!'
  assert.equal((await request('GET', '/admin/settings')).settings.registration_enabled, false)
  const register = { username: 'integration-member', email: 'member@example.test', password }
  await request('POST', '/auth/register', register, 403, null)
  await request('PATCH', '/admin/settings', { settings: { registration_enabled: true } })
  const member = await request('POST', '/auth/register', register, 201, null)
  assert.equal(member.user.role, 'user')
  const org = await request(
    'POST',
    '/orgs',
    { name: 'Integration organization', slug: 'configuration-integration' },
    201,
  )
  await request(
    'POST',
    `/orgs/${org.id}/members`,
    { username: register.username, role: 'member' },
    201,
  )
  await request('GET', `/orgs/${org.id}/credentials`, undefined, 403, member.access_token)
  await request('PUT', `/orgs/${org.id}/members/${member.user.id}`, { role: 'admin' })
  await request('GET', `/orgs/${org.id}/credentials`, undefined, 200, member.access_token)
  const orgCredential = await request(
    'POST',
    `/orgs/${org.id}/credentials`,
    { provider: 'openai', endpoint, secret: tokenSecrets[0] },
    201,
    member.access_token,
  )
  await request(
    'GET',
    `/credentials/${orgCredential.id}/versions`,
    undefined,
    200,
    member.access_token,
  )
  await request(
    'POST',
    `/credentials/${orgCredential.id}/versions`,
    { secret: tokenSecrets[1] },
    201,
    member.access_token,
  )
  await request(
    'POST',
    `/credentials/${orgCredential.id}/versions/1/revoke`,
    undefined,
    204,
    member.access_token,
  )
  assert.equal(
    (
      await request(
        'POST',
        `/credentials/${orgCredential.id}/collect`,
        undefined,
        200,
        member.access_token,
      )
    ).deleted_versions,
    1,
  )
})

test('A7 recursive profile merge preserves false/zero/empty arrays', async ({ backend }) => {
  const { request, makeProfile } = backend
  const profile = await makeProfile()
  await request('PUT', `/execution-profiles/${profile.id}`, {
    config: { schema_version: 1, postprocess: { trim_spaces: false } },
  })
  const saved = await request('GET', `/execution-profiles/${profile.id}`)
  assert.equal(saved.config.context.enabled, false)
  assert.equal(saved.config.context.before, 0)
  assert.deepEqual(saved.config.ruby.preserve_kinds, [])
  assert.deepEqual(saved.config.qa.checks, [])
  assert.equal(saved.config.postprocess.trim_spaces, false)
})

test('A1/A9 restart preserves registration policy and paused task snapshot', async ({
  backend,
}) => {
  const { request, makeWorkflow, pausedJob, getJob, restart, terminalJob } = backend
  const { project, plan } = await makeWorkflow()
  const job = await pausedJob(project, plan, 'fixture-model')
  const snapshot = (await getJob(job.id)).execution_config
  await request('PATCH', '/admin/settings', { settings: { registration_enabled: true } })
  await restart()
  assert.equal((await request('GET', '/admin/settings')).settings.registration_enabled, true)
  assert.equal((await getJob(job.id)).status, 'paused')
  assert.deepEqual((await getJob(job.id)).execution_config, snapshot)
  await request('POST', `/jobs/${job.id}/resume`)
  assert.equal((await terminalJob(job.id)).status, 'completed')
})

test('A9 rotation freezes accepted task versions and snapshots while new jobs use new bindings', async ({
  backend: fixture,
}) => {
  const {
    request,
    makeWorkflow,
    makePlan,
    backendBody,
    endpoint,
    tokenSecrets,
    pausedJob,
    getJob,
    calls,
    startJob,
    terminalJob,
  } = fixture
  const { credential, backend, profile, project, plan } = await makeWorkflow('frozen-model')
  const second = await request(
    'POST',
    '/backends',
    backendBody('Shared backend', 'second-model', endpoint, credential.id),
    201,
  )
  const secondPlan = await makePlan('Shared plan', second.id, profile.id)
  const oldJob = await pausedJob(project, plan, 'frozen-model')
  const snapshot = (await getJob(oldJob.id)).execution_config
  await request('POST', `/credentials/${credential.id}/versions`, { secret: tokenSecrets[1] }, 201)
  await request(
    'PUT',
    `/backends/${backend.id}`,
    backendBody('Updated backend', 'updated-model', endpoint),
  )
  await request('PUT', `/execution-profiles/${profile.id}`, { config: { context: { before: 2 } } })
  assert.deepEqual((await getJob(oldJob.id)).execution_config, snapshot)
  const beforeResume = calls.length
  await request('POST', `/jobs/${oldJob.id}/resume`)
  assert.equal((await terminalJob(oldJob.id)).status, 'completed')
  assert.ok(calls.length > beforeResume)
  assert.ok(
    calls
      .slice(beforeResume)
      .every((call) => call.secretVersion === 1 && call.model === 'frozen-model'),
  )
  for (const [nextPlan, model] of [
    [plan, 'updated-model'],
    [secondPlan, 'second-model'],
  ]) {
    const before = calls.length
    const job = await startJob(project, nextPlan)
    assert.equal((await terminalJob(job.id)).status, 'completed')
    assert.ok(calls.length > before)
    assert.ok(calls.slice(before).every((call) => call.secretVersion === 2 && call.model === model))
  }
  assert.equal((await request('POST', `/credentials/${credential.id}/collect`)).deleted_versions, 0)
})

test('A9 revocation prevents paused execution recovery without rebinding', async ({ backend }) => {
  const { request, makeWorkflow, pausedJob, expectRecoveryBlocked, tokenSecrets } = backend
  const { credential, project, plan } = await makeWorkflow()
  const job = await pausedJob(project, plan, 'fixture-model')
  await request('POST', `/credentials/${credential.id}/versions`, { secret: tokenSecrets[1] }, 201)
  await request('POST', `/credentials/${credential.id}/versions/1/revoke`, undefined, 204)
  await expectRecoveryBlocked(job, 'credential version has been revoked')
})

test('A9 Backend deletion blocks recovery and preserves other shared bindings', async ({
  backend: fixture,
}) => {
  const {
    request,
    makeWorkflow,
    backendBody,
    endpoint,
    makePlan,
    pausedJob,
    expectRecoveryBlocked,
    calls,
    startJob,
    terminalJob,
  } = fixture
  const { credential, backend, profile, project, plan } = await makeWorkflow('deleted-model')
  const shared = await request(
    'POST',
    '/backends',
    backendBody('Surviving backend', 'shared-model', endpoint, credential.id),
    201,
  )
  const sharedPlan = await makePlan('Surviving plan', shared.id, profile.id)
  const job = await pausedJob(project, plan, 'deleted-model')
  await request('DELETE', `/backends/${backend.id}`, undefined, 204)
  await expectRecoveryBlocked(job, 'backend no longer exists')
  const before = calls.length
  const sharedJob = await startJob(project, sharedPlan)
  assert.equal((await terminalJob(sharedJob.id)).status, 'completed')
  assert.ok(calls.length > before)
  assert.ok(
    calls.slice(before).every((call) => call.secretVersion === 1 && call.model === 'shared-model'),
  )
})

test('A9 live RPM update releases subsequent requests of an existing job', async ({
  backend: fixture,
}) => {
  const { request, makeWorkflow, startJob, until, calls, backendBody, endpoint, terminalJob } =
    fixture
  const { backend, project, plan } = await makeWorkflow('rpm-model', 1)
  const job = await startJob(project, plan)
  await until(
    () => request('GET', '/admin/runtime/summary'),
    (value) => value.limiters?.waiters > 0,
    'real limiter wait',
  )
  assert.equal(calls.length, 1)
  const changedAt = Date.now()
  await request(
    'PUT',
    `/backends/${backend.id}`,
    backendBody('Updated RPM', 'rpm-model', endpoint, undefined, 0),
  )
  assert.equal((await terminalJob(job.id)).status, 'completed')
  assert.ok(Date.now() - changedAt < 10000, 'RPM change did not release existing waiters promptly')
})

test('A9 live execution lease protects versions until the HTTP execution completes', async ({
  backend,
}) => {
  const { request, makeWorkflow, holds, release, calls, until, tokenSecrets } = backend
  const { credential, plan } = await makeWorkflow('lease-model')
  holds.add('lease-model')
  const immediate = request('POST', '/quick-translate', {
    source_text: 'Lease protected execution',
    source_lang: 'en',
    target_lang: 'zh',
    execution_plan_id: plan.id,
  })
  // Attach cleanup immediately so an earlier assertion cannot orphan this pending request.
  try {
    await until(
      () => calls.length,
      (count) => count > 0,
      'leased immediate execution',
    )
    await request(
      'POST',
      `/credentials/${credential.id}/versions`,
      { secret: tokenSecrets[1] },
      201,
    )
    assert.equal(
      (await request('POST', `/credentials/${credential.id}/collect`)).deleted_versions,
      0,
    )
  } finally {
    release('lease-model')
    assert.equal((await immediate).status, 'success')
  }
  assert.equal((await request('POST', `/credentials/${credential.id}/collect`)).deleted_versions, 1)
})

test('A9 live revocation allows issued requests to finish and blocks subsequent job requests', async ({
  backend,
}) => {
  const { request, makeWorkflow, holds, release, calls, until, startJob, terminalJob } = backend
  const { credential, project, plan } = await makeWorkflow('live-revoke-model')
  holds.add('live-revoke-model')
  const job = await startJob(project, plan)
  await until(
    () => calls.length,
    (count) => count > 0,
    'request before live revocation',
  )
  await request('POST', `/credentials/${credential.id}/versions/1/revoke`, undefined, 204)
  release('live-revoke-model')
  assert.equal((await terminalJob(job.id)).status, 'failed')
  assert.equal(calls.length, 1, 'revocation allowed a subsequent upstream request')
})

test('A4/A9 current-version revocation, completed-job collection protection and secret-free responses', async ({
  backend,
}) => {
  const { request, makeWorkflow, startJob, terminalJob, tokenSecrets } = backend
  const { credential, project, plan } = await makeWorkflow()
  const job = await startJob(project, plan)
  assert.equal((await terminalJob(job.id)).status, 'completed')
  await request('POST', `/credentials/${credential.id}/versions`, { secret: tokenSecrets[1] }, 201)
  await request('POST', `/credentials/${credential.id}/versions/2/revoke`, undefined, 204)
  const versions = await request('GET', `/credentials/${credential.id}/versions`)
  assert.equal(versions.items.find((item) => item.version === 2).revoked, true)
  assert.equal((await request('POST', `/credentials/${credential.id}/collect`)).deleted_versions, 0)
  for (const route of [
    '/backends',
    '/credentials',
    `/jobs/${job.id}`,
    `/credentials/${credential.id}/versions`,
  ]) {
    const serialized = JSON.stringify(await request('GET', route))
    for (const secret of tokenSecrets)
      assert.ok(!serialized.includes(secret), `${route} leaked credential`)
    assert.ok(!serialized.includes('api_key'), `${route} contains legacy key`)
  }
})
