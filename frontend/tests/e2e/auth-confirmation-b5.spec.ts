import { expect, test, type Page, type Route } from '@playwright/test'
import { authCapabilities, json, mockApp, testUser } from './fixtures'

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

async function routeSignupConfirmation(
  page: Page,
  options: {
    flowExpiresAt?: Expiry
    confirmationExpiresAt?: Expiry
    complete?: CompleteHandler
  } = {},
) {
  const requests = { starts: 0, exchanges: 0, completes: [] as Record<string, unknown>[] }
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
  await expect
    .poll(() =>
      page.evaluate(() => {
        const flow = sessionStorage.getItem('linguaflow.external-flow.v1')
        return flow ? JSON.parse(flow).stage : null
      }),
    )
    .toBe('confirming')
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
  expect(
    await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1')),
  ).toBeNull()
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
  expect(
    await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1')),
  ).toBeNull()
  expect(requests.completes).toHaveLength(1)

  await page.getByRole('button', { name: '取消并返回', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(requests.completes).toHaveLength(1)
})

test('a confirmation expires while displayed and blocks submission', async ({ page }) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    confirmationExpiresAt: () => new Date(Date.now() + 2200).toISOString(),
  })
  await openConfirmation(page)
  await page.locator('input[autocomplete="username"]').fill('expiring-owner')
  await expect(page.getByText('确认已过期，请重新发起认证。')).toBeVisible({ timeout: 6000 })
  await expect(confirmationButton(page)).toBeDisabled()
  await page.locator('input[autocomplete="username"]').press('Enter')
  expect(requests.completes).toHaveLength(0)
})

test('an exchange that returns an already expired confirmation cannot be completed', async ({
  page,
}) => {
  await anonymous(page)
  const requests = await routeSignupConfirmation(page, {
    confirmationExpiresAt: '2000-01-01T00:00:00Z',
  })
  await openConfirmation(page)
  await expect(page.getByText('确认已过期，请重新发起认证。')).toBeVisible()
  await expect(confirmationButton(page)).toBeDisabled()
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
  expect(
    await page.evaluate(() => sessionStorage.getItem('linguaflow.external-flow.v1')),
  ).toBeNull()
  expect(requests.completes).toHaveLength(1)

  await page.getByRole('button', { name: '取消并返回', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  await openConfirmation(page)
  expect(requests.starts).toBe(2)
  expect(requests.exchanges).toBe(2)
  expect(requests.completes).toHaveLength(1)
  expect(
    await page.evaluate(
      () => JSON.parse(sessionStorage.getItem('linguaflow.external-flow.v1')!).transactionId,
    ),
  ).toBe('tx-2')
})
