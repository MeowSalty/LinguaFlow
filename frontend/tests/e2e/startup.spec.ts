import { expect, test, type Page } from '@playwright/test'

type StartupState = 'first-visit' | 'signed-out' | 'signed-in' | 'local'
type ApiRequest = { path: string; authorization: string | undefined }

const counts = {
  pending: 0,
  running: 0,
  pausing: 0,
  paused: 0,
  waiting_retry: 0,
  needs_action: 0,
  recent_failed: 0,
}
const operationsSummary = {
  total: counts,
  by_type: { translation: counts, glossary_sync: counts, storage: counts },
  recent_failed_since: '2026-09-29T00:00:00Z',
  as_of: '2026-09-30T00:00:00Z',
}

const user = (id = 1) => ({
  id,
  username: `startup-${id}`,
  display_name: `Startup user ${id}`,
  email: `startup-${id}@example.com`,
  role: 'user',
  active: true,
})

const session = (id: number) => ({
  user: user(id),
  access_token: `access-${id}`,
  refresh_token: `refresh-${id}`,
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
})

/** Race navigation/render assertions against pageerror so a white screen fails at its cause. */
async function withoutPageErrors(page: Page, run: () => Promise<void>) {
  const errors: Error[] = []
  let rejectPageError!: (error: Error) => void
  const pageError = new Promise<never>((_, reject) => {
    rejectPageError = reject
  })
  const onError = (error: Error) => {
    errors.push(error)
    rejectPageError(error)
  }
  page.on('pageerror', onError)
  try {
    await Promise.race([run(), pageError])
    expect(errors).toEqual([])
  } finally {
    page.off('pageerror', onError)
  }
}

async function mockStartup(page: Page, state: StartupState) {
  const requests: ApiRequest[] = []
  await page.addInitScript((initialState: StartupState) => {
    // Seed once per tab: reload must verify the app's persisted state, not reseed credentials.
    if (sessionStorage.getItem('startup-seeded')) return
    sessionStorage.setItem('startup-seeded', '1')
    localStorage.setItem('linguaflow.locale', 'zh-Hans')
    localStorage.setItem('linguaflow.theme', 'light')
    if (initialState !== 'first-visit' && initialState !== 'local')
      localStorage.setItem('linguaflow.api_base_url', '/api/v1')
    if (initialState === 'signed-in' || initialState === 'local') {
      localStorage.setItem('linguaflow.access_token', 'access-1')
      localStorage.setItem('linguaflow.refresh_token', 'refresh-1')
    }
  }, state)
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
    const authorization = route.request().headers().authorization
    requests.push({ path, authorization })
    const respond = (body: unknown) => route.fulfill({ status: 200, json: body })
    switch (path) {
      case '/ping':
        return respond({ status: 'ok', service: 'Startup service' })
      case '/mode':
        return respond({ mode: state === 'local' ? 'local' : 'server' })
      case '/users/me':
        return respond(user(authorization === 'Bearer access-2' ? 2 : 1))
      case '/auth/login':
        return respond(session(2))
      case '/auth/logout':
        return route.fulfill({ status: 204 })
      case '/operations/summary':
        return respond(operationsSummary)
      default:
        return respond({ items: [], total: 0 })
    }
  })
  return requests
}

const startupCases = [
  { state: 'first-visit', path: '/service', heading: '选择 LinguaFlow 服务器' },
  { state: 'signed-out', path: '/login', heading: '登录' },
  { state: 'signed-in', path: '/', heading: '工作台' },
  { state: 'local', path: '/', heading: '工作台' },
] as const

for (const scenario of startupCases) {
  test(`${scenario.state} mounts and survives a reload`, async ({ page }) => {
    const requests = await mockStartup(page, scenario.state)
    await withoutPageErrors(page, async () => {
      const assertReady = async () => {
        await expect(page).toHaveURL((url) => url.pathname === scenario.path)
        await expect(
          page.getByRole('heading', { name: scenario.heading, exact: true }),
        ).toBeVisible()
        if (scenario.state === 'signed-in' || scenario.state === 'local')
          await expect(
            page.getByRole('button', { name: 'Startup user 1', exact: true }),
          ).toBeVisible()
      }
      await page.goto('/')
      await assertReady()
      const initialUserRequests = requests.filter((request) => request.path === '/users/me').length
      await page.reload()
      await assertReady()
      const userRequests = requests.filter((request) => request.path === '/users/me')
      if (scenario.state === 'signed-in' || scenario.state === 'local') {
        expect(initialUserRequests).toBeGreaterThan(0)
        expect(userRequests.length).toBeGreaterThan(initialUserRequests)
      }
      if (scenario.state === 'signed-in') {
        expect(userRequests.every((request) => request.authorization === 'Bearer access-1')).toBe(
          true,
        )
      } else if (scenario.state === 'local') {
        expect(userRequests.every((request) => request.authorization === undefined)).toBe(true)
        expect(
          await page.evaluate(() => localStorage.getItem('linguaflow.access_token')),
        ).toBeNull()
      } else {
        expect(userRequests).toHaveLength(0)
      }
    })
  })
}

test('login and logout persist across reload without restoring the previous account', async ({
  page,
}) => {
  const requests = await mockStartup(page, 'signed-in')
  await withoutPageErrors(page, async () => {
    await page.goto('/settings/security')
    await page.getByRole('button', { name: '退出当前登录', exact: true }).click()
    await expect(page).toHaveURL(/\/login$/)
    await page.reload()
    await expect(page.getByRole('heading', { name: '登录', exact: true })).toBeVisible()
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBeNull()
    await page.locator('input[autocomplete="username"]').fill('startup-2')
    await page.locator('input[autocomplete="current-password"]').fill('password')
    await page.getByRole('button', { name: '登录', exact: true }).click()
    await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Startup user 2', exact: true })).toBeVisible()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Startup user 2', exact: true })).toBeVisible()
    expect(requests.some((request) => request.path === '/auth/logout')).toBe(true)
    expect(requests.filter((request) => request.path === '/users/me').at(-1)?.authorization).toBe(
      'Bearer access-2',
    )
  })
})

test('switching services replaces the identity and clears credentials before new requests', async ({
  page,
}) => {
  await mockStartup(page, 'signed-in')
  const nextServiceRequests: ApiRequest[] = []
  await page.route('**/second-api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/second-api/v1', '')
    nextServiceRequests.push({ path, authorization: route.request().headers().authorization })
    let body: unknown = { items: [], total: 0 }
    if (path === '/ping') body = { status: 'ok', service: 'Second service' }
    if (path === '/mode') body = { mode: 'local' }
    if (path === '/users/me') body = user(2)
    if (path === '/operations/summary') body = operationsSummary
    return route.fulfill({ status: 200, json: body })
  })
  await withoutPageErrors(page, async () => {
    await page.goto('/')
    await page.getByRole('button', { name: 'Startup user 1', exact: true }).click()
    await page.getByText('切换服务器', { exact: true }).click()
    await page.locator('input[autocomplete="off"]').fill('/second-api/v1')
    await page.getByRole('button', { name: '连接', exact: true }).click()
    await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Startup user 2', exact: true })).toBeVisible()
    await page.reload()
    await expect(page.getByRole('button', { name: 'Startup user 2', exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Startup user 1', exact: true })).toHaveCount(0)
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.api_base_url'))).toBe(
      new URL('/second-api/v1', page.url()).href,
    )
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBeNull()
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.refresh_token'))).toBeNull()
    expect(nextServiceRequests.some((request) => request.path === '/users/me')).toBe(true)
    expect(nextServiceRequests.every((request) => request.authorization === undefined)).toBe(true)
  })
})
