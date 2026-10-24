import { expect, test, type Page } from '@playwright/test'
import { authCapabilities, json, loginMethods, summaryFixture, testUser } from './fixtures'

const verifier = 'v'.repeat(43)
const issuedSession = {
  user: testUser('user', 42),
  access_token: 'callback-access',
  refresh_token: 'callback-refresh',
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
}
const confirmation = {
  kind: 'signup_confirmation',
  transaction_id: 'tx-callback',
  confirmation_token: 'private-confirmation',
  expires_at: '2099-01-01T00:00:00Z',
  signup_requirements: ['local_password'],
  signup: { email_requirement: 'optional', password_requirement: 'required' },
  profile: {
    connection_id: 'company',
    connection_name: 'Company',
    external_display: 'External member',
  },
}
type RequestRecord = { path: string; time: number; authorization?: string; body?: unknown }

async function setupCallback(
  page: Page,
  options: {
    protected?: boolean
    flow?: Record<string, unknown>
    exchange?: unknown
    theme?: 'light' | 'dark'
  } = {},
): Promise<RequestRecord[]> {
  const requests: RequestRecord[] = []
  await page.addInitScript(
    ({ protectedFlow, patch, theme }) => {
      if (sessionStorage.getItem('callback.fixture.seeded')) return
      sessionStorage.setItem('callback.fixture.seeded', '1')
      localStorage.setItem('linguaflow.api_base_url', '/api/v1')
      localStorage.setItem('linguaflow.auth_epoch', 'callback-epoch')
      localStorage.setItem('linguaflow.theme', theme)
      if (protectedFlow) {
        localStorage.setItem('linguaflow.access_token', 'expired-access')
        localStorage.setItem('linguaflow.refresh_token', 'original-refresh')
      } else {
        localStorage.removeItem('linguaflow.access_token')
        localStorage.removeItem('linguaflow.refresh_token')
      }
      sessionStorage.setItem(
        'linguaflow.external-flow.v2',
        JSON.stringify({
          baseUrl: new URL('/api/v1', location.origin).href,
          epoch: 'callback-epoch',
          userId: protectedFlow ? 42 : null,
          purpose: protectedFlow ? 'link' : 'login',
          connectionId: 'company',
          transactionId: 'tx-callback',
          expiresAt: new Date(Date.now() + 300_000).toISOString(),
          returnTo: '/settings/profile',
          stage: 'started',
          verifier: 'v'.repeat(43),
          ...patch,
        }),
      )
    },
    {
      protectedFlow: options.protected ?? false,
      patch: options.flow ?? {},
      theme: options.theme ?? 'light',
    },
  )
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace('/api/v1', '')
    requests.push({
      path,
      time: Date.now(),
      authorization: request.headers().authorization,
      body: request.method() === 'POST' ? request.postDataJSON() : undefined,
    })
    if (path === '/ping') return json(route, { status: 'ok', service: 'Test service' })
    if (path === '/mode') return json(route, { mode: 'server' })
    if (path === '/users/me') {
      if (request.headers().authorization === 'Bearer expired-access')
        return json(route, { title: 'Unauthorized' }, 401)
      return json(route, issuedSession.user)
    }
    if (path === '/auth/refresh')
      return json(route, {
        ...issuedSession,
        access_token: 'rotated-access',
        refresh_token: 'rotated-refresh',
      })
    if (path === '/auth/external/exchange')
      return json(route, options.exchange ?? { kind: 'session', session: issuedSession })
    if (path === '/auth/external/complete')
      return json(route, { kind: 'session', session: issuedSession })
    if (path === '/auth/capabilities') return json(route, authCapabilities)
    if (path === '/users/me/login-methods') return json(route, loginMethods)
    if (path === '/operations/summary') return json(route, summaryFixture)
    return json(route, { items: [], total: 0 })
  })
  return requests
}

/** Model a 90-second stall, then release early after proving the critical work completed. */
async function holdResources(page: Page, entry = false) {
  const requested: Array<{ url: string; time: number }> = []
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  const timeout = setTimeout(release, 90_000)
  const pattern = entry
    ? /\/(?:assets\/index-[^/]+\.js|src\/entry\.ts)(?:\?.*)?$/
    : /\/(?:assets\/(?:main-|i18n-(?!runtime))[^/]+\.(?:js|css)|src\/(?:main|i18n)\.ts)(?:\?.*)?$/
  await page.route(pattern, async (route) => {
    requested.push({ url: route.request().url(), time: Date.now() })
    await gate
    await route.continue().catch(() => undefined)
  })
  return {
    requested,
    release: () => {
      clearTimeout(timeout)
      release()
    },
  }
}

async function expectNoCallbackSecrets(page: Page) {
  const state = await page.evaluate(() => ({
    url: location.href,
    ticket: '__linguaflowAuthTicket' in window,
    flow: sessionStorage.getItem('linguaflow.external-flow.v2'),
    storage: [localStorage, sessionStorage]
      .flatMap((storage) => Object.keys(storage).map((key) => `${key}=${storage.getItem(key)}`))
      .join('\n'),
    text: document.body.textContent,
  }))
  expect(state.ticket).toBe(false)
  expect(state.flow).toBeNull()
  for (const secret of ['test-ticket', verifier, 'private-confirmation', 'private-proof']) {
    expect(state.url).not.toContain(secret)
    expect(state.storage).not.toContain(secret)
    expect(state.text).not.toContain(secret)
  }
}

test('HTML paints and shows slow-network help while even the entry script is stalled', async ({
  page,
}, testInfo) => {
  const requests = await setupCallback(page, { theme: 'dark' })
  await page.setViewportSize({ width: 390, height: 844 })
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  await page.clock.install()
  const hold = await holdResources(page, true)
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect(page.getByRole('status')).toContainText('正在确认认证结果')
    await expect(page.locator('#auth-startup')).toBeVisible()
    expect(requests).toHaveLength(0)
    await page.clock.runFor(10_001)
    await expect(page.locator('#auth-startup-slow')).toBeVisible()
    expect(
      await page.locator('.auth-spinner').evaluate((node) => getComputedStyle(node).animationName),
    ).toBe('none')
    expect(await page.evaluate(() => document.documentElement.dataset.theme)).toBe('dark')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath('callback-html-dark-390.png') })
  } finally {
    hold.release()
  }
})

test('an entry download failure has visible recovery without mounting Vue', async ({ page }) => {
  const requests = await setupCallback(page)
  await page.route(/\/(?:assets\/index-[^/]+\.js|src\/entry\.ts)(?:\?.*)?$/, (route) =>
    route.abort(),
  )
  await page.goto('/auth/complete?ticket=test-ticket')
  await expect(page.getByRole('status')).toContainText('无法加载认证页面')
  await expect(page.getByRole('link', { name: '返回登录', exact: true })).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(page.getByRole('link', { name: '返回登录', exact: true })).toBeFocused()
  expect(requests).toHaveLength(0)
  await expectNoCallbackSecrets(page)
})

test('a missing ticket fails immediately without service probes or business resources', async ({
  page,
}) => {
  const requests = await setupCallback(page)
  const hold = await holdResources(page)
  try {
    await page.goto('/auth/complete')
    await expect(page.getByRole('status')).toContainText('认证流程已失效')
    expect(requests).toHaveLength(0)
    expect(hold.requested).toHaveLength(0)
    await expectNoCallbackSecrets(page)
  } finally {
    hold.release()
  }
})

test('login exchanges before stalled main JS/CSS, then continues in the same document', async ({
  page,
}, testInfo) => {
  const requests = await setupCallback(page)
  const hold = await holdResources(page)
  const started = Date.now()
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect(page.getByRole('status')).toContainText('认证已验证', { timeout: 10_000 })
    await expect.poll(() => hold.requested.length).toBeGreaterThan(0)
    const exchange = requests.find((request) => request.path === '/auth/external/exchange')!
    expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
    expect(exchange.body).toEqual({ ticket: 'test-ticket', verifier })
    expect(exchange.authorization).toBeUndefined()
    expect(hold.requested.every((request) => request.time >= exchange.time)).toBe(true)
    expect(
      requests.filter((request) => request.time <= exchange.time).map((request) => request.path),
    ).toEqual(['/ping', '/mode', '/auth/external/exchange'])
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
      'callback-access',
    )
    await expectNoCallbackSecrets(page)
    const paints = await page.evaluate(() =>
      performance
        .getEntriesByType('paint')
        .map((paint) => ({ name: paint.name, milliseconds: paint.startTime })),
    )
    await testInfo.attach('callback-timing', {
      body: JSON.stringify({
        exchangeMilliseconds: exchange.time - started,
        firstMainMilliseconds: hold.requested[0]!.time - started,
        paints,
      }),
      contentType: 'application/json',
    })
    const documentId = await page.evaluate(() => performance.timeOrigin)
    hold.release()
    await expect(page).toHaveURL(/\/settings\/profile$/)
    await expect(page.locator('#app')).not.toBeEmpty()
    await expect(page.locator('#auth-startup')).toHaveCount(0)
    expect(await page.evaluate(() => performance.timeOrigin)).toBe(documentId)
    expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
  } finally {
    hold.release()
  }
})

test('a test popup relays over a real BroadcastChannel without loading the app or probing a service', async ({
  page,
}) => {
  await page.goto('/favicon.ico')
  const received: Array<{ valid: boolean; time: number }> = []
  await page.exposeBinding(
    'recordCallbackRelay',
    (_source, value: { valid: boolean; time: number }) => received.push(value),
  )
  const popupPromise = page.waitForEvent('popup')
  await page.evaluate(() => {
    const channel = new BroadcastChannel('linguaflow-auth-test-startup')
    channel.onmessage = (event) => {
      const valid =
        event.origin === location.origin &&
        event.data?.type === 'ticket' &&
        event.data?.transactionId === 'tx-test' &&
        event.data?.ticket === 'test-ticket'
      void (
        window as Window & { recordCallbackRelay: (value: unknown) => Promise<void> }
      ).recordCallbackRelay({ valid, time: Date.now() })
      if (valid) channel.postMessage({ type: 'complete', transactionId: 'tx-test' })
    }
    const popup = window.open('about:blank', 'callback-test', 'popup,width=560,height=760')!
    popup.opener = null
  })
  const popup = await popupPromise
  await popup.evaluate(() => {
    sessionStorage.setItem(
      'linguaflow.connection-test-relay.v1',
      JSON.stringify({
        channel: 'linguaflow-auth-test-startup',
        transactionId: 'tx-test',
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
  })
  const apiRequests: string[] = []
  await popup.route('**/api/v1/**', (route) => {
    apiRequests.push(route.request().url())
    return route.abort()
  })
  const hold = await holdResources(popup)
  const closed = popup.waitForEvent('close')
  try {
    await popup.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect.poll(() => received).toEqual([expect.objectContaining({ valid: true })])
    await closed
    expect(apiRequests).toHaveLength(0)
    expect(hold.requested).toHaveLength(0)
  } finally {
    hold.release()
  }
})

test('signup confirmation resumes from private memory without repeating exchange', async ({
  page,
}) => {
  const requests = await setupCallback(page, { exchange: confirmation })
  const hold = await holdResources(page)
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect(page.getByRole('status')).toContainText('认证已验证')
    await expectNoCallbackSecrets(page)
    hold.release()
    await expect(page.getByRole('heading', { name: '创建你的账号', exact: true })).toBeVisible()
    await expectNoCallbackSecrets(page)
    expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
    await page.locator('input[autocomplete="username"]').fill('member2')
    await page.locator('input[autocomplete="new-password"]').nth(0).fill('valid-password')
    await page.locator('input[autocomplete="new-password"]').nth(1).fill('valid-password')
    await page.getByRole('button', { name: '创建账号并登录', exact: true }).click()
    await expect(page).toHaveURL(/\/settings\/profile$/)
    expect(
      requests.find((request) => request.path === '/auth/external/complete')?.body,
    ).toMatchObject({
      confirmation_token: 'private-confirmation',
      verifier,
      username: 'member2',
    })
    expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
    await expectNoCallbackSecrets(page)
  } finally {
    hold.release()
  }
})

test('protected callbacks restore a rotated session before linking, ahead of the main app', async ({
  page,
}) => {
  const requests = await setupCallback(page, {
    protected: true,
    exchange: {
      ...confirmation,
      kind: 'link_confirmation',
      signup_requirements: [],
      signup: undefined,
    },
  })
  const hold = await holdResources(page)
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect(page.getByRole('status')).toContainText('认证已验证')
    const exchange = requests.find((request) => request.path === '/auth/external/exchange')!
    expect(exchange.authorization).toBe('Bearer rotated-access')
    expect(requests.filter((request) => request.path === '/auth/refresh')).toHaveLength(1)
    expect(
      requests.filter((request) => request.time <= exchange.time).map((request) => request.path),
    ).toEqual([
      '/ping',
      '/mode',
      '/users/me',
      '/auth/refresh',
      '/users/me',
      '/auth/external/exchange',
    ])
    await expectNoCallbackSecrets(page)
    hold.release()
    await expect(page.getByRole('heading', { name: '确认绑定登录方式', exact: true })).toBeVisible()
    expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
    await expectNoCallbackSecrets(page)
  } finally {
    hold.release()
  }
})

test('an unknown exchange times out visibly and is never automatically replayed', async ({
  page,
}) => {
  await setupCallback(page)
  await page.clock.install()
  const hold = await holdResources(page)
  let calls = 0
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route('**/api/v1/auth/external/exchange', async (route) => {
    calls++
    await gate
    await json(route, { kind: 'session', session: issuedSession }).catch(() => undefined)
  })
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect.poll(() => calls).toBe(1)
    await page.clock.runFor(20_001)
    await expect(page.getByRole('status')).toContainText('连接超时')
    await expect(page.getByRole('link', { name: '返回登录', exact: true })).toBeVisible()
    release()
    await page.clock.runFor(5_000)
    expect(calls).toBe(1)
    expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBeNull()
    expect(hold.requested).toHaveLength(0)
    await expectNoCallbackSecrets(page)
  } finally {
    release()
    hold.release()
  }
})

test('a failed business resource shows recovery after a single successful exchange', async ({
  page,
}) => {
  const requests = await setupCallback(page)
  await page.route('**/src/main.ts*', (route) => route.abort())
  await page.route('**/assets/main-*.js', (route) => route.abort())
  await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
  await expect(page.getByRole('status')).toContainText('认证已验证，但后续界面加载失败')
  await expect(page.getByRole('link', { name: '返回登录', exact: true })).toBeVisible()
  expect(requests.filter((request) => request.path === '/auth/external/exchange')).toHaveLength(1)
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'callback-access',
  )
  await expectNoCallbackSecrets(page)
})

test('an account change while confirmation UI downloads clears the flow and prevents mounting stale UI', async ({
  page,
  context,
}) => {
  await setupCallback(page, { exchange: confirmation })
  const hold = await holdResources(page)
  try {
    await page.goto('/auth/complete?ticket=test-ticket', { waitUntil: 'commit' })
    await expect(page.getByRole('status')).toContainText('认证已验证')
    const other = await context.newPage()
    await other.goto('/favicon.ico')
    await other.evaluate(() => localStorage.setItem('linguaflow.auth_epoch', 'different-account'))
    await expect(page.getByRole('status')).toContainText('认证流程已失效')
    hold.release()
    await page.waitForLoadState('networkidle')
    await expect(page.locator('#app')).toBeEmpty()
    await expect(page.getByRole('status')).toContainText('认证流程已失效')
    await expectNoCallbackSecrets(page)
  } finally {
    hold.release()
  }
})
