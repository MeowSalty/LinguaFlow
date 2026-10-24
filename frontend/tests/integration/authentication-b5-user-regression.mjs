import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { expect } from '@playwright/test'
import { assertExternalAuthMemory } from './external-auth-memory.mjs'

/** Auth/session keys are disposable; API origin config must survive a reset. */
const PRESERVED_AUTH_KEYS = [
  'linguaflow.api_base_url',
  'linguaflow.api_mode',
  'linguaflow.service.mode',
]

async function clearAuthState(page) {
  await page.evaluate((preserved) => {
    for (const storage of [localStorage, sessionStorage]) {
      for (let index = storage.length - 1; index >= 0; index -= 1) {
        const key = storage.key(index)
        if (!key?.startsWith('linguaflow.')) continue
        if (preserved.includes(key)) continue
        storage.removeItem(key)
      }
    }
  }, PRESERVED_AUTH_KEYS)
}

async function token(page) {
  return page.evaluate(() => localStorage.getItem('linguaflow.access_token'))
}

async function read(page, app, pathname) {
  const accessToken = await token(page)
  assert.ok(accessToken, 'B5 maintenance flow requires a real product session')
  const response = await page.request.get(`${app.apiBase}${pathname}`, {
    headers: { Authorization: `Bearer ${accessToken}` },
  })
  assert.equal(response.status(), 200, `${pathname} returned ${response.status()}`)
  return response.json()
}

async function providerLogin(page, app, provider, username, { requirePrompt = false } = {}) {
  const origin = new URL(provider.issuer ?? provider.base).origin
  await page.waitForURL((url) => url.origin === origin, { timeout: 30_000 })
  const usernameInput = page.locator('#username')
  const passwordInput = page.locator('#password')
  await expect(passwordInput).toBeVisible({ timeout: 45_000 })
  const prompted = await usernameInput.isVisible().catch(() => false)
  if (requirePrompt)
    assert.ok(prompted, 'Fresh OIDC proof did not present a real identity-service login')
  if (prompted) await usernameInput.fill(username)
  await passwordInput.fill(provider.password)
  if (provider.issuer) await page.locator('button[type="submit"]').first().click()
  else await page.locator('#oauth-approve').click()
  return { prompted }
}

async function appLogin(
  page,
  app,
  displayName,
  provider,
  username,
  { requirePrompt = false } = {},
) {
  await page.goto(`${app.origin}/login`)
  // Stored tokens from a previous flow make the provider return to /login
  // instead of the product callback; start every external login unauthenticated.
  await clearAuthState(page)
  await page.goto(`${app.origin}/login`)
  await page.getByRole('button', { name: `使用 ${displayName} 继续`, exact: true }).click()
  const authentication = await providerLogin(page, app, provider, username, { requirePrompt })
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 45_000 })
  return authentication
}

export async function loginB5ExternalAccount({ page, app, connection, provider, identity }) {
  await page.context().clearCookies()
  return appLogin(page, app, connection.displayName, provider, identity, { requirePrompt: true })
}

async function localPasswordLogin(page, app, username, password) {
  await page.goto(`${app.origin}/login`)
  await clearAuthState(page)
  await page.goto(`${app.origin}/login`)
  await page.locator('input[autocomplete="username"]').fill(username)
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page
    .getByRole('button', { name: /登录/u })
    .first()
    .click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 45_000 })
}

async function verifyPasswordProof(page, password) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible({ timeout: 20_000 })
  await expect(dialog.getByLabel('使用密码验证')).toBeVisible({ timeout: 20_000 })
  await dialog.getByLabel('使用密码验证').fill(password)
  await expect(dialog.getByLabel('使用密码验证')).toHaveValue(password)
  const responsePromise = page.waitForResponse(
    (response) => new URL(response.url()).pathname.endsWith('/auth/reauthenticate'),
    { timeout: 20_000 },
  )
  await dialog.getByRole('button', { name: '验证', exact: true }).click()
  const response = await responsePromise
  assert.equal(response.status(), 200, 'Password proof was rejected by the real product API')
  await expect(dialog.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible({
    timeout: 20_000,
  })
}

async function bindAlternativeWithCurrentSession({
  page,
  app,
  accountPassword,
  primary,
  secondary,
  primaryProvider,
  secondaryProvider,
  primaryIdentity,
  secondaryIdentity,
}) {
  await page.goto(`${app.origin}/settings/security`)
  await expect(page.getByText('已绑定的登录方式', { exact: true })).toBeVisible({ timeout: 30_000 })
  const available = await read(page, app, '/users/me/login-methods')
  if (available.bindings.some((binding) => binding.display_name === secondary.displayName))
    return { binding_already_present: true }

  const completeBodies = []
  const completionRoute = '**/api/v1/auth/external/complete'
  const observeCompletion = async (route) => {
    const propertyNames = Object.keys(route.request().postDataJSON()).sort()
    assert.deepEqual(propertyNames, ['confirmation_token', 'verifier'])
    completeBodies.push(propertyNames)
    await route.fallback()
  }
  await page.route(completionRoute, observeCompletion)

  const row = page
    .locator('div.rounded-lg.border')
    .filter({ has: page.getByText(secondary.displayName, { exact: true }) })
    .filter({ has: page.getByRole('button', { name: '绑定', exact: true }) })
    .first()
  await expect(row).toBeVisible({ timeout: 30_000 })
  await row.getByRole('button', { name: '绑定', exact: true }).click()
  if (accountPassword) {
    await verifyPasswordProof(page, accountPassword)
  } else {
    const externalProof = page.getByRole('button', {
      name: `使用 ${primary.displayName} 验证`,
      exact: true,
    })
    await expect(externalProof).toBeVisible({ timeout: 30_000 })
    await page.context().clearCookies()
    await externalProof.click()
    await providerLogin(page, app, primaryProvider, primaryIdentity, { requirePrompt: true })
    await expect(page.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible({
      timeout: 30_000,
    })
  }
  // Provider cookies could silently authenticate the primary subject instead
  // of the dedicated secondary identity. Clearing cookies preserves the app's
  // in-memory proof while the current document remains open.
  await page.context().clearCookies()
  await page.getByRole('button', { name: '继续', exact: true }).click()
  await providerLogin(page, app, secondaryProvider, secondaryIdentity, { requirePrompt: true })
  const confirm = page.getByRole('button', { name: '确认绑定', exact: true })
  await expect(confirm).toBeVisible({ timeout: 30_000 })
  await assertExternalAuthMemory(page)
  await confirm.click()
  await expect(page).toHaveURL(/\/settings\/security(?:$|\?)/u, { timeout: 30_000 })
  await page.unroute(completionRoute, observeCompletion)
  assert.equal(completeBodies.length, 1, 'The explicit binding confirmation was not submitted once')
  const after = await read(page, app, '/users/me/login-methods')
  const binding = after.bindings.find((item) => item.display_name === secondary.displayName)
  assert.ok(binding, 'The alternative provider binding was not persisted')
  assert.equal(
    binding.can_reauthenticate,
    true,
    'A linked OIDC binding must support fresh reauthentication',
  )
  return {
    binding_id: binding.binding_id,
    linked_by_product_ui: true,
    proof_kind: accountPassword ? 'verified-local-password' : 'fresh-primary-provider',
    binding_count: after.bindings.length,
    binding_verified_before_separate_login: binding.verified,
    complete_property_names: completeBodies[0],
  }
}

export async function freshOidcProofAndMaintenance({
  browser,
  app,
  accountID,
  oidc,
  oidcIdentity,
  oidcDisplayName,
  removableDisplayName,
}) {
  const context = await browser.newContext({
    baseURL: app.origin,
    viewport: { width: 1440, height: 1000 },
  })
  await context.addInitScript(
    ([apiBase]) => {
      localStorage.setItem('linguaflow.api_base_url', apiBase)
      localStorage.setItem('linguaflow.api_mode', 'server')
      localStorage.setItem('linguaflow.service.mode', 'server')
    },
    [app.apiBase],
  )
  const page = await context.newPage()
  try {
    const exchangePropertyNames = []
    await page.route('**/api/v1/auth/external/exchange', async (route) => {
      const names = Object.keys(route.request().postDataJSON()).sort()
      assert.deepEqual(names, ['ticket', 'verifier'])
      exchangePropertyNames.push(names)
      await route.continue()
    })
    const authentication = await appLogin(page, app, oidcDisplayName, oidc, oidcIdentity, {
      requirePrompt: true,
    })
    const identity = await read(page, app, '/users/me')
    assert.equal(identity.id, accountID, 'Fresh OIDC login selected a different user identity')
    const methods = await read(page, app, '/users/me/login-methods')
    assert.equal(methods.password.can_login, false)
    assert.equal(methods.password.can_set, false)
    const oidcBinding = methods.bindings.find((binding) => binding.display_name === oidcDisplayName)
    const removable = methods.bindings.find(
      (binding) => binding.display_name === removableDisplayName,
    )
    assert.ok(oidcBinding?.verified && oidcBinding.can_reauthenticate)
    assert.ok(
      removable?.can_unlink,
      'The alternate binding is not removable after maintenance proof',
    )

    await page.goto(`${app.origin}/settings/security`)
    await expect(page.locator('input[autocomplete="current-password"]')).toHaveCount(0)
    await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
    const row = page
      .locator('div.rounded-lg.border')
      .filter({ has: page.getByText(removableDisplayName, { exact: true }) })
      .filter({ has: page.getByRole('button', { name: '解绑', exact: true }) })
      .first()
    await expect(row).toBeVisible({ timeout: 30_000 })
    await row.getByRole('button', { name: '解绑', exact: true }).click()
    const proof = page.getByRole('button', {
      name: `使用 ${oidcDisplayName} 验证`,
      exact: true,
    })
    await expect(proof).toBeVisible({ timeout: 30_000 })
    await page.context().clearCookies()
    await proof.click()
    const proofPrompt = await providerLogin(page, app, oidc, oidcIdentity, {
      requirePrompt: true,
    })
    await expect(page.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible({
      timeout: 30_000,
    })
    const storageEvidence = await assertExternalAuthMemory(page)
    const oldAccess = await token(page)
    await page.getByRole('button', { name: '确认解绑', exact: true }).click()
    await expect(page).toHaveURL(/\/login(?:$|\?)/u, { timeout: 30_000 })
    assert.equal(await token(page), null, 'Sensitive maintenance did not clear the product session')
    const replay = await page.request.get(`${app.apiBase}/users/me`, {
      headers: { Authorization: `Bearer ${oldAccess}` },
    })
    assert.equal(replay.status(), 401, 'A pre-maintenance access token remained usable')
    return {
      account_id: accountID,
      fresh_provider_login_prompted: authentication.prompted,
      sensitive_operation_proof_prompted: proofPrompt.prompted,
      proof_action: 'unlink-alternate-provider',
      session_revoked: true,
      old_access_token_rejected: true,
      password_controls_hidden: true,
      exchange_property_names: exchangePropertyNames,
      external_auth_storage: storageEvidence,
    }
  } finally {
    await context.close().catch(() => {})
  }
}

export async function bindB5Alternative(input) {
  const result = await bindAlternativeWithCurrentSession(input)
  return { account_id: input.accountID ?? null, ...result }
}

function newMaintenanceContext(browser, app) {
  return browser.newContext({
    baseURL: app.origin,
    viewport: { width: 1440, height: 1000 },
  })
}

async function primeMaintenanceContext(context, app) {
  await context.addInitScript(
    ([apiBase]) => {
      localStorage.setItem('linguaflow.api_base_url', apiBase)
      localStorage.setItem('linguaflow.api_mode', 'server')
      localStorage.setItem('linguaflow.service.mode', 'server')
    },
    [app.apiBase],
  )
}

/**
 * Bind every active account to the shared maintenance OIDC connection and prove
 * each binding with an independent fresh OIDC login. Password accounts use
 * their password proof; provider-signup accounts use a fresh primary-provider
 * proof before linking the dedicated OIDC identity supplied by the caller.
 */
export async function prepareB5AccountMaintenance({
  browser,
  app,
  accounts,
  maintenanceConnection,
  oidc,
  signupConnection,
  signupProvider,
}) {
  const results = []
  for (const account of accounts) {
    assert.ok(account?.id, 'Each maintenance account needs its real product user id')
    const context = await newMaintenanceContext(browser, app)
    await primeMaintenanceContext(context, app)
    const page = await context.newPage()
    let loginKind
    let binding
    try {
      if (account.password) {
        await localPasswordLogin(page, app, account.username, account.password)
        loginKind = 'local-password'
      } else {
        assert.ok(account.primaryIdentity, 'Provider-signup accounts need a primary identity')
        await appLogin(
          page,
          app,
          signupConnection.displayName,
          signupProvider,
          account.primaryIdentity,
        )
        loginKind = 'signup-provider'
      }
      const methods = await read(page, app, '/users/me/login-methods')
      if (
        methods.bindings.some((item) => item.display_name === maintenanceConnection.displayName)
      ) {
        binding = { binding_already_present: true }
      } else {
        binding = await bindAlternativeWithCurrentSession({
          page,
          app,
          accountPassword: account.password ?? null,
          primary: account.password ? null : signupConnection,
          primaryProvider: account.password ? null : signupProvider,
          primaryIdentity: account.password ? null : account.primaryIdentity,
          secondary: maintenanceConnection,
          secondaryProvider: oidc,
          secondaryIdentity: account.oidcIdentity,
        })
      }
    } finally {
      await context.close().catch(() => {})
    }

    const verifyContext = await newMaintenanceContext(browser, app)
    await primeMaintenanceContext(verifyContext, app)
    const verifyPage = await verifyContext.newPage()
    try {
      const authentication = await appLogin(
        verifyPage,
        app,
        maintenanceConnection.displayName,
        oidc,
        account.oidcIdentity,
        { requirePrompt: true },
      )
      const identity = await read(verifyPage, app, '/users/me')
      assert.equal(
        identity.id,
        account.id,
        `Fresh maintenance OIDC login selected a different user for ${account.username}`,
      )
      const methods = await read(verifyPage, app, '/users/me/login-methods')
      const freshBinding = methods.bindings.find(
        (item) => item.display_name === maintenanceConnection.displayName,
      )
      assert.ok(
        freshBinding?.verified,
        `Maintenance binding for ${account.username} is not verified`,
      )
      assert.ok(
        freshBinding.can_reauthenticate,
        `Maintenance binding for ${account.username} cannot reauthenticate`,
      )
      results.push({
        account_id: account.id,
        username: account.username,
        login_kind: loginKind,
        binding: { ...binding, binding_id: freshBinding.binding_id },
        fresh_provider_login_prompted: authentication.prompted,
        binding_verified: true,
        can_reauthenticate: true,
      })
    } finally {
      await verifyContext.close().catch(() => {})
    }
  }
  return {
    maintenance_display_name: maintenanceConnection.displayName,
    accounts: results,
  }
}

/**
 * Ordinary-user password maintenance for the R1 account: change the password
 * through the real security form, verify the server revokes the previous
 * session, and return the new password in memory only.
 */
export async function changeB5R1Password({ browser, app, account }) {
  assert.ok(account?.id && account.username && account.password)
  const newPassword = `B5-R1-New-${randomBytes(18).toString('base64url')}`
  const context = await newMaintenanceContext(browser, app)
  await primeMaintenanceContext(context, app)
  const page = await context.newPage()
  try {
    await localPasswordLogin(page, app, account.username, account.password)
    const identity = await read(page, app, '/users/me')
    assert.equal(identity.id, account.id)
    const oldAccess = await token(page)
    await page.goto(`${app.origin}/settings/security`)
    const form = page
      .locator('form')
      .filter({ has: page.locator('input[autocomplete="current-password"]') })
      .first()
    await expect(form).toBeVisible({ timeout: 30_000 })
    await form.locator('input[autocomplete="current-password"]').fill(account.password)
    await form.locator('input[autocomplete="new-password"]').first().fill(newPassword)
    const confirmations = form.locator('input[autocomplete="new-password"]')
    if ((await confirmations.count()) > 1) await confirmations.nth(1).fill(newPassword)
    await form.locator('button[type="submit"]').click()
    await expect(page).toHaveURL(/\/login(?:$|\?)/u, { timeout: 30_000 })
    assert.equal(await token(page), null, 'Password change did not revoke the local session')
    const replay = await page.request.get(`${app.apiBase}/users/me`, {
      headers: { Authorization: `Bearer ${oldAccess}` },
    })
    assert.equal(replay.status(), 401, 'The pre-change access token survived password maintenance')
    const oldLogin = await page.request.post(`${app.apiBase}/auth/login`, {
      data: { identifier: account.username, password: account.password },
    })
    assert.equal(oldLogin.status(), 401, 'The old password remained usable after change')
    const newLogin = await page.request.post(`${app.apiBase}/auth/login`, {
      data: { identifier: account.username, password: newPassword },
    })
    assert.equal(newLogin.status(), 200, 'The new password did not log in through the real API')
    return {
      account_id: account.id,
      username: account.username,
      new_password: newPassword,
      old_access_rejected: true,
      old_password_rejected: true,
      new_password_login_passed: true,
    }
  } finally {
    await context.close().catch(() => {})
  }
}
