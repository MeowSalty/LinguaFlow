import { expect, test, type Page } from '@playwright/test'
import { json, mockApp, testUser } from './fixtures'

const accountASession = {
  user: testUser('admin', 9),
  access_token: 'account-a-access',
  refresh_token: 'account-a-refresh',
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
}
const accountBSession = {
  user: testUser('user', 2),
  access_token: 'account-b-access',
  refresh_token: 'account-b-refresh',
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
}

async function anonymousOnce(page: Page) {
  await mockApp(page, { initializeAuthOnce: true })
  await page.addInitScript(() => {
    if (localStorage.getItem('fixture.auth.anonymous.initialized')) return
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
    localStorage.setItem('fixture.auth.anonymous.initialized', '1')
  })
}

async function routeCurrentUserByToken(page: Page) {
  await page.route('**/api/v1/users/me', (route) => {
    const authorization = route.request().headers().authorization
    if (authorization === 'Bearer account-b-access') return json(route, testUser('user', 2))
    if (authorization === 'Bearer account-a-access') return json(route, testUser('admin', 9))
    return json(route, { title: 'No authenticated session' }, 401)
  })
}

test('a different account logging in in another tab clears and rejects a held login response', async ({
  page,
}) => {
  type CrossTabObservation = {
    passwordInputPresent: boolean
    passwordValue: string | null
    accessToken: string | null
    refreshToken: string | null
    authEpoch: string | null
  }
  let resolveObservation!: (observation: CrossTabObservation) => void
  const observedCrossTabChange = new Promise<CrossTabObservation>((resolve) => {
    resolveObservation = resolve
  })
  let holdAuthReload = false
  let authReloadIntercepted = false
  let releaseAuthReload!: () => void
  const heldAuthReload = new Promise<void>((resolve) => {
    releaseAuthReload = resolve
  })
  let markAuthReloadStarted!: () => void
  const authReloadStarted = new Promise<void>((resolve) => {
    markAuthReloadStarted = resolve
  })
  await page.exposeBinding('__reportCrossTabLoginContext', (_source, value) => {
    resolveObservation(value as CrossTabObservation)
  })
  await page.route('**/*', async (route) => {
    if (holdAuthReload && route.request().isNavigationRequest()) {
      authReloadIntercepted = true
      markAuthReloadStarted()
      await heldAuthReload
    }
    await route.continue()
  })
  await anonymousOnce(page)
  await page.addInitScript(() => {
    window.addEventListener('storage', (event) => {
      if (event.key !== 'linguaflow.auth_epoch') return
      queueMicrotask(() =>
        queueMicrotask(() => {
          window.setTimeout(() => {
            const input = document.querySelector<HTMLInputElement>(
              'input[autocomplete="current-password"]',
            )
            const report = (
              window as Window & {
                __reportCrossTabLoginContext?: (value: {
                  passwordInputPresent: boolean
                  passwordValue: string | null
                  accessToken: string | null
                  refreshToken: string | null
                  authEpoch: string | null
                }) => void
              }
            ).__reportCrossTabLoginContext
            report?.({
              passwordInputPresent: input !== null,
              passwordValue: input?.value ?? null,
              accessToken: localStorage.getItem('linguaflow.access_token'),
              refreshToken: localStorage.getItem('linguaflow.refresh_token'),
              authEpoch: event.newValue,
            })
          })
        }),
      )
    })
  })
  await routeCurrentUserByToken(page)
  await page.goto('/login')
  await expect(page.locator('input[autocomplete="current-password"]')).toBeVisible()

  const other = await page.context().newPage()
  await anonymousOnce(other)
  await routeCurrentUserByToken(other)
  await other.route('**/api/v1/auth/login', (route) => json(route, accountBSession))
  await other.goto('/login')
  await expect(other.locator('input[autocomplete="current-password"]')).toBeVisible()

  let releaseA!: () => void
  const heldA = new Promise<void>((resolve) => {
    releaseA = resolve
  })
  let markAStarted!: () => void
  const aStarted = new Promise<void>((resolve) => {
    markAStarted = resolve
  })
  let aPayload: Record<string, unknown> | undefined
  let aResponseReleased = false
  await page.route('**/api/v1/auth/login', async (route) => {
    aPayload = route.request().postDataJSON() as Record<string, unknown>
    markAStarted()
    await heldA
    await json(route, accountASession)
    aResponseReleased = true
  })

  const passwordA = page.locator('input[autocomplete="current-password"]')
  await page.locator('input[autocomplete="username"]').fill('account-a')
  await passwordA.fill('secret-for-account-a')
  const bReloaded = other.waitForEvent('load')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await aStarted
  await bReloaded
  await expect(passwordA).toHaveValue('secret-for-account-a')
  await expect(other.locator('input[autocomplete="current-password"]')).toBeVisible()

  await other.locator('input[autocomplete="username"]').fill('account-b')
  await other.locator('input[autocomplete="current-password"]').fill('secret-for-account-b')
  holdAuthReload = true
  await other.getByRole('button', { name: '登录', exact: true }).click()
  await expect(other.getByRole('button', { name: 'Test 2', exact: true })).toBeVisible()
  const observation = await observedCrossTabChange
  await expect.poll(() => authReloadIntercepted).toBe(true)
  if (observation.passwordInputPresent) expect(observation.passwordValue).toBe('')
  else expect(observation.passwordValue).toBeNull()
  expect(observation.authEpoch).toBeTruthy()
  expect(aPayload).toEqual({ identifier: 'account-a', password: 'secret-for-account-a' })
  releaseA()
  await expect.poll(() => aResponseReleased).toBe(true)
  // Both tabs share storage; read the settled tab while A's real reload is held.
  expect(await other.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'account-b-access',
  )
  expect(await other.evaluate(() => localStorage.getItem('linguaflow.refresh_token'))).toBe(
    'account-b-refresh',
  )

  releaseAuthReload()
  await authReloadStarted
  await expect(page.getByRole('button', { name: 'Test 2', exact: true })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'account-b-access',
  )
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.refresh_token'))).toBe(
    'account-b-refresh',
  )

  await other.close()
})
