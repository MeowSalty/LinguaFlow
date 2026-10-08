import { expect, test, type Page, type Route } from '@playwright/test'
import { json, mockApp } from './fixtures'
import {
  storageControl,
  storageDrawerTab,
  storageAdminTab,
  expectStorageControl,
} from './storage-management-helpers'
import {
  connectionActions,
  policyCapabilities,
  spaceActions,
  storageAction,
  storageRuntime,
} from '../storage-fixtures'
import type { ApiSchemas } from '../../src/api/client-core'

type RecordedRequest = {
  path: string
  method: string
  query: Array<[string, string]>
  body: Record<string, unknown> | null
}
const modes = [
  ['site_only', 'site', '仅站点托管', '站点托管'],
  ['both', 'site', '站点托管或自有存储', '站点托管'],
  ['both', 'user', '站点托管或自有存储', '自有存储'],
  ['user_required', 'user', '必须使用自有存储', '自有存储'],
] as const
const deploymentMessage = '站点尚未启用此操作所需的存储能力，请联系管理员。'
const maintenanceMessage = '存储正在维护，暂时无法执行此操作。'

async function setup(page: Page, role = 'admin') {
  await mockApp(page, { role })
  const requests: RecordedRequest[] = []
  const connection: ApiSchemas['StorageConnection'] = {
    id: 1,
    scope: 'user',
    owner_id: 1,
    name: '既有恢复连接',
    driver: 's3',
    endpoint: 'https://provider.invalid',
    region: 'test',
    status: 'enabled',
    health: 'auth_required',
    has_auth: false,
    management_generation: 8,
    auth_generation: 2,
    management_actions: connectionActions(),
  }
  const space: ApiSchemas['StorageSpace'] = {
    id: 2,
    connection_id: 1,
    name: '保留空间',
    status: 'active',
    verified: true,
    management_generation: 3,
    capacity_bytes: 10000,
    available_bytes: 9980,
    reserved_bytes: 0,
    candidate_bytes: 0,
    live_bytes: 20,
    pending_delete_bytes: 0,
    management_actions: spaceActions(),
  }
  const state = {
    requests,
    connection,
    space,
    checks: [] as ApiSchemas['StorageCheck'][],
    empty: false,
    orgRole: 'owner' as 'owner' | 'admin' | 'member' | null,
    createAction: storageAction(),
    capabilityStatus: 200,
    capabilityIncomplete: false,
    policy: {
      ...policyCapabilities(),
      mode: 'both',
      default_choice: 'user',
      generation: 6,
      logical_limit_bytes: 10000,
      default_space_capacity_bytes: null,
    } as ApiSchemas['StoragePolicy'],
    policyReadStatus: 200,
    onPolicyPut: null as
      | null
      | ((route: Route, body: ApiSchemas['StoragePolicyRequest']) => Promise<void>),
    onCapabilities: null as null | (() => Promise<void>),
  }
  const organization = () => ({
    id: 7,
    name: 'Team Seven',
    slug: 'seven',
    display_name: 'Team Seven',
    current_user_role: state.orgRole,
    created_at: '2026-10-05T00:00:00Z',
  })
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace('/api/v1', '')
    const method = route.request().method()
    if (path === '/orgs') return json(route, { items: state.orgRole ? [organization()] : [] })
    if (path === '/orgs/7')
      return state.orgRole ? json(route, organization()) : json(route, {}, 404)
    if (path === '/orgs/7/members' || path === '/orgs/7/activity') return json(route, { items: [] })
    if (!path.includes('/storage')) return route.fallback()
    const body = method === 'GET' ? null : route.request().postDataJSON()
    requests.push({ path, method, query: [...url.searchParams.entries()], body })
    if (path === '/storage/capabilities') {
      const status = state.capabilityStatus
      const snapshot = structuredClone({
        scope: url.searchParams.get('scope'),
        owner_id: url.searchParams.get('scope') === 'org' ? 7 : 1,
        ...(!state.capabilityIncomplete
          ? {
              runtime: state.policy.runtime,
              management_actions: { create_connection: state.createAction },
            }
          : {}),
      })
      await state.onCapabilities?.()
      if (status !== 200) return json(route, {}, status)
      return json(route, snapshot)
    }
    if (path === '/admin/storage/policy') {
      if (method === 'PUT') {
        if (state.onPolicyPut) return state.onPolicyPut(route, body)
        state.policy = { ...state.policy, ...body, generation: state.policy.generation + 1 }
      }
      return state.policyReadStatus === 200
        ? json(route, state.policy)
        : json(route, {}, state.policyReadStatus)
    }
    if (path === '/admin/storage/diagnostics')
      return json(route, {
        spaces: [],
        disks: [],
        temporary_bytes: 0,
        recovery_backlog: 0,
        blocked_cleanup_by_code: {},
        migrations_by_phase: {},
      })
    if (
      [
        '/storage/connections',
        '/orgs/7/storage/connections',
        '/admin/storage/connections',
      ].includes(path)
    ) {
      const isSite = path.startsWith('/admin')
      const item = isSite
        ? { ...connection, scope: 'site', owner_id: 0, name: '站点恢复连接' }
        : path.startsWith('/orgs/')
          ? { ...connection, scope: 'org', owner_id: 7 }
          : connection
      return json(route, { items: state.empty ? [] : [item] })
    }
    if (path === '/storage/connections/1/spaces') return json(route, { items: [space] })
    if (path === '/storage/connections/1/checks') return json(route, { items: state.checks })
    if (path.endsWith('/authorize')) {
      connection.has_auth = true
      connection.management_generation++
      connection.auth_generation++
      return json(route, connection)
    }
    if (path.endsWith('/revoke')) {
      connection.has_auth = false
      connection.management_generation++
      return json(route, connection)
    }
    if (path.endsWith('/check')) return json(route, connection)
    if (method === 'PATCH' && ['/storage/spaces/2', '/storage/connections/1'].includes(path)) {
      if (path.includes('/spaces/')) {
        Object.assign(space, {
          status: body.status,
          management_generation: space.management_generation + 1,
        })
        return json(route, space)
      }
      Object.assign(connection, {
        status: body.status,
        management_generation: connection.management_generation + 1,
      })
      return json(route, connection)
    }
    return json(route, { title: 'Unexpected storage fixture route' }, 404)
  })
  return state
}

const writes = (state: Awaited<ReturnType<typeof setup>>) =>
  state.requests.filter((request) => request.method !== 'GET')
const policyCard = (page: Page) =>
  page
    .locator('.n-card')
    .filter({ has: page.locator('.n-card-header__main', { hasText: '策略与配额' }) })
    .first()
const quota = (page: Page) =>
  policyCard(page).getByRole('textbox', { name: '每个用户或组织的内容配额', exact: true })
const policySave = (page: Page) =>
  policyCard(page).getByRole('button', { name: '保存策略', exact: true })
async function openPolicy(page: Page) {
  await page.goto('/admin/storage')
  await storageAdminTab(page, '存储策略')
  await policyCard(page).locator('.n-base-selection').last().click()
  await page.locator('.n-base-select-option').filter({ hasText: /^B$/ }).click()
}
const focus = (page: Page) =>
  page.evaluate(() => {
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
  })

test('E-T01 empty personal storage discovers exact ownership and explains disabled creation', async ({
  page,
}) => {
  const state = await setup(page, 'user')
  state.empty = true
  state.policy.runtime = storageRuntime(false)
  state.createAction = storageAction(false)
  await page.goto('/settings/storage')
  await expect(page.getByText('暂无存储连接', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '新建连接', exact: true })).toBeDisabled()
  await expect(page.getByText(`新建连接：${deploymentMessage}`, { exact: true })).toBeVisible()
  const capabilities = state.requests.filter((request) => request.path === '/storage/capabilities')
  expect(capabilities).toHaveLength(1)
  expect(capabilities[0]!.query).toEqual([['scope', 'user']])
  expect(state.requests.some((request) => request.path.startsWith('/admin'))).toBe(false)
  expect(writes(state)).toHaveLength(0)
})

for (const role of ['owner', 'admin', 'member', null] as const) {
  test(`E-T01 organization role ${role ?? 'nonmember'} obeys its own scope even for a platform administrator`, async ({
    page,
  }) => {
    const state = await setup(page)
    state.orgRole = role
    state.empty = true
    state.policy.runtime = storageRuntime(false)
    state.createAction = storageAction(false)
    await page.goto('/settings/team?org_id=7')
    if (role === 'owner' || role === 'admin') {
      await expect(page.getByRole('button', { name: '新建连接', exact: true })).toBeDisabled()
      await expect(page.getByText(`新建连接：${deploymentMessage}`, { exact: true })).toBeVisible()
      const capabilities = state.requests.filter(
        (request) => request.path === '/storage/capabilities',
      )
      expect(capabilities).toHaveLength(1)
      expect(capabilities[0]!.query).toEqual([
        ['scope', 'org'],
        ['organization_id', '7'],
      ])
      expect(state.requests.some((request) => request.path === '/orgs/7/storage/connections')).toBe(
        true,
      )
    } else {
      await expect(page.getByRole('button', { name: '新建连接', exact: true })).toHaveCount(0)
      await expect(page.locator('.n-skeleton')).toHaveCount(0)
      expect(state.requests).toHaveLength(0)
    }
    expect(
      state.requests.some(
        (request) =>
          request.path.startsWith('/admin/storage') || request.path === '/storage/connections',
      ),
    ).toBe(false)
    expect(writes(state)).toHaveLength(0)
  })
}

for (const [mode, defaultChoice, modeLabel, choiceLabel] of modes) {
  for (const enabled of [true, false]) {
    for (const maintenance of [true, false]) {
      test(`E-T02 saved ${mode}/${defaultChoice} enabled=${enabled} maintenance=${maintenance}`, async ({
        page,
      }) => {
        const state = await setup(page)
        state.connection.has_auth = true
        state.connection.health = 'healthy'
        Object.assign(state.policy, {
          mode,
          default_choice: defaultChoice,
          runtime: storageRuntime(enabled, maintenance),
          allowed_policy_modes: enabled ? ['site_only', 'both', 'user_required'] : ['site_only'],
          policy_restriction_codes:
            !enabled && mode !== 'site_only' ? ['storage_deployment_disabled'] : [],
        })
        for (const action of [
          'create_space',
          'authorize_read',
          'authorize_write',
          'revoke_auth',
          'set_status',
        ] as const)
          state.connection.management_actions[action] = storageAction(false, [
            'storage_capability_unsupported',
          ])
        state.connection.management_actions.check_read = storageAction()
        state.connection.management_actions.check_write = storageAction(
          enabled && !maintenance,
          maintenance ? ['storage_maintenance'] : ['storage_deployment_disabled'],
        )
        await openPolicy(page)
        await expect(quota(page)).toHaveValue('10000')
        const selects = policyCard(page).locator('.n-base-selection-label')
        await expect(selects.nth(0)).toContainText(modeLabel)
        await expect(selects.nth(1)).toContainText(choiceLabel)
        await quota(page).fill('12000')
        if (enabled || mode === 'site_only') await expect(policySave(page)).toBeEnabled()
        else {
          await expect(policySave(page)).toBeDisabled()
          await expect(
            page.getByText('当前部署不支持保存此政策模式。', { exact: true }),
          ).toBeVisible()
        }
        expect(state.requests.some((request) => request.path === '/storage/capabilities')).toBe(
          false,
        )
        if (new URL(page.url()).pathname === '/admin/storage')
          await storageAdminTab(page, '存储连接')
        await page.getByRole('button', { name: '查看详情', exact: true }).click()
        const drawer = page.locator('.n-drawer:visible')
        for (const name of ['新建空间', '更新授权', '撤销授权', '禁用连接'])
          await expectStorageControl(page, name, false)
        await storageDrawerTab(page, '连接与授权')
        for (const name of ['恢复只读授权', '授权并写检查'])
          await expect(
            drawer.getByText(`${name}：此存储不支持该操作。`, { exact: true }),
          ).toBeVisible()
        await expectStorageControl(page, '只读检测', true)
        const writeCheck = await storageControl(page, '写入检测')
        if (enabled && !maintenance) await expect(writeCheck).toBeEnabled()
        else await expect(writeCheck).toBeDisabled()
        await expectStorageControl(page, '设为只读', true)
        expect(writes(state)).toHaveLength(0)
      })
    }
  }
}

test('E-T04 restricted quota draft changes policy only through the explicit site action and sends five fields', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  Object.assign(state.policy, {
    runtime: storageRuntime(false),
    allowed_policy_modes: ['site_only'],
    policy_restriction_codes: ['storage_deployment_disabled'],
  })
  await openPolicy(page)
  await quota(page).fill('23000')
  await expect(policySave(page)).toBeDisabled()
  expect(writes(state)).toHaveLength(0)
  await page.getByRole('button', { name: '改为仅站点存储', exact: true }).click()
  await expect(policySave(page)).toBeEnabled()
  expect(writes(state)).toHaveLength(0)
  await policySave(page).click()
  await expect.poll(() => writes(state).length).toBe(1)
  expect(writes(state)[0]!.body).toEqual({
    mode: 'site_only',
    default_choice: 'site',
    generation: 6,
    logical_limit_bytes: 23000,
    default_space_capacity_bytes: null,
  })
  await expect(quota(page)).toHaveValue('22.4609375')
  await expect(policyCard(page).getByText('23,000 字节', { exact: true })).toBeVisible()
  await expect(policyCard(page).locator('.n-base-selection-label').last()).toContainText('KiB')
})

test('E-T04/E-T09 focus preserves a dirty draft and external generation requires explicit review', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  await openPolicy(page)
  await quota(page).fill('23000')
  state.policy = { ...state.policy, generation: 7, logical_limit_bytes: 17000 }
  await focus(page)
  await expect(page.getByRole('button', { name: '核对差异', exact: true })).toBeVisible()
  await expect(quota(page)).toHaveValue('23000')
  await expect(policySave(page)).toBeDisabled()
  await page.getByRole('button', { name: '核对差异', exact: true }).click()
  await page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '采用当前基线', exact: true })
    .click()
  await expect(policySave(page)).toBeEnabled()
  expect(writes(state)).toHaveLength(0)
  await policySave(page).click()
  await expect.poll(() => writes(state).length).toBe(1)
  expect(writes(state)[0]!.body).toEqual({
    mode: 'both',
    default_choice: 'user',
    generation: 7,
    logical_limit_bytes: 23000,
    default_space_capacity_bytes: null,
  })
})

test('E-T04 a deployment refusal after an old snapshot retains the entire draft without retrying', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  state.onPolicyPut = async (route) => {
    state.policy = {
      ...state.policy,
      runtime: storageRuntime(false),
      allowed_policy_modes: ['site_only'],
      policy_restriction_codes: ['storage_deployment_disabled'],
    }
    await json(route, { error_code: 'storage_deployment_disabled' }, 409)
  }
  await openPolicy(page)
  await quota(page).fill('23000')
  await policySave(page).click()
  await expect(page.getByText('当前部署不支持保存此政策模式。', { exact: true })).toBeVisible()
  await expect(quota(page)).toHaveValue('23000')
  await expect(policyCard(page).locator('.n-base-selection-label').nth(0)).toContainText(
    '站点托管或自有存储',
  )
  await expect(policySave(page)).toBeDisabled()
  await expect(page.getByText('存储配置已更新', { exact: true })).toHaveCount(0)
  await focus(page)
  await expect(quota(page)).toHaveValue('23000')
  expect(writes(state)).toHaveLength(1)
})

test('E-T04 freezes fields during PUT and advances the baseline for the next explicit save', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  let finish!: () => void
  state.onPolicyPut = async (route, body) => {
    await new Promise<void>((resolve) => {
      finish = resolve
    })
    state.policy = { ...state.policy, ...body, generation: state.policy.generation + 1 }
    await json(route, state.policy)
  }
  await openPolicy(page)
  await quota(page).fill('23000')
  await policySave(page).click()
  await expect.poll(() => typeof finish).toBe('function')
  await expect(quota(page)).toBeDisabled()
  await expect(policyCard(page).locator('.n-base-selection--disabled')).toHaveCount(3)
  finish()
  await expect(quota(page)).toBeEnabled()
  state.onPolicyPut = null
  await quota(page).fill('25000')
  await expect(policySave(page)).toBeEnabled()
  await policySave(page).click()
  await expect.poll(() => writes(state).length).toBe(2)
  expect(writes(state).map((request) => request.body?.generation)).toEqual([6, 7])
  await expect(page.getByRole('button', { name: '核对差异', exact: true })).toHaveCount(0)
})

test('E-T04 successful PUT followed by failed GET stays saved, preserves new editing and never resends PUT', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  state.onPolicyPut = async (route, body) => {
    state.policy = { ...state.policy, ...body, generation: 7 }
    state.policyReadStatus = 503
    await json(route, state.policy)
  }
  await openPolicy(page)
  await quota(page).fill('23000')
  await policySave(page).click()
  await expect(
    page.getByText('政策已保存，状态待刷新。不会自动再次提交。', { exact: true }),
  ).toBeVisible()
  await expect(quota(page)).toHaveValue('22.4609375')
  await expect(policyCard(page).getByText('23,000 字节', { exact: true })).toBeVisible()
  await policyCard(page).locator('.n-base-selection').last().click()
  await page.locator('.n-base-select-option').filter({ hasText: /^B$/ }).click()
  await expect(quota(page)).toHaveValue('23000')
  await quota(page).fill('25000')
  await focus(page)
  await expect(quota(page)).toHaveValue('25000')
  await expect(policySave(page)).toBeDisabled()
  expect(writes(state)).toHaveLength(1)
  state.policyReadStatus = 200
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(policySave(page)).toBeEnabled()
  await expect(quota(page)).toHaveValue('25000')
  expect(writes(state)).toHaveLength(1)
})

for (const writeCheck of [false, true]) {
  test(`E-T06 explicit authorization write_check=${writeCheck} remains usable with missing credentials and failed history`, async ({
    page,
  }) => {
    const state = await setup(page)
    state.checks = [
      {
        check_id: 9,
        connection_id: 1,
        mode: 'write',
        management_generation: 7,
        created_at: '2026-10-04T00:00:00Z',
        completed_at: '2026-10-04T00:00:01Z',
        status: 'failed',
        cleanup_status: 'done',
        error_code: 'storage_permission_denied',
        accounted_bytes: 0,
        authorization_activated: false,
        results: [],
      },
    ]
    await page.goto('/settings/storage')
    await page.getByRole('button', { name: '查看详情', exact: true }).click()
    await storageDrawerTab(page, '检测历史')
    await expect(page.getByText('检测失败', { exact: true })).toBeVisible()
    await (await storageControl(page, '更新授权')).click()
    const modal = page.locator('.n-modal:visible')
    await expect(modal.getByText('恢复只读授权', { exact: true })).toBeVisible()
    if (writeCheck) {
      await modal.locator('.n-base-selection').click()
      await page.locator('.n-base-select-option').filter({ hasText: '授权并写检查' }).click()
    }
    await modal.locator('input').nth(0).fill('access')
    await modal.locator('input').nth(1).fill('secret')
    await modal.getByRole('button', { name: '保存', exact: true }).click()
    await expect.poll(() => writes(state).length).toBe(1)
    expect(writes(state)[0]!.body).toEqual({
      access_key_id: 'access',
      secret_access_key: 'secret',
      write_check: writeCheck,
      expected_management_generation: 8,
    })
  })
}

test('E-T06 deployment blocks only refused actions and preserves read recovery and revocation', async ({
  page,
}) => {
  const state = await setup(page)
  state.policy.runtime = storageRuntime(false)
  state.createAction = storageAction(false)
  for (const action of ['create_space', 'authorize_write', 'check_write'] as const)
    state.connection.management_actions[action] = storageAction(false)
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  await expectStorageControl(page, '新建空间', false)
  await expectStorageControl(page, '写入检测', false)
  await expectStorageControl(page, '只读检测', true)
  await expectStorageControl(page, '更新授权', true)
  await expectStorageControl(page, '撤销授权', true)
  await (await storageControl(page, '只读检测')).click()
  await expect.poll(() => writes(state).length).toBe(1)
  expect(writes(state)[0]!.body).toEqual({ write_check: false, expected_generation: 8 })
})

for (const reason of [
  'storage_capability_unsupported',
  'storage_space_required',
  'storage_check_space_limit_exceeded',
] as const) {
  test(`E-T06 displays the backend check reason ${reason} without probing`, async ({ page }) => {
    const state = await setup(page)
    state.connection.management_actions.check_read = storageAction(false, [reason])
    state.connection.management_actions.check_write = storageAction(false, [reason])
    if (reason === 'storage_capability_unsupported') state.connection.driver = 'local'
    await page.goto('/settings/storage')
    await page.getByRole('button', { name: '查看详情', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await expectStorageControl(page, '只读检测', false)
    await expectStorageControl(page, '写入检测', false)
    const reasonText =
      reason === 'storage_capability_unsupported'
        ? '此存储不支持该操作。'
        : reason === 'storage_space_required'
          ? '请先创建存储空间。'
          : '空间总数超过 100（含已禁用空间），暂不支持连接检查。'
    await expect(drawer.getByText(reasonText, { exact: false }).first()).toBeVisible()
    expect(writes(state)).toHaveLength(0)
  })
}

test('E-T06 site Local connection actions remain unavailable while its space state works by keyboard', async ({
  page,
}) => {
  const state = await setup(page)
  state.connection.driver = 'local'
  for (const action of Object.keys(state.connection.management_actions) as Array<
    keyof typeof state.connection.management_actions
  >) {
    state.connection.management_actions[action] = storageAction(false, [
      'storage_capability_unsupported',
    ])
  }
  await openPolicy(page)
  await storageAdminTab(page, '存储连接')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  for (const name of ['新建空间', '只读检测', '写入检测', '更新授权', '撤销授权', '禁用连接']) {
    await expectStorageControl(page, name, false)
  }
  const change = await storageControl(page, '设为只读')
  await expect(change).toBeEnabled()
  await change.focus()
  await page.keyboard.press('Enter')
  const confirm = page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '保存', exact: true })
  await confirm.focus()
  await page.keyboard.press('Enter')
  await expect.poll(() => writes(state).length).toBe(1)
  expect(writes(state)[0]).toMatchObject({
    path: '/storage/spaces/2',
    method: 'PATCH',
    body: { status: 'read_only', expected_generation: 3 },
  })
  expect(state.requests.some((request) => request.path === '/storage/capabilities')).toBe(false)
})

test('E-T06 a refused space state does not disable allowed connection state management', async ({
  page,
}) => {
  const state = await setup(page)
  state.space.management_actions.set_status = storageAction(false, ['storage_maintenance'])
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  await expectStorageControl(page, '设为只读', false)
  await expect(drawer.getByText(maintenanceMessage, { exact: true })).toBeVisible()
  await expectStorageControl(page, '禁用连接', true)
  expect(writes(state)).toHaveLength(0)
})

test('E-T09 focus and visibility invalidate a live capability before refresh and do not repeat writes', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  await page.goto('/settings/storage')
  const create = page.getByRole('button', { name: '新建连接', exact: true })
  await expect(create).toBeEnabled()
  const readsBefore = state.requests.filter(
    (request) => request.path === '/storage/capabilities',
  ).length
  let finish!: () => void
  state.onCapabilities = () =>
    new Promise<void>((resolve) => {
      finish = resolve
    })
  state.policy.runtime = storageRuntime(false, true)
  state.createAction = storageAction(false, ['storage_maintenance'])
  await focus(page)
  await expect(create).toBeDisabled()
  await expect.poll(() => typeof finish).toBe('function')
  expect(state.requests.filter((request) => request.path === '/storage/capabilities')).toHaveLength(
    readsBefore + 1,
  )
  finish()
  state.onCapabilities = null
  await expect(page.getByText(`新建连接：${maintenanceMessage}`, { exact: true })).toBeVisible()
  expect(writes(state)).toHaveLength(0)
  state.policy.runtime = storageRuntime()
  state.createAction = storageAction()
  await focus(page)
  await expect(create).toBeEnabled()
  expect(writes(state)).toHaveLength(0)
})

test('E-T09 returning to storage discovers again and ignores an enabled response from its previous mount', async ({
  page,
}) => {
  const state = await setup(page)
  state.empty = true
  await page.goto('/settings/storage')
  const create = page.getByRole('button', { name: '新建连接', exact: true })
  await expect(create).toBeEnabled()
  let finish!: () => void
  state.onCapabilities = () =>
    new Promise<void>((resolve) => {
      finish = resolve
    })
  await focus(page)
  await expect.poll(() => typeof finish).toBe('function')
  await page.getByRole('link', { name: '个人资料', exact: true }).click()
  await expect(create).toHaveCount(0)
  state.policy.runtime = storageRuntime(false)
  state.createAction = storageAction(false)
  state.onCapabilities = null
  await page.getByRole('link', { name: '文件存储', exact: true }).click()
  await expect(page.getByText(`新建连接：${deploymentMessage}`, { exact: true })).toBeVisible()
  await expect(create).toBeDisabled()
  finish()
  await expect(create).toBeDisabled()
  expect(state.requests.filter((request) => request.path === '/storage/capabilities')).toHaveLength(
    3,
  )
  expect(writes(state)).toHaveLength(0)
})

for (const failure of ['missing-route', 'incomplete', 'network'] as const) {
  test(`E-T10 ${failure} keeps readable connections without guessing permission or falling back to administrator APIs`, async ({
    page,
  }) => {
    const state = await setup(page, 'user')
    state.capabilityStatus = failure === 'missing-route' ? 404 : failure === 'network' ? 503 : 200
    state.capabilityIncomplete = failure === 'incomplete'
    await page.goto('/settings/storage')
    await expect(page.getByText('既有恢复连接', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: '新建连接', exact: true })).toBeDisabled()
    const unsupported = page.getByText('新建连接：后端版本尚不支持能力发现。', { exact: true })
    if (failure === 'missing-route') await expect(unsupported).toBeVisible()
    else await expect(unsupported).toHaveCount(0)
    expect(state.requests.some((request) => request.path.startsWith('/admin'))).toBe(false)
    expect(writes(state)).toHaveLength(0)
  })
}

test('E-T10 an organization capability 404 is scope uncertainty and never a personal or site fallback', async ({
  page,
}) => {
  const state = await setup(page)
  state.capabilityStatus = 404
  await page.goto('/settings/team?org_id=7')
  await expect(page.getByRole('button', { name: '新建连接', exact: true })).toBeDisabled()
  await expect(
    page.getByText('新建连接：此归属不可访问，或存储合同尚待确认。', { exact: true }),
  ).toBeVisible()
  await expect(page.getByText('后端版本尚不支持能力发现。')).toHaveCount(0)
  expect(
    state.requests.some(
      (request) => request.path.startsWith('/admin') || request.path === '/storage/connections',
    ),
  ).toBe(false)
  expect(writes(state)).toHaveLength(0)
})
