import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'

const policy = (enabled: boolean) => ({
  settings: {
    registration_enabled: enabled,
    task_retention: { enabled: false, retention_days: 30, revision: 1 },
  },
})
const registrationPatch = (enabled: boolean) => ({ settings: { registration_enabled: enabled } })
const toggle = (page: Page) => page.getByRole('switch', { name: '允许公开注册' })
const save = (page: Page) => page.getByRole('button', { name: /保存注册设置$/ })
const refresh = (page: Page) => page.getByRole('button', { name: /^(loading )?刷新$/ })

test('registration settings load a confirmed value and send true and false as booleans', async ({
  page,
}) => {
  await mockApp(page)
  const writes: unknown[] = []
  await page.route('**/api/v1/admin/settings', (route) => {
    if (route.request().method() === 'PATCH') {
      const body: unknown = route.request().postDataJSON()
      writes.push(body)
      return json(
        route,
        policy((body as ReturnType<typeof registrationPatch>).settings.registration_enabled),
      )
    }
    return json(route, policy(false))
  })
  await page.goto('/admin/settings')
  await expect(toggle(page)).not.toBeChecked()
  await expect(save(page)).toBeDisabled()
  await toggle(page).click()
  await save(page).click()
  await expect(save(page)).toBeDisabled()
  expect(writes).toEqual([registrationPatch(true)])
  await toggle(page).click()
  await save(page).click()
  await expect(save(page)).toBeDisabled()
  expect(writes).toEqual([registrationPatch(true), registrationPatch(false)])
  await expect(page.getByText('保存后影响后续注册请求；重启不会重置此设置。')).toBeVisible()
  await expect(page.getByRole('button', { name: '添加配置项' })).toHaveCount(0)
})

test('initial read failure does not manufacture a disabled registration policy', async ({
  page,
}) => {
  await mockApp(page)
  let failed = true
  let writes = 0
  await page.route('**/api/v1/admin/settings', (route) => {
    if (route.request().method() === 'PATCH') writes++
    return failed
      ? json(route, { status: 503, title: 'unavailable' }, 503)
      : json(route, policy(true))
  })
  await page.goto('/admin/settings')
  await expect(page.getByText('读取注册政策失败，请重试。', { exact: true })).toBeVisible()
  await expect(toggle(page)).toHaveCount(0)
  await expect(save(page)).toHaveCount(0)
  failed = false
  await refresh(page).click()
  await expect(toggle(page)).toBeChecked()
  await expect(save(page)).toBeDisabled()
  expect(writes).toBe(0)
})

test('refresh cancellation sends no request and failed refresh retains the unsaved draft', async ({
  page,
}) => {
  await mockApp(page)
  let reads = 0
  let failRefresh = true
  await page.route('**/api/v1/admin/settings', (route) => {
    reads++
    return reads > 1 && failRefresh
      ? json(route, { status: 503, title: 'unavailable' }, 503)
      : json(route, policy(false))
  })
  await page.goto('/admin/settings')
  await expect(toggle(page)).not.toBeChecked()
  await toggle(page).click()
  await refresh(page).click()
  await page.getByRole('button', { name: '取消', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(reads).toBe(1)
  await expect(toggle(page)).toBeChecked()
  await refresh(page).click()
  await page.getByRole('button', { name: '丢弃修改并刷新', exact: true }).click()
  await expect(
    page.getByText('刷新失败，已保留当前草稿和上次确认的政策。', { exact: true }),
  ).toBeVisible()
  await expect(toggle(page)).toBeChecked()
  await expect(save(page)).toBeEnabled()
  failRefresh = false
  await refresh(page).click()
  await page.getByRole('button', { name: '丢弃修改并刷新', exact: true }).click()
  await expect(toggle(page)).not.toBeChecked()
  await expect(save(page)).toBeDisabled()
  expect(reads).toBe(3)
})

test('saving serializes controls and a failure retains a retryable draft', async ({ page }) => {
  await mockApp(page)
  let finish!: () => void
  let writes = 0
  await page.route('**/api/v1/admin/settings', async (route) => {
    if (route.request().method() === 'PATCH') {
      writes++
      await new Promise<void>((resolve) => {
        finish = resolve
      })
      return json(route, { status: 503, title: 'unavailable' }, 503)
    }
    return json(route, policy(false))
  })
  await page.goto('/admin/settings')
  await toggle(page).click()
  await save(page).click()
  await expect.poll(() => writes).toBe(1)
  await expect(toggle(page)).toBeDisabled()
  await expect(refresh(page)).toBeDisabled()
  await expect(save(page)).toBeDisabled()
  finish()
  await expect(page.getByText('保存失败，已保留当前草稿，请重试。', { exact: true })).toBeVisible()
  await expect(toggle(page)).toBeChecked()
  await expect(save(page)).toBeEnabled()
  expect(writes).toBe(1)
})

test('returning while the first policy read is pending adopts its confirmed result', async ({
  page,
}) => {
  await mockApp(page)
  let finish!: () => void
  let reads = 0
  await page.route('**/api/v1/admin/settings', async (route) => {
    reads++
    await new Promise<void>((resolve) => {
      finish = resolve
    })
    return json(route, policy(true))
  })
  await page.goto('/admin/settings')
  await expect.poll(() => reads).toBe(1)
  const menu = page.getByRole('button', { name: '菜单', exact: true })
  if (await menu.isVisible()) {
    await menu.click()
    await page.getByRole('button', { name: '工作台', exact: true }).click()
  } else await page.getByRole('link', { name: '工作台', exact: true }).click()
  await expect(page).toHaveURL(/\/$/)
  await page.goBack()
  await expect(page).toHaveURL(/\/admin\/settings$/)
  finish()
  await expect(toggle(page)).toBeChecked()
  await expect(save(page)).toBeDisabled()
  expect(reads).toBe(1)
})

for (const [status, expected] of [
  [403, '注册已关闭，请联系管理员。'],
  [503, '服务暂时不可用，请稍后重试。'],
] as const) {
  test(`registration ${status} shows its own message without replay or admin policy lookup`, async ({
    page,
  }) => {
    await mockApp(page)
    await page.addInitScript(() => {
      localStorage.removeItem('linguaflow.access_token')
      localStorage.removeItem('linguaflow.refresh_token')
    })
    let writes = 0
    let adminReads = 0
    await page.route('**/api/v1/admin/settings', (route) => {
      adminReads++
      return json(route, policy(true))
    })
    await page.route('**/api/v1/auth/register', (route) => {
      writes++
      expect(route.request().postDataJSON()).not.toHaveProperty('role')
      return json(route, { status, title: 'upstream message' }, status)
    })
    await page.goto('/register')
    await page.locator('input[autocomplete="username"]').fill('new-user')
    await page.getByPlaceholder('you@example.com').fill('new@example.com')
    await page.locator('input[autocomplete="new-password"]').nth(0).fill('password123')
    await page.locator('input[autocomplete="new-password"]').nth(1).fill('password123')
    await page.getByRole('button', { name: '注册并登录', exact: true }).click()
    await expect(page.getByText(expected, { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: '注册并登录', exact: true })).toBeEnabled()
    await expect(page.locator('input[autocomplete="username"]')).toHaveValue('new-user')
    expect(writes).toBe(1)
    expect(adminReads).toBe(0)
    await expect(page).toHaveURL(/\/register$/)
  })
}
