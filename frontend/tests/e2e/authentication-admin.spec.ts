import { expect, test } from '@playwright/test'
import { json, mockApp } from './fixtures'
import {
  authDiagnostics,
  b4ConnectionCalls,
  mockB4Connection,
  oidcConnection,
  type B4ConnectionHandler,
} from './auth-admin-b4-fixtures'

const baseRecord = () => oidcConnection()

test('create, copy callback, save conflicting candidate, reconnect, then enable', async ({
  page,
}) => {
  await mockApp(page)
  const configured = baseRecord()
  let record = oidcConnection({
    ...configured,
    aggregate_revision: 1,
    configuration_complete: false,
    configuration: {
      ...configured.configuration,
      has_client_secret: false,
      oidc: { ...configured.configuration.oidc!, issuer: '', client_id: '' },
    },
  })
  let created = false
  let saveConflict = true
  let saved: Record<string, unknown> | undefined
  const calls = b4ConnectionCalls()
  const catalogs: Array<{ revision: number; purpose: string }> = []
  const impacts: Record<string, unknown>[] = []
  await page.route('**/api/v1/admin/auth-connections/company/attributes**', (route) => {
    const url = new URL(route.request().url())
    const candidate = route.request().method() === 'POST' ? route.request().postDataJSON() : null
    const revision =
      candidate?.expected_revision ?? Number(url.searchParams.get('expected_revision'))
    const purpose = candidate?.purpose ?? url.searchParams.get('purpose')
    catalogs.push({ revision, purpose })
    return json(route, {
      revision,
      purpose,
      fields: [],
      operators: {},
    })
  })
  await page.route('**/api/v1/admin/auth-connections/company/impact', (route) => {
    const body = route.request().postDataJSON()
    impacts.push(body)
    for (const purpose of ['login', 'signup', 'link'])
      expect(catalogs).toContainEqual({ revision: 8, purpose })
    return json(route, {
      affected_users: [],
      blocked_users: [],
      affected_sessions: 0,
      can_save: true,
    })
  })
  await page.route('**/api/v1/admin/auth-connections?*', (route) =>
    json(route, { items: created ? [record] : [] }),
  )
  await page.route('**/api/v1/admin/auth-connections', (route) => {
    if (route.request().method() !== 'POST') return json(route, { items: created ? [record] : [] })
    expect(route.request().postDataJSON()).toEqual({
      kind: 'oidc',
      display_name: 'Company SSO',
      description: 'Production identity provider',
    })
    created = true
    return json(route, record, 201)
  })
  await page.route('**/api/v1/admin/auth-connections/company', async (route) => {
    if (route.request().method() !== 'PATCH') return json(route, record)
    saved = route.request().postDataJSON()
    calls.save += 1
    if (saveConflict) {
      saveConflict = false
      record = oidcConnection({
        aggregate_revision: 8,
        configuration: {
          ...record.configuration,
          oidc: {
            ...record.configuration.oidc!,
            issuer: 'https://writer.example',
            client_id: 'writer-client',
          },
        },
      })
      return json(
        route,
        {
          type: 'urn:linguaflow:auth-connection-revision-conflict',
          title: 'Revision conflict',
          status: 409,
          code: 'revision-conflict',
        },
        409,
      )
    }
    const body = saved!
    record = oidcConnection({
      aggregate_revision: 9,
      configuration: {
        ...record.configuration,
        oidc: {
          ...record.configuration.oidc!,
          issuer: body.oidc.issuer,
          client_id: body.oidc.client_id,
        },
      },
    })
    return json(route, record)
  })
  await page.goto('/admin/authentication')
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-connections-create')).toBeEnabled()
  await page.getByTestId('auth-connections-create').click()
  await page.getByLabel('显示名称').fill('Company SSO')
  await page.getByRole('dialog').locator('textarea').fill('Production identity provider')
  await page.getByRole('dialog').getByRole('button', { name: '创建连接' }).click()
  const drawer = page.locator('.n-drawer-content')
  await expect(drawer.getByLabel('显示名称')).toHaveValue('Company SSO')
  await expect(drawer.getByText('https://frontend.example/auth/callback/company')).toBeVisible()
  await drawer.getByRole('button', { name: '复制', exact: true }).click()
  await expect(page.evaluate(() => navigator.clipboard.readText())).resolves.toBe(
    'https://frontend.example/auth/callback/company',
  )

  await expect(drawer.getByLabel('Issuer 地址')).toHaveValue('')
  await expect(drawer.getByLabel('Client ID')).toHaveValue('')
  await expect(drawer.getByRole('button', { name: '保存配置', exact: true })).toBeDisabled()

  await drawer.getByLabel('Issuer 地址').fill('https://candidate.example')
  await drawer.getByLabel('Client ID').fill('candidate-client')
  await drawer.getByRole('button', { name: '保存配置', exact: true }).click()
  const conflictPanel = drawer
    .locator('.n-alert')
    .filter({ hasText: '设置已被其他管理员更新。请选择最新设置，或将我的修改重新应用后手动保存。' })
  await expect.poll(() => calls.save).toBe(1)
  await expect(conflictPanel).toContainText('https://writer.example')
  await expect(conflictPanel).toContainText('https://candidate.example')
  await expect(drawer.getByRole('heading', { name: '最新设置' })).toBeVisible()
  await expect(drawer.getByRole('heading', { name: '我的修改' })).toBeVisible()
  await drawer.getByTestId('auth-connection-conflict-reapply').click()
  await expect.poll(() => impacts.length).toBe(1)
  expect(impacts[0]).toMatchObject({
    expected_revision: 8,
    changes: { oidc: { issuer: 'https://candidate.example', client_id: 'candidate-client' } },
  })
  expect(calls.save).toBe(1)
  await drawer.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect.poll(() => calls.save).toBe(2)
  expect(saved).toMatchObject({
    expected_revision: 8,
    oidc: { issuer: 'https://candidate.example', client_id: 'candidate-client' },
  })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
})

test('candidate check and enable action require a revision-bound test proof', async ({ page }) => {
  const calls = b4ConnectionCalls()
  let record = baseRecord()
  const handler: B4ConnectionHandler = {
    start(body) {
      expect(body.expected_revision).toBe(7)
      return {
        transaction_id: 'transaction-e2e',
        authorization_url: 'http://127.0.0.1:4173/auth/complete?ticket=ticket-e2e',
        expires_at: new Date(Date.now() + 120_000).toISOString(),
      }
    },
    enable(body) {
      expect(body.test_id).toBe('transaction-e2e')
      expect(body.verifier).toMatch(/^[\w-]{43}$/)
    },
  }
  await mockB4Connection(page, () => record, calls, handler)
  await page.route('**/api/v1/admin/auth-connections/company/check', (route) => {
    expect(route.request().postDataJSON()).toMatchObject({
      expected_revision: 7,
      changes: { oidc: { issuer: 'https://candidate.example', client_id: 'candidate-client' } },
    })
    return json(route, {
      valid: true,
      revision: 7,
      fresh_supported: true,
      issues: [],
      rules: authDiagnostics,
    })
  })
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  await drawer.getByLabel('Issuer 地址').fill('https://candidate.example')
  await drawer.getByLabel('Client ID').fill('candidate-client')
  await drawer.getByRole('button', { name: '检查配置', exact: true }).click()
  await expect(page.getByTestId('auth-connection-check-result')).toContainText('配置检查通过')
  await page.getByTestId('auth-connection-close-check').click()
  await page.route('**/api/v1/admin/auth-connections/company', (route) => {
    if (route.request().method() !== 'PATCH') return json(route, record)
    record = oidcConnection({
      aggregate_revision: 8,
      configuration: {
        ...record.configuration,
        oidc: {
          ...record.configuration.oidc!,
          issuer: 'https://candidate.example',
          client_id: 'candidate-client',
        },
      },
      validation: {
        fresh_supported: true,
        tested_at: new Date(Date.now() - 1000).toISOString(),
        applied_test_id: 'transaction-e2e',
      },
    })
    return json(route, record)
  })

  const popupPromise = page.context().waitForEvent('page')
  await drawer.getByRole('button', { name: '真实测试', exact: true }).click()
  const popup = await popupPromise
  await expect(popup).toHaveURL(/\/auth\/complete$/)
  await expect.poll(() => calls.exchange).toBe(1)
  await expect(drawer.getByText('真实测试结果')).toBeVisible()
  await expect(drawer.getByRole('button', { name: '保存配置', exact: true })).toBeEnabled()
  await drawer.getByRole('button', { name: '保存配置', exact: true }).click()
  await expect(drawer.getByRole('button', { name: '保存配置', exact: true })).toBeDisabled()
  await drawer.getByRole('button', { name: '启用', exact: true }).click()
  await expect(drawer.getByText('已启用')).toBeVisible()
  await expect.poll(() => calls.enable).toBe(1)
  await expect.poll(() => calls.exchange).toBe(1)
})

test('a non-administrator never fetches authentication configuration', async ({ page }) => {
  await mockApp(page, { role: 'user' })
  let reads = 0
  await page.route('**/api/v1/admin/auth-connections**', (route) => {
    reads++
    return json(route, { items: [] })
  })
  await page.goto('/admin/authentication')
  await expect(page).toHaveURL(/\/$/)
  expect(reads).toBe(0)
})

test('the fixed local policy uses its independent editor and never starts an external connection test', async ({
  page,
}) => {
  const calls = b4ConnectionCalls()
  await mockB4Connection(page, oidcConnection(), calls)
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-local-account-edit').click()
  const editor = page.getByTestId('auth-local-account-editor')
  await expect(editor.getByTestId('auth-local-account-email-login')).toBeDisabled()
  await expect(editor.getByTestId('auth-local-account-save')).toBeDisabled()
  await expect(editor.getByRole('button', { name: '启用', exact: true })).toHaveCount(0)
  await expect(editor.getByRole('button', { name: '停用', exact: true })).toHaveCount(0)
  await expect(editor.getByRole('button', { name: '真实测试', exact: true })).toHaveCount(0)
  expect(calls.start).toBe(0)
  expect(calls.exchange).toBe(0)

  await page.locator('.n-drawer-header__close').click()
  await expect(page.locator('.n-drawer-content')).toHaveCount(0)
})

test('a disabled state response loss reads back once and applies the new state', async ({
  page,
}) => {
  const calls = b4ConnectionCalls()
  const record = oidcConnection({ state: 'enabled' })
  const disabled = oidcConnection({ aggregate_revision: 8, state: 'disabled' })
  let reads = 0
  let disablePosts = 0
  await mockB4Connection(page, record, calls)
  await page.route('**/api/v1/admin/auth-connections/company', (route) => {
    if (route.request().method() !== 'GET') return json(route, record)
    reads += 1
    return json(route, disabled)
  })
  await page.route('**/api/v1/admin/auth-connections/company/disable', (route) => {
    disablePosts += 1
    return json(route, { title: 'Response lost' }, 503)
  })
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  await drawer.getByTestId('auth-connection-disable').click()

  await expect.poll(() => disablePosts).toBe(1)
  await expect.poll(() => reads).toBe(1)
  await expect(drawer.getByText('已停用')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-configure')).toBeEnabled()
  await expect(drawer.getByTestId('auth-connection-disable')).toHaveCount(0)
})

test('closing a dirty editor requires confirmation and discards without writing', async ({
  page,
}) => {
  const calls = b4ConnectionCalls()
  const record = oidcConnection()
  await mockB4Connection(page, record, calls)
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  await drawer.getByLabel('Issuer 地址').fill('https://candidate.example')
  await expect(drawer.getByRole('button', { name: '保存配置', exact: true })).toBeEnabled()

  await drawer.getByTestId('auth-connection-close').click()
  const confirmation = page.locator('.n-dialog:visible')
  await expect(confirmation).toContainText('存在未保存修改，关闭后这些修改将丢失。')
  await confirmation.getByRole('button', { name: '放弃并关闭' }).click()

  await expect(drawer).toHaveCount(0)
  await expect(page.getByTestId('auth-connection-company')).toBeVisible()
})

test('the drawer header close icon runs the editor close flow instead of doing nothing', async ({
  page,
}) => {
  const calls = b4ConnectionCalls()
  const record = oidcConnection()
  await mockB4Connection(page, record, calls)
  await page.goto('/admin/authentication')
  const open = () =>
    page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  const headerClose = page.locator('.n-drawer-header__close')

  await open()
  await expect(drawer.getByLabel('Issuer 地址')).toHaveValue('https://identity.example.com')
  await headerClose.click()
  await expect(drawer).toHaveCount(0)

  await open()
  await drawer.getByLabel('Issuer 地址').fill('https://candidate.example')
  await headerClose.click()
  const confirmation = page.locator('.n-dialog:visible')
  await expect(confirmation).toContainText('存在未保存修改，关闭后这些修改将丢失。')
  await confirmation.getByRole('button', { name: '继续编辑' }).click()
  await expect(drawer.getByLabel('Issuer 地址')).toHaveValue('https://candidate.example')

  await headerClose.click()
  await confirmation.getByRole('button', { name: '放弃并关闭' }).click()
  await expect(drawer).toHaveCount(0)
  expect(calls.save).toBe(0)
})
