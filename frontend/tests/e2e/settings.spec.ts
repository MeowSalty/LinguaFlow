import { expect, test } from '@playwright/test'
import { json, mockApp, summaryFixture, testUser } from './fixtures'

test('profile update sends only changed fields and explicitly clears an empty display name', async ({
  page,
}) => {
  await mockApp(page)
  let payload: unknown
  await page.route('**/api/v1/users/me', (route) => {
    if (route.request().method() === 'PUT') {
      payload = route.request().postDataJSON()
      return json(route, { ...testUser(), display_name: '' })
    }
    return json(route, testUser())
  })
  await page.goto('/profile')
  await expect(page).toHaveURL(/\/settings\/profile$/)
  await page.locator('input[autocomplete="name"]').fill('')
  await page.getByRole('button', { name: '保存资料', exact: true }).click()
  await expect.poll(() => payload).toEqual({ display_name: '' })
  await expect(page.locator('input[autocomplete="email"]')).toHaveValue('tester-1@example.com')
})

test('profile conflict retains edits and the authenticated session', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/users/me', (route) =>
    route.request().method() === 'PUT'
      ? json(route, { title: '邮箱已被使用', status: 409 }, 409)
      : json(route, testUser()),
  )
  await page.goto('/settings/profile')
  await page.locator('input[autocomplete="email"]').fill('taken@example.com')
  await page.getByRole('button', { name: '保存资料', exact: true }).click()
  await expect(page.getByText('邮箱已被使用', { exact: true })).toBeVisible()
  await expect(page.locator('input[autocomplete="email"]')).toHaveValue('taken@example.com')
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'test-access',
  )
})

test('password mismatch is a field error and preserves untrimmed password input and login', async ({
  page,
}) => {
  await mockApp(page)
  let payload: unknown
  await page.route('**/api/v1/users/me/password', (route) => {
    payload = route.request().postDataJSON()
    return json(
      route,
      {
        title: 'current_password_mismatch',
        type: 'urn:linguaflow:problem:current-password-mismatch',
        status: 400,
      },
      400,
    )
  })
  await page.goto('/settings/security')
  await page.locator('input[autocomplete="current-password"]').fill(' wrong password ')
  await page.locator('input[autocomplete="new-password"]').nth(0).fill(' 12345678 ')
  await page.locator('input[autocomplete="new-password"]').nth(1).fill(' 12345678 ')
  await page.getByRole('button', { name: '修改密码', exact: true }).click()
  await expect(page.getByText('当前密码不正确', { exact: true })).toBeVisible()
  expect(payload).toEqual({ current_password: ' wrong password ', new_password: ' 12345678 ' })
  await expect(page.locator('input[autocomplete="new-password"]').first()).toHaveValue(' 12345678 ')
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'test-access',
  )
})

test('local settings expose read-only profile and a working service connection entry', async ({
  page,
}) => {
  await mockApp(page, { mode: 'local' })
  await page.goto('/settings/profile')
  await expect(page.getByText('当前为本地免登录模式，身份资料只读。')).toBeVisible()
  await expect(page.locator('input[autocomplete="email"]')).toBeDisabled()
  await expect(page.locator('input[autocomplete="name"]')).toBeDisabled()
  await expect(page.getByRole('link', { name: '团队与组织', exact: true })).toHaveCount(0)
  await page.getByRole('link', { name: '账号安全', exact: true }).click()
  await expect(page.getByText('本地模式无需密码登录。可前往服务连接页切换至服务器。')).toBeVisible()
  await page.getByRole('button', { name: '服务连接', exact: true }).click()
  await expect(page).toHaveURL(/\/service\?force=1/)
})

test('home failure recovery uses the exact server-provided timestamp window', async ({ page }) => {
  await mockApp(page)
  const failedRequest = page.waitForRequest((request) => {
    const url = new URL(request.url())
    return url.pathname.endsWith('/operations') && url.searchParams.get('status') === 'failed'
  })
  await page.goto('/')
  const url = new URL((await failedRequest).url())
  expect(url.searchParams.get('updated_from')).toBe(summaryFixture.recent_failed_since)
  expect(url.searchParams.get('updated_before')).toBe(summaryFixture.as_of)
  expect(url.searchParams.get('limit')).toBe('3')
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
})

test('failed login retains the entered credentials without remounting the form', async ({
  page,
}) => {
  await mockApp(page)
  await page.addInitScript(() => {
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
  })
  await page.route('**/api/v1/auth/login', (route) =>
    json(route, { title: '用户名或密码错误', status: 401 }, 401),
  )
  await page.goto('/login')
  await page.locator('input[autocomplete="username"]').fill('remembered-user')
  await page.locator('input[autocomplete="current-password"]').fill('remembered-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByText('用户名或密码错误', { exact: true })).toBeVisible()
  await expect(page.locator('input[autocomplete="username"]')).toHaveValue('remembered-user')
  await expect(page.locator('input[autocomplete="current-password"]')).toHaveValue(
    'remembered-password',
  )
})
