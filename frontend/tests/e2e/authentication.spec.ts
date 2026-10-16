import { expect, test, type Page } from '@playwright/test'
import { createHash } from 'node:crypto'
import { authCapabilities, json, mockApp, testUser } from './fixtures'

async function anonymous(page: Page) {
  await mockApp(page)
  await page.addInitScript(() => {
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
  })
}
const externalConnection = {
  connection_id: 'company',
  display_name: 'Company SSO',
  description: '',
  display_order: 0,
  can_login: true,
  can_signup: true,
  signup_requirements: ['local_password'],
}
const session = {
  user: { ...testUser('user', 2), email: null },
  access_token: 'external-access',
  refresh_token: 'external-refresh',
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
}

test('password login uses identifier and keeps the password verbatim', async ({ page }) => {
  await anonymous(page)
  let payload: unknown
  await page.route('**/api/v1/auth/login', (route) => {
    payload = route.request().postDataJSON()
    return json(route, { title: 'Invalid credentials' }, 401)
  })
  await page.goto('/login')
  await page.locator('input[autocomplete="username"]').fill(' Alice ')
  await page.locator('input[autocomplete="current-password"]').fill(' old ')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect.poll(() => payload).toEqual({ identifier: ' Alice ', password: ' old ' })
  await expect(page.locator('input[autocomplete="current-password"]')).toHaveValue(' old ')
})

test('capability failure closes registration and can be retried', async ({ page }) => {
  await anonymous(page)
  let failed = true
  await page.route('**/api/v1/auth/capabilities', (route) =>
    failed
      ? json(route, { title: 'Capabilities unavailable' }, 503)
      : json(route, authCapabilities),
  )
  await page.goto('/register')
  await expect(page.getByText('Capabilities unavailable')).toBeVisible()
  await expect(page.locator('input[autocomplete="username"]')).toHaveCount(0)
  failed = false
  await page.getByRole('button', { name: '重试', exact: true }).click()
  await expect(page.locator('input[autocomplete="username"]')).toBeVisible()
})

test('registration normalizes username and allows an omitted email', async ({ page }) => {
  await anonymous(page)
  let payload: Record<string, unknown> | undefined
  await page.route('**/api/v1/auth/register', (route) => {
    payload = route.request().postDataJSON()
    return json(route, session, 201)
  })
  await page.goto('/register')
  await page.locator('input[autocomplete="username"]').fill(' Alice_Test ')
  await page.locator('input[autocomplete="new-password"]').nth(0).fill(' abcdefgh ')
  await page.locator('input[autocomplete="new-password"]').nth(1).fill(' abcdefgh ')
  await page.getByRole('button', { name: '注册并登录', exact: true }).click()
  await expect.poll(() => payload?.username).toBe('alice_test')
  expect(payload).not.toHaveProperty('email')
  expect(payload?.password).toBe(' abcdefgh ')
})

test('OAuth signup uses a browser challenge, requires explicit first password and retries a username conflict', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, { ...authCapabilities, connections: [externalConnection] }),
  )
  let challenge = ''
  let exchanges = 0
  const completed: Record<string, unknown>[] = []
  await page.route('**/api/v1/auth/external/company/start', (route) => {
    const body = route.request().postDataJSON()
    challenge = body.browser_challenge
    expect(body).not.toHaveProperty('verifier')
    return json(route, {
      transaction_id: 'tx-one',
      authorization_url: `${body.completion_url}?ticket=one-use-ticket`,
      expires_at: '2099-01-01T00:00:00Z',
    })
  })
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    exchanges++
    const body = route.request().postDataJSON()
    expect(createHash('sha256').update(body.verifier).digest('base64url')).toBe(challenge)
    expect(body.ticket).toBe('one-use-ticket')
    expect(route.request().headers().referer).toBeUndefined()
    return json(route, {
      kind: 'signup_confirmation',
      confirmation_token: 'confirmation-one',
      transaction_id: 'tx-one',
      expires_at: '2099-01-01T00:00:00Z',
      signup_requirements: ['local_password'],
      profile: {
        connection_id: 'company',
        connection_name: 'Company SSO',
        external_display: 'Example identity',
      },
    })
  })
  await page.route('**/api/v1/auth/external/complete', (route) => {
    completed.push(route.request().postDataJSON())
    return completed.length === 1
      ? json(route, { title: '用户名不可用' }, 409)
      : json(route, { kind: 'session', session })
  })
  await page.goto('/login')
  await page.getByRole('button', { name: '使用 Company SSO 继续' }).click()
  await expect(page).toHaveURL(/\/auth\/complete$/)
  await expect(page.getByText('Example identity')).toBeVisible()
  expect(completed).toHaveLength(0)
  await page.reload()
  await expect(page.getByText('Example identity')).toBeVisible()
  expect(exchanges).toBe(1)
  await page.locator('input[autocomplete="username"]').fill('new-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  expect(completed).toHaveLength(0)
  for (const username of ['taken-owner', 'new-owner']) {
    await page.locator('input[autocomplete="username"]').fill(username)
    await page.locator('input[autocomplete="new-password"]').nth(0).fill(' first-password ')
    await page.locator('input[autocomplete="new-password"]').nth(1).fill(' first-password ')
    await page.getByRole('button', { name: '创建账号并登录' }).click()
    if (username === 'taken-owner') await expect(page.getByText('用户名不可用')).toBeVisible()
  }
  await expect(page).toHaveURL(/\/$/)
  expect(exchanges).toBe(1)
  expect(completed[1]).toMatchObject({
    username: 'new-owner',
    initial_password: ' first-password ',
    confirmation_token: 'confirmation-one',
  })
  expect(
    await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1')),
  ).toBeNull()
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'external-access',
  )
})

test('an unsolicited callback removes the ticket and never exchanges it', async ({ page }) => {
  await anonymous(page)
  let exchanges = 0
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    exchanges++
    return json(route, {}, 400)
  })
  await page.goto('/auth/complete?ticket=unsolicited')
  await expect(page).toHaveURL(/\/auth\/complete$/)
  await expect(page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。')).toBeVisible()
  expect(exchanges).toBe(0)
})

test('binding requires explicit confirmation and keeps the current account session', async ({
  page,
}) => {
  await mockApp(page)
  let payload: Record<string, unknown> | undefined
  await page.route('**/api/v1/auth/external/exchange', (route) =>
    json(route, {
      kind: 'link_confirmation',
      confirmation_token: 'link-confirmation',
      transaction_id: 'link-tx',
      expires_at: '2099-01-01T00:00:00Z',
      signup_requirements: [],
      profile: {
        connection_id: 'company',
        connection_name: 'Company SSO',
        external_display: 'Linked identity',
      },
    }),
  )
  await page.route('**/api/v1/auth/external/complete', (route) => {
    payload = route.request().postDataJSON()
    expect(route.request().headers().authorization).toBe('Bearer test-access')
    return json(route, { kind: 'linked', binding_id: 7 })
  })
  await page.goto('/settings/preferences')
  await page.evaluate(() => {
    const epoch = localStorage.getItem('linguaflow.auth_epoch') ?? 'test-epoch'
    localStorage.setItem('linguaflow.auth_epoch', epoch)
    sessionStorage.setItem(
      'linguaflow.external-flow.v1',
      JSON.stringify({
        baseUrl: location.origin + '/api/v1',
        epoch,
        userId: 1,
        purpose: 'link',
        connectionId: 'company',
        verifier: 'v'.repeat(43),
        transactionId: 'link-tx',
        expiresAt: '2099-01-01T00:00:00Z',
        returnTo: '/settings/security',
        stage: 'started',
      }),
    )
  })
  await page.goto('/auth/complete?ticket=link-ticket')
  await expect(page.getByText('Linked identity')).toBeVisible()
  expect(payload).toBeUndefined()
  await expect(page.locator('input[autocomplete="username"]')).toHaveCount(0)
  await page.getByRole('button', { name: '确认绑定', exact: true }).click()
  await expect(page).toHaveURL(/\/settings\/security$/)
  expect(payload).toEqual({ confirmation_token: 'link-confirmation', verifier: 'v'.repeat(43) })
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'test-access',
  )
})
