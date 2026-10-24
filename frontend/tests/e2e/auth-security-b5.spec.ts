import { expect, test, type Page } from '@playwright/test'
import { json, loginMethods, mockApp } from './fixtures'

const proof = 'external-unlink-proof'
const verifiedText = '身份已验证，请确认本次操作。'

function securityMethods() {
  return {
    ...loginMethods,
    bindings: [
      {
        binding_id: 8,
        connection_id: 'target',
        display_name: 'Target login',
        can_login: true,
        can_reauthenticate: false,
        can_unlink: true,
        verified: true,
        archived: false,
      },
      {
        binding_id: 7,
        connection_id: 'company',
        display_name: 'Company SSO',
        can_login: true,
        can_reauthenticate: true,
        can_unlink: true,
        verified: true,
        archived: false,
      },
    ],
  }
}

async function beginExternalUnlink(
  page: Page,
  contextChange?: 'account' | 'service',
  options: { proofLifetimeMs?: number; methodsGate?: Promise<void> } = {},
) {
  const requests = { starts: [] as Record<string, unknown>[], exchanges: 0, expiresAt: 0 }
  await mockApp(page, { initializeAuthOnce: true })
  await page.route('**/api/v1/users/me/login-methods', async (route) => {
    if (requests.exchanges > 0) await options.methodsGate
    return json(route, securityMethods())
  })
  await page.route('**/api/v1/auth/reauthenticate', (route) => {
    const body = route.request().postDataJSON()
    requests.starts.push(body)
    expect(body.method).toBe('oidc')
    expect(body.binding_id).toBe(7)
    expect(body.action).toBe('unlink')
    expect(body.target_id).toBe('8')
    expect(body).not.toHaveProperty('reauthentication_proof')
    return json(route, {
      transaction_id: 'reauth-tx',
      authorization_url: `${body.completion_url}?ticket=reauth-ticket`,
      expires_at: '2099-01-01T00:00:00Z',
    })
  })
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    requests.exchanges++
    requests.expiresAt = options.proofLifetimeMs
      ? Date.now() + options.proofLifetimeMs
      : Date.parse('2099-01-01T00:00:00Z')
    return json(route, {
      kind: 'reauthentication_proof',
      reauthentication_proof: proof,
      expires_at: new Date(requests.expiresAt).toISOString(),
    })
  })
  if (contextChange)
    await page.addInitScript((change) => {
      if (location.pathname !== '/auth/complete') return
      if (change === 'account') {
        localStorage.setItem('linguaflow.auth_epoch', 'changed-account-epoch')
        localStorage.setItem('linguaflow.access_token', 'changed-account-token')
      } else {
        localStorage.setItem('linguaflow.api_base_url', `${location.origin}/alternate/api/v1`)
      }
    }, contextChange)

  await page.goto('/settings/security')
  await page.getByRole('button', { name: '解绑', exact: true }).nth(0).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: '使用 Company SSO 验证', exact: true }).click()
  return requests
}

async function expectProofNotStored(page: Page) {
  const state = await page.evaluate(() => ({
    href: location.href,
    v2: sessionStorage.getItem('linguaflow.external-flow.v2'),
    result: sessionStorage.getItem('linguaflow.external-result.v1'),
    values: [localStorage, sessionStorage]
      .flatMap((store) => Object.keys(store).map((key) => store.getItem(key) ?? ''))
      .join('\n'),
  }))
  expect(new URL(state.href).searchParams.has('ticket')).toBe(false)
  expect(new URL(state.href).search).not.toMatch(
    /[?&](?:verifier|confirmation(?:_token)?|reauthentication_proof|proof)=/i,
  )
  expect(state.v2).toBeNull()
  expect(state.result).toBeNull()
  expect(state.values).not.toContain(proof)
}

test('password proof verifies the chosen link action before beginning the external flow', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/users/me/login-methods', (route) =>
    json(route, {
      ...loginMethods,
      available_connections: [
        { connection_id: 'company', display_name: 'Company', can_link: true },
      ],
    }),
  )
  const requests: unknown[] = []
  await page.route('**/api/v1/auth/reauthenticate', (route) => {
    requests.push(route.request().postDataJSON())
    return json(route, {
      reauthentication_proof: 'scoped-link-proof',
      expires_at: new Date(Date.now() + 120_000).toISOString(),
    })
  })
  await page.goto('/settings/security')
  await page.getByRole('button', { name: '绑定', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('使用密码验证').fill('untrimmed password ')
  await expect(dialog.getByLabel('使用密码验证')).toHaveValue('untrimmed password ')
  await expect(dialog.getByLabel('使用密码验证')).toBeFocused()
  await dialog.getByRole('button', { name: '验证', exact: true }).click()
  await expect
    .poll(() => requests)
    .toEqual([
      {
        method: 'password',
        password: 'untrimmed password ',
        action: 'link',
        target_id: 'company',
      },
    ])
  await expect(dialog.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible()
  await expect(dialog.locator('input[autocomplete="current-password"]')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '继续', exact: true })).toBeEnabled()
})

test('external reauthentication proof returns to its action dialog and can be claimed only once', async ({
  page,
}) => {
  const requests = await beginExternalUnlink(page)
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText(verifiedText, { exact: true })).toBeVisible()
  await expect(page).toHaveURL(/\/settings\/security$/)
  expect(requests.starts).toHaveLength(1)
  expect(requests.exchanges).toBe(1)
  await expectProofNotStored(page)

  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '解绑', exact: true }).nth(0).click()
  const reopened = page.getByRole('dialog')
  await expect(reopened.getByText(verifiedText, { exact: true })).toHaveCount(0)
  await expect(
    reopened.getByRole('button', { name: '使用 Company SSO 验证', exact: true }),
  ).toBeVisible()
  expect(requests.exchanges).toBe(1)
})

test('refreshing security settings does not restore a handed-off external proof', async ({
  page,
}) => {
  const requests = await beginExternalUnlink(page)
  await expect(page.getByRole('dialog').getByText(verifiedText, { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(requests.exchanges).toBe(1)
  await expectProofNotStored(page)

  await page.getByRole('button', { name: '解绑', exact: true }).nth(0).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText(verifiedText, { exact: true })).toHaveCount(0)
  expect(requests.exchanges).toBe(1)
})

test('a proof that expires while login methods load cannot reopen its verified action', async ({
  page,
}) => {
  let releaseMethods!: () => void
  const methodsGate = new Promise<void>((resolve) => {
    releaseMethods = resolve
  })
  const requests = await beginExternalUnlink(page, undefined, {
    proofLifetimeMs: 1200,
    methodsGate,
  })
  await expect.poll(() => requests.exchanges).toBe(1)
  await expect(page).toHaveURL(/\/settings\/security$/)
  await expect.poll(() => Date.now() > requests.expiresAt).toBe(true)
  releaseMethods()
  await page.getByRole('button', { name: '解绑', exact: true }).nth(0).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText(verifiedText, { exact: true })).toHaveCount(0)
  await expect(
    dialog.getByRole('button', { name: '使用 Company SSO 验证', exact: true }),
  ).toBeVisible()
  await expectProofNotStored(page)
  expect(requests.exchanges).toBe(1)
})

for (const contextChange of ['account', 'service'] as const) {
  test(`external reauthentication is discarded when the ${contextChange} changes during redirect`, async ({
    page,
  }) => {
    const requests = await beginExternalUnlink(page, contextChange)
    await expect(
      page.getByText('认证流程已失效或与当前服务、账号不匹配，请重新开始。'),
    ).toBeVisible()
    expect(requests.starts).toHaveLength(1)
    expect(requests.exchanges).toBe(0)
    await expectProofNotStored(page)
  })
}
