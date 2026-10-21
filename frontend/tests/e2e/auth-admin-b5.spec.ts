import { expect, test, type Page } from '@playwright/test'
import { oidcConnection } from './auth-admin-b4-fixtures'
import { authCapabilities, json, mockApp } from './fixtures'

async function openLocalEditor(page: Page) {
  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-public-registration-state')).toHaveText('已开启')
  await expect(page.getByTestId('auth-local-account-summary')).toContainText('密码登录')
  await page.getByTestId('auth-local-account-edit').click()
  await expect(page.getByTestId('auth-local-account-editor')).toBeVisible()
}

test('keeps public registration, local account and external connections independently loaded', async ({
  page,
}) => {
  await mockApp(page)
  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-public-registration-state')).toHaveText('已开启')
  await expect(page.getByTestId('auth-public-registration-hint')).toHaveText(
    '全站允许新账号注册；各渠道仍会按自己的政策和准入规则判断是否接受。',
  )
  await expect(page.getByRole('heading', { name: '本地账号政策', exact: true })).toBeVisible()
  const localSummary = page.getByTestId('auth-local-account-summary')
  await expect(localSummary.getByText('登录', { exact: true })).toBeVisible()
  await expect(localSummary.getByText('注册与设密', { exact: true })).toBeVisible()
  await expect(page.getByTestId('auth-local-account-summary-password-login')).toContainText(
    '密码登录',
  )
  await expect(page.getByTestId('auth-local-account-summary-enrollment')).toContainText('开启')
  await expect(page.getByTestId('auth-local-account-summary-email-requirement')).toContainText(
    '可选',
  )
  await expect(page.getByText('修订 1', { exact: true })).toHaveCount(0)
  const publicBox = await page.getByTestId('auth-public-registration').boundingBox()
  const localBox = await page.getByTestId('auth-local-account-card').boundingBox()
  expect(publicBox).not.toBeNull()
  expect(localBox).not.toBeNull()
  expect(Math.abs(publicBox!.x - localBox!.x)).toBeLessThanOrEqual(1)
  expect(Math.abs(publicBox!.width - localBox!.width)).toBeLessThanOrEqual(1)
  expect(publicBox!.height).toBeLessThan(localBox!.height)
  await expect(page.getByTestId('auth-connections-refresh')).toBeVisible()
})

test('explains that closing public registration blocks every signup channel', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/admin/settings', (route) =>
    json(route, {
      settings: {
        registration_enabled: false,
        task_retention: { enabled: false, retention_days: 30, revision: 1 },
      },
    }),
  )
  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-public-registration-state')).toHaveText('已关闭')
  await expect(page.getByTestId('auth-public-registration-hint')).toHaveText(
    '全站暂停创建新账号，本地账号和第三方连接都不能注册；已有账号的登录与绑定不受影响。',
  )
})

test('keeps public registration and local policy above the third-party connections', async ({
  page,
}) => {
  await mockApp(page)
  const connection = oidcConnection()
  for (const pattern of ['**/api/v1/admin/auth-connections?*', '**/api/v1/admin/auth-connections'])
    await page.route(pattern, (route) => json(route, { items: [connection] }))

  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-public-registration-state')).toHaveText('已开启')
  const external = page.getByTestId('auth-external-connections')
  await expect(external).toBeVisible()
  await expect(page.getByTestId(`auth-connection-${connection.id}`)).toBeVisible()
  await expect(external.getByTestId('auth-connections-refresh')).toBeVisible()
  await expect(external.getByTestId('auth-connections-create')).toBeVisible()
  await expect(page.getByTestId('auth-connections-create')).toHaveCount(1)

  const top = async (testId: string) => {
    const box = await page.getByTestId(testId).boundingBox()
    expect(box).not.toBeNull()
    return box!.y
  }
  const local = page.getByTestId('auth-local-account-card')
  const localBox = await local.boundingBox()
  expect(localBox).not.toBeNull()
  expect(await top('auth-public-registration')).toBeLessThanOrEqual(
    await top('auth-local-account-card'),
  )
  expect(localBox!.y + localBox!.height).toBeLessThanOrEqual(await top('auth-external-connections'))
})

test('blocks invalid dependencies and saves the complete policy with expected revision', async ({
  page,
}) => {
  await mockApp(page)
  let patchBody: Record<string, unknown> | null = null
  await page.route('**/api/v1/admin/local-account/impact', (route) =>
    json(route, {
      affected_users: [],
      blocked_users: [],
      affected_sessions: 0,
      can_save: true,
    }),
  )
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    patchBody = route.request().postDataJSON() as Record<string, unknown>
    return json(route, {
      revision: 2,
      policy: patchBody?.policy,
      capabilities: authCapabilities.local_account,
      issues: [],
    })
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-password-login').click()
  await expect(page.getByTestId('auth-local-account-editor')).toContainText(
    '密码登录未开启；请先同时关闭首次设密和本地注册。',
  )
  await expect(page.getByTestId('auth-local-account-save')).toBeDisabled()

  await page.getByTestId('auth-local-account-enrollment').click()
  await page.getByTestId('auth-local-account-signup').click()
  await expect(page.getByTestId('auth-local-account-save')).toBeEnabled()
  await page.getByTestId('auth-local-account-save').click()

  await expect(page.getByText('本地政策已保存。')).toBeVisible()
  expect(patchBody).toMatchObject({
    expected_revision: 1,
    policy: {
      password_login_enabled: false,
      password_enrollment_enabled: false,
      username_login_enabled: true,
      email_login_enabled: false,
      signup: { enabled: false, email_requirement: 'optional', rules: { mode: 'unrestricted' } },
    },
  })
  expect(patchBody).not.toHaveProperty('changes')
})

test('keeps my draft and offers latest/reapply when a save conflicts on revision', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/admin/local-account/impact', (route) =>
    json(route, {
      affected_users: [],
      blocked_users: [],
      affected_sessions: 0,
      can_save: true,
    }),
  )
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    return json(
      route,
      {
        type: 'urn:linguaflow:auth-connection-revision-conflict',
        title: 'revision conflict',
        status: 409,
        issues: [],
        impact: { affected_users: [], blocked_users: [], affected_sessions: 0, can_save: true },
      },
      409,
    )
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-password-login').click()
  await page.getByTestId('auth-local-account-enrollment').click()
  await page.getByTestId('auth-local-account-signup').click()
  await page.getByTestId('auth-local-account-save').click()

  await expect(page.getByTestId('auth-local-account-conflict')).toBeVisible()
  await expect(page.getByTestId('auth-local-account-conflict-latest')).toBeVisible()
  await expect(page.getByTestId('auth-local-account-conflict-reapply')).toBeVisible()
})

test('titles the local policy drawer once without repeating it in the editor body', async ({
  page,
}) => {
  await mockApp(page)
  await openLocalEditor(page)

  await expect(page.locator('.n-drawer-header__main')).toHaveText('固定本地账号政策')
  await expect(page.getByTestId('auth-local-account-editor')).toContainText(
    '配置已有账号登录、首次设密和本地注册。',
  )
  const headings = await page.locator('.n-drawer-body h1, .n-drawer-body h2').allTextContents()
  expect(headings.map((text) => text.trim())).toEqual(['登录', '首次设密', '注册'])
})
