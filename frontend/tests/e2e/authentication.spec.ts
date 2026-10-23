import { expect, test, type Page, type Route } from '@playwright/test'
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
  signup: { email_requirement: 'optional', password_requirement: 'required' },
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
      signup: { email_requirement: 'optional', password_requirement: 'required' },
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
      ? json(route, { type: 'urn:linguaflow:conflict', title: 'conflict' }, 409)
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
    if (username === 'taken-owner')
      await expect(page.getByText('此用户名已被使用，请更换用户名。')).toBeVisible()
  }
  await expect(page).toHaveURL(/\/$/)
  expect(exchanges).toBe(1)
  expect(completed[1]).toMatchObject({
    username: 'new-owner',
    initial_password: ' first-password ',
    confirmation_token: 'confirmation-one',
  })
  expect(completed[1]).not.toHaveProperty('email')
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

const b5SignupRoute = (
  page: Page,
  emailRequirement: 'optional' | 'required',
  passwordRequirement: 'none' | 'optional' | 'required',
  complete: (route: Route, call: number) => ReturnType<typeof json>,
) => {
  const signupRequirements = [
    ...(emailRequirement === 'required' ? (['email'] as const) : []),
    ...(passwordRequirement === 'required' ? (['local_password'] as const) : []),
  ]
  const completed: Record<string, unknown>[] = []
  void page.route('**/api/v1/auth/external/company/start', (route) => {
    const body = route.request().postDataJSON()
    return json(route, {
      transaction_id: 'tx-b5',
      authorization_url: `${body.completion_url}?ticket=b5-ticket`,
      expires_at: '2099-01-01T00:00:00Z',
    })
  })
  void page.route('**/api/v1/auth/external/exchange', (route) =>
    json(route, {
      kind: 'signup_confirmation',
      confirmation_token: 'confirmation-b5',
      transaction_id: 'tx-b5',
      expires_at: '2099-01-01T00:00:00Z',
      signup: { email_requirement: emailRequirement, password_requirement: passwordRequirement },
      signup_requirements: signupRequirements,
      email: emailRequirement === 'required' ? 'b5@example.com' : 'suggested@example.com',
      profile: {
        connection_id: 'company',
        connection_name: 'Company SSO',
        external_display: 'B5 identity',
      },
    }),
  )
  void page.route('**/api/v1/auth/external/complete', (route) => {
    completed.push(route.request().postDataJSON())
    return complete(route, completed.length)
  })
  return completed
}

const openB5Signup = async (page: Page) => {
  await page.goto('/login')
  await page.getByRole('button', { name: '使用 Company SSO 继续' }).click()
  await expect(page.getByText('B5 identity')).toBeVisible()
}

test('external signup omits an empty optional email and never sends a password when none is allowed', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, {
      ...authCapabilities,
      connections: [
        {
          connection_id: 'company',
          display_name: 'Company SSO',
          description: '',
          display_order: 0,
          can_login: true,
          can_signup: true,
          signup: { email_requirement: 'optional', password_requirement: 'none' },
          signup_requirements: [],
        },
      ],
    }),
  )
  const completed = b5SignupRoute(page, 'optional', 'none', (route) =>
    json(route, { kind: 'session', session }),
  )
  await openB5Signup(page)
  await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
  await expect(page.getByRole('switch')).toHaveCount(0)
  await page.locator('input[autocomplete="email"]').fill('')
  await page.locator('input[autocomplete="username"]').fill('b5-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(completed).toEqual([
    {
      username: 'b5-owner',
      confirmation_token: 'confirmation-b5',
      verifier: expect.any(String),
    },
  ])
})

test('external signup with a required email blocks an empty submit and sends the normalized address', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, {
      ...authCapabilities,
      connections: [
        {
          connection_id: 'company',
          display_name: 'Company SSO',
          description: '',
          display_order: 0,
          can_login: true,
          can_signup: true,
          signup: { email_requirement: 'required', password_requirement: 'none' },
          signup_requirements: ['email'],
        },
      ],
    }),
  )
  const completed = b5SignupRoute(page, 'required', 'none', (route) =>
    json(route, { kind: 'session', session }),
  )
  await openB5Signup(page)
  await expect(page.locator('input[autocomplete="email"]')).toHaveValue('b5@example.com')
  await page.locator('input[autocomplete="email"]').fill('')
  await page.locator('input[autocomplete="username"]').fill('b5-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page.getByText('请输入注册邮箱。')).toBeVisible()
  expect(completed).toHaveLength(0)
  await page.locator('input[autocomplete="email"]').fill('  B5@Example.COM  ')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(completed[0]).toMatchObject({ email: 'b5@example.com', username: 'b5-owner' })
  expect(completed[0]).not.toHaveProperty('initial_password')
})

test('optional external password defaults to skipped and omits initial_password', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, {
      ...authCapabilities,
      connections: [
        {
          connection_id: 'company',
          display_name: 'Company SSO',
          description: '',
          display_order: 0,
          can_login: true,
          can_signup: true,
          signup: { email_requirement: 'optional', password_requirement: 'optional' },
          signup_requirements: [],
        },
      ],
    }),
  )
  const completed = b5SignupRoute(page, 'optional', 'optional', (route) =>
    json(route, { kind: 'session', session }),
  )
  await openB5Signup(page)
  await expect(page.getByRole('switch')).toBeVisible()
  await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
  await page.locator('input[autocomplete="username"]').fill('b5-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(completed[0]).toMatchObject({ email: 'suggested@example.com' })
  expect(completed[0]).not.toHaveProperty('initial_password')
})

test('optional external password submits raw input only after the switch is enabled', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, {
      ...authCapabilities,
      connections: [
        {
          connection_id: 'company',
          display_name: 'Company SSO',
          description: '',
          display_order: 0,
          can_login: true,
          can_signup: true,
          signup: { email_requirement: 'optional', password_requirement: 'optional' },
          signup_requirements: [],
        },
      ],
    }),
  )
  const completed = b5SignupRoute(page, 'optional', 'optional', (route) =>
    json(route, { kind: 'session', session }),
  )
  await openB5Signup(page)
  await page.getByRole('switch').click()
  await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(2)
  await page.locator('input[autocomplete="username"]').fill('b5-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page.getByText('新密码至少需要 8 个 Unicode 字符')).toBeVisible()
  expect(completed).toHaveLength(0)
  await page.locator('input[autocomplete="new-password"]').nth(0).fill(' first-password ')
  await page.locator('input[autocomplete="new-password"]').nth(1).fill(' first-password ')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(completed[0]).toMatchObject({ initial_password: ' first-password ' })
  expect(
    await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1')),
  ).toBeNull()
})

test('a user-modified signup email is sent but never written into the stored confirmation', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, {
      ...authCapabilities,
      connections: [
        {
          connection_id: 'company',
          display_name: 'Company SSO',
          description: '',
          display_order: 0,
          can_login: true,
          can_signup: true,
          signup: { email_requirement: 'optional', password_requirement: 'none' },
          signup_requirements: [],
        },
      ],
    }),
  )
  const completed = b5SignupRoute(page, 'optional', 'none', (route, call) =>
    call === 1
      ? json(route, { type: 'urn:linguaflow:conflict', title: 'conflict' }, 409)
      : json(route, { kind: 'session', session }),
  )
  await openB5Signup(page)
  await expect(page.locator('input[autocomplete="email"]')).toHaveValue('suggested@example.com')
  await page.locator('input[autocomplete="email"]').fill('mine@example.com')
  await page.locator('input[autocomplete="username"]').fill('taken-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page.getByText('此用户名已被使用，请更换用户名。')).toBeVisible()
  expect(completed[0]).toMatchObject({ email: 'mine@example.com', username: 'taken-owner' })
  const stored = await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1'))
  expect(stored).toContain('suggested@example.com')
  expect(stored).not.toContain('mine@example.com')
  await page.locator('input[autocomplete="username"]').fill('b5-owner')
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(/\/$/)
  expect(completed[1]).toMatchObject({ email: 'mine@example.com', username: 'b5-owner' })
})

test('security hides password maintenance and rejects stale inputs when password login is disabled', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/users/me/login-methods', (route) =>
    json(route, {
      password: {
        is_set: true,
        can_login: false,
        can_set: false,
        can_delete: true,
        verified: true,
      },
      email: { address: 'tester-1@example.com', verified: false, can_recover: false },
      bindings: [
        {
          binding_id: 7,
          connection_id: 'oidc',
          display_name: 'OIDC',
          can_login: true,
          can_reauthenticate: true,
          can_unlink: true,
          verified: true,
          archived: false,
        },
      ],
      available_connections: [],
    }),
  )
  await page.goto('/settings/security')
  await expect(page.locator('input[autocomplete="current-password"]')).toHaveCount(0)
  await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
  await expect(page.getByRole('button', { name: '删除密码' })).toBeVisible()
  await expect(page.getByText('当前不可用于登录')).toBeVisible()
})
