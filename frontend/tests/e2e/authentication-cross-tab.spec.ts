import { expect, test } from '@playwright/test'
import { json, mockApp, testUser } from './fixtures'

test('another tab logging out clears the current private page without restoring old tokens', async ({
  page,
}) => {
  await mockApp(page, { initializeAuthOnce: true })
  await page.goto('/settings/preferences')
  const other = await page.context().newPage()
  await mockApp(other, { initializeAuthOnce: true })
  await other.goto('/settings/preferences')
  await other.evaluate(() => {
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
    localStorage.setItem('linguaflow.auth_epoch', 'logged-out-in-another-tab')
  })
  await expect(page).toHaveURL(/\/login/)
  await expect(page.locator('input[autocomplete="username"]')).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBeNull()
  await other.close()
})

test('two tabs recover an expired access token with exactly one refresh', async ({ page }) => {
  const other = await page.context().newPage()
  let expired = false
  let refreshes = 0
  for (const target of [page, other]) {
    await mockApp(target, { initializeAuthOnce: true })
    await target.route('**/api/v1/users/me', (route) =>
      expired && route.request().headers().authorization !== 'Bearer rotated-access'
        ? json(route, { title: 'Expired', status: 401 }, 401)
        : json(route, testUser()),
    )
    await target.route('**/api/v1/auth/refresh', (route) => {
      refreshes++
      return json(route, {
        user: testUser(),
        access_token: 'rotated-access',
        refresh_token: 'rotated-refresh',
        token_type: 'Bearer',
        expires_at: '2099-01-01T00:00:00Z',
        refresh_expires_at: '2099-02-01T00:00:00Z',
      })
    })
    await target.goto('/settings/preferences')
    await expect(target.getByRole('button', { name: 'Test 1', exact: true })).toBeVisible()
  }
  expired = true
  await Promise.all([page.reload(), other.reload()])
  for (const target of [page, other])
    await expect(target.getByRole('button', { name: 'Test 1', exact: true })).toBeVisible()
  expect(refreshes).toBe(1)
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.refresh_token'))).toBe(
    'rotated-refresh',
  )
  await other.close()
})

test('logout waits for another tab rotation and revokes the latest token pair', async ({
  page,
}) => {
  const other = await page.context().newPage()
  let expired = false
  let refreshes = 0
  let release!: () => void
  const held = new Promise<void>((resolve) => {
    release = resolve
  })
  const logouts: { access: string | undefined; refresh: string }[] = []
  for (const target of [page, other]) {
    await mockApp(target, { initializeAuthOnce: true })
    await target.route('**/api/v1/users/me', (route) =>
      expired && route.request().headers().authorization !== 'Bearer rotated-access'
        ? json(route, { title: 'Expired', status: 401 }, 401)
        : json(route, testUser()),
    )
    await target.route('**/api/v1/auth/refresh', async (route) => {
      refreshes++
      await held
      if (refreshes > 1) return json(route, { title: 'Replayed token', status: 401 }, 401)
      return json(route, {
        user: testUser(),
        access_token: 'rotated-access',
        refresh_token: 'rotated-refresh',
        token_type: 'Bearer',
        expires_at: '2099-01-01T00:00:00Z',
        refresh_expires_at: '2099-02-01T00:00:00Z',
      })
    })
    await target.route('**/api/v1/auth/logout', (route) => {
      logouts.push({
        access: route.request().headers().authorization,
        refresh: route.request().postDataJSON().refresh_token,
      })
      return route.request().headers().authorization === 'Bearer rotated-access'
        ? route.fulfill({ status: 204 })
        : json(route, { title: 'Expired', status: 401 }, 401)
    })
    await target.goto('/settings/security')
    await expect(target.getByRole('button', { name: '退出当前登录', exact: true })).toBeVisible()
  }
  expired = true
  const reloading = page.reload().catch(() => {})
  await expect.poll(() => refreshes).toBe(1)
  await other.getByRole('button', { name: '退出当前登录', exact: true }).click()
  release()
  await reloading
  await expect(other).toHaveURL(/\/login/)
  await expect(page).toHaveURL(/\/login/)
  expect(refreshes).toBe(1)
  expect(logouts).toEqual([{ access: 'Bearer rotated-access', refresh: 'rotated-refresh' }])
  await other.close()
})
