import { expect, test, type Page, type Route } from '@playwright/test'
import { authCapabilities, json, loginMethods, mockApp, testUser } from './fixtures'

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

type CompleteHandler = (route: Route, call: number) => Promise<void>
type Expiry = string | ((call: number) => string)

const later = () => '2099-01-01T00:00:00Z'
const at = (expiry: Expiry | undefined, call: number) =>
  typeof expiry === 'function' ? expiry(call) : (expiry ?? later())

async function anonymous(page: Page) {
  await mockApp(page)
  await page.addInitScript(() => {
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
  })
}

async function expectExternalSecretsNotStored(page: Page, secrets: string[]) {
  const state = await page.evaluate(() => ({
    href: location.href,
    v2: sessionStorage.getItem('linguaflow.external-flow.v2'),
    v1: sessionStorage.getItem('linguaflow.external-flow.v1'),
    result: sessionStorage.getItem('linguaflow.external-result.v1'),
    storage: [localStorage, sessionStorage]
      .flatMap((store) => Object.keys(store).map((key) => `${key}=${store.getItem(key)}`))
      .join('\n'),
  }))
  expect(new URL(state.href).searchParams.has('ticket')).toBe(false)
  expect(new URL(state.href).search).not.toMatch(
    /[?&](?:verifier|confirmation(?:_token)?|reauthentication_proof|proof)=/i,
  )
  expect(state.v2).toBeNull()
  expect(state.v1).toBeNull()
  expect(state.result).toBeNull()
  expect(state.storage).not.toMatch(
    /"(?:verifier|confirmation_token|reauthentication_proof|proof)"\s*:/,
  )
  for (const secret of secrets) {
    expect(state.href).not.toContain(secret)
    expect(state.storage).not.toContain(secret)
  }
}

async function routeSignupConfirmation(
  page: Page,
  options: {
    flowExpiresAt?: Expiry
    confirmationExpiresAt?: Expiry
    complete?: CompleteHandler
  } = {},
) {
  const requests = {
    starts: 0,
    exchanges: 0,
    exchangeBodies: [] as Record<string, unknown>[],
    completes: [] as Record<string, unknown>[],
  }
  await page.route('**/api/v1/auth/capabilities', (route) =>
    json(route, { ...authCapabilities, connections: [externalConnection] }),
  )
  await page.route('**/api/v1/auth/external/company/start', (route) => {
    requests.starts++
    const body = route.request().postDataJSON()
    return json(route, {
      transaction_id: `tx-${requests.starts}`,
      authorization_url: `${body.completion_url}?ticket=ticket-${requests.starts}`,
      expires_at: at(options.flowExpiresAt, requests.starts),
    })
  })
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    requests.exchanges++
    requests.exchangeBodies.push(route.request().postDataJSON())
    return json(route, {
      kind: 'signup_confirmation',
      confirmation_token: `confirmation-${requests.exchanges}`,
      transaction_id: `tx-${requests.starts}`,
      expires_at: at(options.confirmationExpiresAt, requests.exchanges),
      signup: { email_requirement: 'optional', password_requirement: 'required' },
      signup_requirements: ['local_password'],
      email: 'suggested@example.com',
      profile: {
        connection_id: 'company',
        connection_name: 'Company SSO',
        external_display: 'B5 identity',
      },
    })
  })
  await page.route('**/api/v1/auth/external/complete', (route) => {
    requests.completes.push(route.request().postDataJSON() as Record<string, unknown>)
    return (
      options.complete?.(route, requests.completes.length) ??
      json(route, { kind: 'session', session })
    )
  })
  return requests
}

async function openConfirmation(page: Page) {
  await page.goto('/login')
  await page.getByRole('button', { name: '使用 Company SSO 继续' }).click()
  await expect(page.getByText('B5 identity')).toBeVisible()
}

async function fillPasswords(page: Page, value: string) {
  const passwords = page.locator('input[autocomplete="new-password"]')
  await passwords.nth(0).fill(value)
  await passwords.nth(1).fill(value)
}

const confirmationButton = (page: Page) =>
  page.getByRole('button', { name: '创建账号并登录', exact: true })

async function openBindingConfirmation(page: Page) {
  const requests = { exchanges: 0, completes: 0, verifier: '', linkProof: 'link-start-proof' }
  await mockApp(page)
  await page.route('**/api/v1/users/me/login-methods', (route) =>
    json(route, {
      ...loginMethods,
      available_connections: [
        { connection_id: 'company', display_name: 'Company SSO', can_link: true },
      ],
    }),
  )
  await page.route('**/api/v1/auth/reauthenticate', (route) =>
    json(route, {
      reauthentication_proof: requests.linkProof,
      expires_at: '2099-01-01T00:00:00Z',
    }),
  )
  await page.route('**/api/v1/users/me/login-bindings/company/start', (route) => {
    const body = route.request().postDataJSON()
    expect(body.reauthentication_proof).toBe(requests.linkProof)
    return json(route, {
      transaction_id: 'binding-tx',
      authorization_url: `${body.completion_url}?ticket=binding-ticket`,
      expires_at: '2099-01-01T00:00:00Z',
    })
  })
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    requests.exchanges++
    requests.verifier = String(route.request().postDataJSON().verifier)
    return json(route, {
      kind: 'link_confirmation',
      confirmation_token: 'binding-confirmation',
      transaction_id: 'binding-tx',
      expires_at: '2099-01-01T00:00:00Z',
      signup_requirements: [],
      profile: {
        connection_id: 'company',
        connection_name: 'Company SSO',
        external_display: 'Binding identity',
      },
    })
  })
  await page.route('**/api/v1/auth/external/complete', (route) => {
    requests.completes++
    return json(route, { kind: 'linked', binding_id: 7 })
  })
  await page.goto('/settings/security')
  await page.getByRole('button', { name: '绑定', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('使用密码验证').fill('current password')
  await dialog.getByRole('button', { name: '验证', exact: true }).click()
  await expect(dialog.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible()
  await dialog.getByRole('button', { name: '继续', exact: true }).click()
  await expect(page.getByText('Binding identity')).toBeVisible()
  return requests
}

test('explicit invalid input and username conflict remain retryable and require fresh passwords', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    complete: (route, call) => {
      if (call === 1)
        return json(route, { type: 'urn:linguaflow:invalid-input', title: 'invalid-input' }, 400)
      if (call === 2)
        return json(route, { type: 'urn:linguaflow:conflict', title: 'conflict' }, 409)
      return json(route, { kind: 'session', session })
    },
  })
  await openConfirmation(page)
  await page.locator('input[autocomplete="username"]').fill('retry-owner')
  await page.locator('input[autocomplete="email"]').fill('retry@example.com')
  await fillPasswords(page, 'first-password')
  await confirmationButton(page).click()

  await expect(page.getByRole('alert').filter({ hasText: 'invalid-input' })).toBeVisible()
  await expect(confirmationButton(page)).toBeEnabled()
  await expect(page.locator('input[autocomplete="new-password"]').nth(0)).toHaveValue('')
  await expect(page.locator('input[autocomplete="new-password"]').nth(1)).toHaveValue('')
  expect(requests.exchanges).toBe(1)
  expect(requests.completes[0]).toMatchObject({
    username: 'retry-owner',
    email: 'retry@example.com',
    initial_password: 'first-password',
  })

  await fillPasswords(page, 'second-password')
  await confirmationButton(page).click()
  await expect(page.getByText('此用户名已被使用，请更换用户名。')).toBeVisible()
  await expect(confirmationButton(page)).toBeEnabled()
  await expect(page.locator('input[autocomplete="new-password"]').nth(0)).toHaveValue('')
  await expect(page.locator('input[autocomplete="new-password"]').nth(1)).toHaveValue('')

  await fillPasswords(page, 'third-password')
  await confirmationButton(page).click()
  await expect(page).toHaveURL(/\/$/)
  expect(requests.completes).toHaveLength(3)
  expect(requests.completes[1].initial_password).toBe('second-password')
  expect(requests.completes[2].initial_password).toBe('third-password')
  await expectExternalSecretsNotStored(page, [
    String(requests.exchangeBodies[0]?.verifier),
    'confirmation-1',
  ])
})

test('an unknown policy or revision conflict clears the flow and cannot be posted again', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    complete: (route) =>
      json(
        route,
        { type: 'urn:linguaflow:auth-policy-revision-conflict', title: 'revision conflict' },
        409,
      ),
  })
  await openConfirmation(page)
  await page.locator('input[autocomplete="username"]').fill('one-shot-owner')
  await fillPasswords(page, 'one-shot-password')
  await confirmationButton(page).click()

  await expect(
    page.getByRole('alert').filter({ hasText: '认证未完成，请重新发起。' }),
  ).toBeVisible()
  await expect(confirmationButton(page)).toBeDisabled()
  await expectExternalSecretsNotStored(page, [
    String(requests.exchangeBodies[0]?.verifier),
    'confirmation-1',
  ])
  expect(requests.completes).toHaveLength(1)

  await page.getByRole('button', { name: '取消并返回', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(requests.completes).toHaveLength(1)
})

test('a confirmation expires while displayed and blocks submission', async ({ page }) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    confirmationExpiresAt: () => new Date(Date.now() + 4000).toISOString(),
  })
  await openConfirmation(page)
  await page.locator('input[autocomplete="username"]').fill('expiring-owner')
  await expect(page.getByText('确认已过期，请重新发起认证。')).toBeVisible({ timeout: 8000 })
  await expect(page.locator('input[autocomplete="username"]')).toHaveCount(0)
  await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
  await expect(confirmationButton(page)).toHaveCount(0)
  expect(requests.completes).toHaveLength(0)
})

test('an exchange that returns an already expired confirmation cannot be completed', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    confirmationExpiresAt: '2000-01-01T00:00:00Z',
  })
  await page.goto('/login')
  await page.getByRole('button', { name: '使用 Company SSO 继续' }).click()
  await expect(page.getByText('确认已过期，请重新发起认证。')).toBeVisible()
  await expect(page.getByText('B5 identity')).toBeHidden()
  await expect(confirmationButton(page)).toHaveCount(0)
  expect(requests.exchanges).toBe(1)
  expect(requests.completes).toHaveLength(0)
})

test('a lost completion response clears the flow and requires a new external start', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    complete: (route, call) =>
      call === 1 ? route.abort('failed') : json(route, { kind: 'session', session }),
  })
  await openConfirmation(page)
  await page.locator('input[autocomplete="username"]').fill('uncertain-owner')
  await fillPasswords(page, 'uncertain-password')
  await confirmationButton(page).click()

  await expect(
    page.getByText('结果未获确认，请重新登录；若在绑定账号，可返回安全设置检查登录方式。'),
  ).toBeVisible()
  await expect(confirmationButton(page)).toBeDisabled()
  await expectExternalSecretsNotStored(page, [
    String(requests.exchangeBodies[0]?.verifier),
    'confirmation-1',
  ])
  expect(requests.completes).toHaveLength(1)

  await page.getByRole('button', { name: '取消并返回', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  await openConfirmation(page)
  expect(requests.starts).toBe(2)
  expect(requests.exchanges).toBe(2)
  expect(requests.completes).toHaveLength(1)
  await expect(page.getByText('B5 identity')).toBeVisible()
  await expectExternalSecretsNotStored(page, [
    String(requests.exchangeBodies[1]?.verifier),
    'confirmation-2',
  ])
})

test('anonymous confirmation is invalidated by refresh without another exchange or completion', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page)
  await openConfirmation(page)
  const verifier = String(requests.exchangeBodies[0]?.verifier)
  await expectExternalSecretsNotStored(page, [verifier, 'confirmation-1'])

  await page.reload()
  await expect(page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。')).toBeVisible()
  expect(requests.exchanges).toBe(1)
  expect(requests.completes).toHaveLength(0)
  await expectExternalSecretsNotStored(page, [verifier, 'confirmation-1'])
})

test('history navigation does not restore an exchanged confirmation', async ({ page }) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page)
  await openConfirmation(page)
  expect(requests.exchanges).toBe(1)

  await page.goBack()
  await expect(page).toHaveURL(/\/login$/)
  await page.goForward()
  await expect(page).toHaveURL(/\/auth\/complete$/)
  await expect(page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。')).toBeVisible()
  expect(requests.exchanges).toBe(1)
  expect(requests.completes).toHaveLength(0)
})

test('binding confirmation is invalidated by refresh without exchange replay or completion', async ({
  page,
}) => {
  const requests = await openBindingConfirmation(page)
  await expectExternalSecretsNotStored(page, [
    requests.verifier,
    'binding-confirmation',
    requests.linkProof,
  ])

  await page.reload()
  await expect(page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。')).toBeVisible()
  expect(requests.exchanges).toBe(1)
  expect(requests.completes).toBe(0)
  await expectExternalSecretsNotStored(page, [
    requests.verifier,
    'binding-confirmation',
    requests.linkProof,
  ])
})

test('callback startup deletes legacy flow and result records without exchanging them', async ({
  page,
}) => {
  await anonymous(page)
  await page.addInitScript(() => {
    sessionStorage.setItem(
      'linguaflow.external-flow.v1',
      JSON.stringify({ stage: 'confirming', verifier: 'legacy-verifier' }),
    )
    sessionStorage.setItem(
      'linguaflow.external-result.v1',
      JSON.stringify({ proof: { proof: 'legacy-proof' } }),
    )
  })
  let exchanges = 0
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    exchanges++
    return json(route, {}, 400)
  })
  await page.goto('/auth/complete?ticket=legacy-ticket')
  await expect(page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。')).toBeVisible()
  expect(exchanges).toBe(0)
  expect(
    await page.evaluate(() => [
      sessionStorage.getItem('linguaflow.external-flow.v1'),
      sessionStorage.getItem('linguaflow.external-result.v1'),
    ]),
  ).toEqual([null, null])
})
