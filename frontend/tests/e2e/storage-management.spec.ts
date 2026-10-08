import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'
import {
  storageControl,
  storageDrawerTab,
  expectStorageControl,
} from './storage-management-helpers'
import {
  connectionActions,
  spaceActions,
  policyCapabilities,
  storageCapabilities,
  storageAction,
} from '../storage-fixtures'

const storageCheck = {
  check_id: 10,
  connection_id: 1,
  mode: 'write',
  management_generation: 8,
  created_at: '2026-10-04T00:00:00Z',
  completed_at: '2026-10-04T00:00:01Z',
  status: 'completed',
  cleanup_status: 'blocked',
  accounted_bytes: 32,
  authorization_activated: false,
  results: [
    { space_id: 2, status: 'completed' },
    { space_id: 3, status: 'failed', error_code: 'storage_permission_denied' },
  ],
}

async function setup(page: Page, role = 'admin') {
  await mockApp(page, { role })
  const requests: Array<{ path: string; method: string; body: unknown }> = []
  const connection = {
    id: 1,
    scope: 'user',
    owner_id: 1,
    name: '个人原件空间',
    driver: 's3',
    endpoint: 'https://example.invalid',
    region: 'test',
    status: 'enabled',
    health: 'future_health',
    has_auth: true,
    management_generation: 8,
    auth_generation: 2,
    management_actions: connectionActions(),
  }
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
    const method = route.request().method()
    if (path.includes('/storage')) {
      requests.push({
        path,
        method,
        body: method === 'GET' ? null : route.request().postDataJSON(),
      })
      if (path === '/storage/capabilities') return json(route, storageCapabilities())
      if (path === '/storage/connections') return json(route, { items: [connection] })
      if (path === '/admin/storage/connections')
        return json(route, {
          items: [
            {
              ...connection,
              scope: 'site',
              owner_id: 0,
              name: '站点托管',
              management_actions: {
                ...connectionActions(),
                create_space: storageAction(false, ['storage_capability_unsupported']),
                authorize_read: storageAction(false, ['storage_capability_unsupported']),
                authorize_write: storageAction(false, ['storage_capability_unsupported']),
                revoke_auth: storageAction(false, ['storage_capability_unsupported']),
                set_status: storageAction(false, ['storage_capability_unsupported']),
              },
            },
          ],
        })
      if (path.endsWith('/spaces'))
        return json(route, {
          items: [
            {
              id: 2,
              connection_id: 1,
              name: '原件',
              status: 'active',
              verified: true,
              management_generation: 3,
              management_actions: spaceActions(),
              capacity_bytes: 10000,
              available_bytes: 9360,
              reserved_bytes: 100,
              candidate_bytes: 200,
              live_bytes: 300,
              pending_delete_bytes: 40,
            },
          ],
        })
      if (path.endsWith('/check'))
        return json(route, { ...connection, check_id: 10, checked_at: '2026-10-04T00:00:00Z' })
      if (path.endsWith('/checks')) return json(route, { items: [storageCheck] })
      if (path.endsWith('/checks/10')) return json(route, storageCheck)
      if (path.endsWith('/authorize'))
        return json(route, {
          ...connection,
          check_id: 10,
          management_generation: 9,
          auth_generation: 3,
        })
      if (path.endsWith('/revoke')) {
        connection.has_auth = false
        connection.management_generation += 1
        return json(route, connection)
      }
      if (path === '/admin/storage/policy')
        return json(route, {
          mode: 'site_only',
          ...policyCapabilities(),
          default_choice: 'site',
          generation: 6,
          logical_limit_bytes: 10000,
          default_space_capacity_bytes: null,
        })
      if (path.startsWith('/admin/storage/diagnostics'))
        return json(route, {
          spaces: [],
          disks: [],
          temporary_bytes: 0,
          recovery_backlog: 2,
          blocked_cleanup_by_code: {},
          migrations_by_phase: {},
        })
    }
    return route.fallback()
  })
  return requests
}

test('connection details are read-only until an explicit check and show capacity facts', async ({
  page,
}) => {
  const requests = await setup(page)
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  await storageDrawerTab(page, '连接与授权')
  await expect(drawer.getByText('状态待确认', { exact: true })).toBeVisible()
  await storageDrawerTab(page, '空间')
  await drawer.locator('summary').filter({ hasText: '容量明细' }).click()
  await expect(drawer.getByText('待删除', { exact: true })).toBeVisible()
  expect(requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  expect(requests.some((request) => request.path.endsWith('/checks'))).toBe(false)
  await (await storageControl(page, '只读检测')).click()
  await expect
    .poll(() => requests.filter((request) => request.path.endsWith('/check')).length)
    .toBe(1)
  expect(requests.find((request) => request.path.endsWith('/check'))?.body).toEqual({
    write_check: false,
    expected_generation: 8,
  })
  await expectStorageControl(page, '写入检测', true)
  await expectStorageControl(page, '撤销授权', true)
  await storageDrawerTab(page, '空间')
  await expect(drawer.getByText('登记占用', { exact: true })).toBeVisible()
  await expect(drawer.getByText('640 字节', { exact: true })).toBeVisible()
  await expect(drawer.getByText('9,360 字节', { exact: true })).toBeVisible()
  await storageDrawerTab(page, '检测历史')
  await drawer.locator('summary').filter({ hasText: '#10' }).click()
  await expect(drawer.getByText('本次未激活授权', { exact: true })).toBeVisible()
  await expect(drawer.getByText(/清理受阻 · 探针账本占用/)).toBeVisible()
  await expect(drawer.getByText(/空间 #3 · 检测失败/)).toBeVisible()
})

test('revocation remains available while a remote probe is pending and uses only its generation', async ({
  page,
}) => {
  const requests = await setup(page)
  let finishProbe!: () => void
  await page.route('**/api/v1/storage/connections/1/check', async (route) => {
    await new Promise<void>((resolve) => {
      finishProbe = resolve
    })
    await route.fallback()
  })
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  await (await storageControl(page, '只读检测')).click()
  await expect.poll(() => typeof finishProbe).toBe('function')
  await (await storageControl(page, '撤销授权')).click()
  await page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '撤销授权', exact: true })
    .click()
  await expect
    .poll(() => requests.find((request) => request.path.endsWith('/revoke'))?.body)
    .toEqual({ expected_generation: 8 })
  finishProbe()
  await storageDrawerTab(page, '连接与授权')
  await expect(drawer.getByText('尚未授权', { exact: true })).toBeVisible()
})

test('closing authorization clears secrets and never persists them in browser storage', async ({
  page,
}) => {
  await setup(page)
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  await (await storageControl(page, '更新授权')).click()
  const modal = page.locator('.n-modal:visible')
  await modal.locator('input').nth(0).fill('temporary-access-id')
  await modal.locator('input').nth(1).fill('secret-never-persisted')
  await modal.getByRole('button', { name: '取消', exact: true }).click()
  await (await storageControl(page, '更新授权')).click()
  await expect(modal.locator('input').nth(0)).toHaveValue('')
  await expect(modal.locator('input').nth(1)).toHaveValue('')
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
  ).not.toContain('secret-never-persisted')
})

test('submitting slow authorization clears the secret form and keeps emergency revocation reachable', async ({
  page,
}) => {
  const requests = await setup(page)
  let finishAuthorization!: () => void
  await page.route('**/api/v1/storage/connections/1/authorize', async (route) => {
    await new Promise<void>((resolve) => {
      finishAuthorization = resolve
    })
    await route.fallback()
  })
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  await (await storageControl(page, '更新授权')).click()
  const modal = page.locator('.n-modal:visible')
  await modal.locator('input').nth(0).fill('temporary-access-id')
  await modal.locator('input').nth(1).fill('temporary-secret')
  await modal.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => typeof finishAuthorization).toBe('function')
  await expect(modal).toHaveCount(0)
  const drawer = page.locator('.n-drawer:visible')
  await (await storageControl(page, '撤销授权')).click()
  await page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '撤销授权', exact: true })
    .click()
  await expect.poll(() => requests.some((request) => request.path.endsWith('/revoke'))).toBe(true)
  finishAuthorization()
  await expect(drawer.getByText('尚未授权', { exact: true })).toBeVisible()
})

test('a late authorization cannot clear a new secret draft opened after revocation', async ({
  page,
}) => {
  const requests = await setup(page)
  let finishAuthorization!: () => void
  await page.route('**/api/v1/storage/connections/1/authorize', async (route) => {
    await new Promise<void>((resolve) => {
      finishAuthorization = resolve
    })
    await route.fallback()
  })
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  await (await storageControl(page, '更新授权')).click()
  const modal = page.locator('.n-modal:visible')
  await modal.locator('input').nth(0).fill('first-access')
  await modal.locator('input').nth(1).fill('first-secret')
  await modal.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => typeof finishAuthorization).toBe('function')
  await (await storageControl(page, '撤销授权')).click()
  await page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '撤销授权', exact: true })
    .click()
  await expectStorageControl(page, '更新授权', true)
  await (await storageControl(page, '更新授权')).click()
  await modal.locator('input').nth(0).fill('replacement-access')
  await modal.locator('input').nth(1).fill('replacement-secret')
  const completed = page.waitForResponse('**/api/v1/storage/connections/1/authorize')
  finishAuthorization()
  await (await completed).finished()
  await expect(modal.locator('input').nth(0)).toHaveValue('replacement-access')
  await expect(modal.locator('input').nth(1)).toHaveValue('replacement-secret')
  expect(requests.filter((request) => request.path.endsWith('/authorize'))).toHaveLength(1)
  expect(
    await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
  ).not.toContain('replacement-secret')
})

test('administrator diagnostics do not perform remote probes or mutation', async ({ page }) => {
  const requests = await setup(page)
  await page.goto('/admin/storage')
  await expect(page.getByText('诊断摘要', { exact: true })).toBeVisible()
  await expect(page.getByText('恢复积压', { exact: true })).toBeVisible()
  expect(requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  expect(requests.some((request) => request.path.endsWith('/check'))).toBe(false)
  await expect(page.getByRole('button', { name: '新建连接', exact: true })).toHaveCount(0)
})

test('ordinary users cannot enter administrator storage or read its policy', async ({ page }) => {
  const requests = await setup(page, 'user')
  await page.goto('/admin/storage')
  await expect(page).toHaveURL(/\/$/)
  expect(requests.some((request) => request.path.startsWith('/admin/storage'))).toBe(false)
  await page.goto('/settings/storage')
  await expect(page.getByText('个人原件空间', { exact: true })).toBeVisible()
  expect(requests.some((request) => request.path === '/admin/storage/policy')).toBe(false)
})

for (const action of ['禁用连接', '设为只读']) {
  test(`an old ${action} confirmation cannot mutate after navigating away`, async ({ page }) => {
    const requests = await setup(page)
    await page.goto('/settings/preferences')
    await page.getByRole('link', { name: '文件存储', exact: true }).click()
    await page.getByRole('button', { name: '查看详情', exact: true }).click()
    await (await storageControl(page, action)).click()
    const confirmation = page.locator('.n-dialog:visible')
    await expect(confirmation).toBeVisible()
    await page.goBack()
    await expect(page).toHaveURL(/\/settings\/preferences$/)
    await expect(page.locator('.n-drawer:visible')).toHaveCount(0)
    await confirmation.getByRole('button', { name: '保存', exact: true }).click()
    await expect(confirmation).toHaveCount(0)
    expect(requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  })
}
