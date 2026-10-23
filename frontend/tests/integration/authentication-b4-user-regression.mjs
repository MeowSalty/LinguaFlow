import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from '@playwright/test'

const TIMEOUT = 45_000
const diagnosticReportPath = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../artifacts/authentication-product/b4-user-regression-last.json',
)

function requireInput({ browser, app, provider, displayName, oidc }) {
  assert.ok(browser, 'browser is required')
  assert.ok(app, 'app is required')
  assert.ok(app.origin, 'app.origin is required')
  assert.ok(app.apiBase, 'app.apiBase is required')
  assert.ok(provider, 'provider is required')
  assert.ok(provider.base, 'provider.base is required')
  assert.ok(provider.password, 'provider.password is required')
  assert.equal(typeof displayName, 'string', 'displayName must be a string')
  assert.ok(displayName.trim(), 'displayName must not be empty')
  assert.doesNotMatch(
    provider.base,
    /\/authorize\/?$/,
    'provider.base must not include the authorize path',
  )
  assert.ok(oidc, 'oidc is required for external recent authentication')
  assert.ok(oidc.displayName, 'oidc.displayName is required')
  assert.ok(oidc.provider, 'oidc.provider is required')
  assert.ok(oidc.provider.issuer, 'oidc.provider.issuer is required')
  assert.ok(oidc.provider.password, 'oidc.provider.password is required')
}

function redact(value, secrets) {
  let text = String(value?.message ?? value)
  for (const secret of secrets) {
    if (secret && text.includes(secret)) text = text.replaceAll(secret, '[redacted]')
  }
  return text
}

async function accessToken(page) {
  return page.evaluate(() => localStorage.getItem('linguaflow.access_token'))
}

async function readApi(page, app, pathname, failureLabel) {
  const token = await accessToken(page)
  assert.ok(token, `${failureLabel} requires an authenticated browser session`)
  const response = await page.request.get(`${app.apiBase}${pathname}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  assert.equal(
    response.status(),
    200,
    `${failureLabel} read-back failed with HTTP ${response.status()}`,
  )
  return response.json()
}

function sanitizedIdentity(identity) {
  return {
    id: identity?.id ?? null,
    username: typeof identity?.username === 'string' ? identity.username : '[redacted]',
    role: identity?.role ?? null,
    active: identity?.active ?? null,
    email: identity?.email ? '[redacted]' : null,
    display_name: identity?.display_name ? '[redacted]' : null,
  }
}

function sanitizedMethods(methods) {
  return {
    password: methods.password,
    email: {
      address_present: Boolean(methods.email?.address),
      verified: methods.email?.verified ?? null,
      can_recover: methods.email?.can_recover ?? null,
    },
    bindings: methods.bindings?.map((binding) => ({
      binding_id: binding.binding_id,
      connection_id: binding.connection_id,
      display_name: binding.display_name,
      can_login: binding.can_login,
      can_reauthenticate: binding.can_reauthenticate,
      can_unlink: binding.can_unlink,
      verified: binding.verified,
      archived: binding.archived,
      login_rule_status: binding.login_rule_status ?? null,
      evidence_expires_at: binding.evidence_expires_at ?? null,
    })),
    available_connections: methods.available_connections?.map((connection) => ({
      connection_id: connection.connection_id,
      display_name: connection.display_name,
      can_link: connection.can_link,
    })),
  }
}

async function writeDiagnosticReport(diagnostic) {
  await mkdir(path.dirname(diagnosticReportPath), { recursive: true })
  await writeFile(diagnosticReportPath, JSON.stringify(diagnostic, null, 2), 'utf8')
}

async function gotoLogin(page) {
  await page.goto('/login')
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible({
    timeout: 30_000,
  })
}

async function assertNoSession(page) {
  await expect(page).toHaveURL(/\/login(?:$|\?)/, { timeout: TIMEOUT })
  const token = await accessToken(page)
  assert.equal(token, null, 'a security operation did not revoke the browser session')
  await gotoLogin(page)
}

async function loginWithPassword(page, app, identifier, password) {
  await gotoLogin(page)
  await page.locator('input[autocomplete="username"]').fill(identifier)
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: TIMEOUT })
}

async function startExternalLogin(page, displayName) {
  await gotoLogin(page)
  await page.getByRole('button', { name: `使用 ${displayName} 继续`, exact: true }).click()
  await expect(page.locator('#username')).toBeVisible({ timeout: TIMEOUT })
}

async function waitForApplicationResult(page, appOrigin, expectedTarget) {
  const resultPath = expectedTarget === 'home' ? '/' : '/settings/security'
  await page.waitForURL(
    (url) =>
      url.origin === appOrigin &&
      (url.pathname === '/auth/complete' || url.pathname === resultPath),
    { timeout: TIMEOUT },
  )
  await page.waitForURL((url) => url.origin === appOrigin && url.pathname === resultPath, {
    timeout: TIMEOUT,
  })
  await expect(page).toHaveURL(
    expectedTarget === 'home' ? `${appOrigin}/` : /\/settings\/security(?:$|\?)/,
    { timeout: TIMEOUT },
  )
}

async function completeExternalFlow(
  page,
  appOrigin,
  providerOrigin,
  providerAuthorizePath,
  username,
  providerPassword,
  { expectedTarget },
) {
  await page.waitForURL(
    (url) =>
      url.origin === providerOrigin && url.pathname.replace(/\/$/, '') === providerAuthorizePath,
    { timeout: TIMEOUT },
  )
  await expect(page.locator('#username')).toBeVisible({ timeout: TIMEOUT })
  await page.locator('#username').fill(username)
  await page.locator('#password').fill(providerPassword)
  await page.locator('#oauth-approve').click()
  if (expectedTarget === 'callback') {
    await expect(page).toHaveURL(/\/auth\/complete(?:$|\?)/, { timeout: TIMEOUT })
    return
  }

  await waitForApplicationResult(page, appOrigin, expectedTarget)
}

async function completeOIDCFlow(page, appOrigin, issuer, password, { expectedTarget }) {
  const issuerOrigin = new URL(issuer).origin
  await page.waitForURL((url) => url.origin === issuerOrigin, { timeout: TIMEOUT })
  if (!(await page.locator('#password').isVisible())) {
    const username = page.locator('#username')
    if (await username.isVisible()) {
      await username.fill('provideradmin')
      await page.locator('button[type="submit"]').click()
    }
  }
  await expect(page.locator('#password')).toBeVisible({ timeout: TIMEOUT })
  if (await page.locator('#username').isVisible()) {
    await page.locator('#username').fill('provideradmin')
  }
  await page.locator('#password').fill(password)
  await page.locator('button[type="submit"]').click()
  await waitForApplicationResult(page, appOrigin, expectedTarget)
}

async function verifyWithPassword(page, password) {
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible({ timeout: 30_000 })
  await expect(dialog.getByLabel('使用密码验证')).toBeVisible({ timeout: 30_000 })
  await dialog.getByLabel('使用密码验证').fill(password)
  await dialog.getByRole('button', { name: '验证', exact: true }).click()
  await expect(dialog.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible({
    timeout: 30_000,
  })
}

async function expectSecurityMethods(page) {
  await page.goto('/settings/security')
  await expect(page.getByText('已绑定的登录方式', { exact: true })).toBeVisible({
    timeout: TIMEOUT,
  })
}

async function assertPasswordNotPersisted(page, secrets) {
  const storage = await page.evaluate(() => ({
    url: location.href,
    localStorage: Object.fromEntries(Object.entries(localStorage)),
    sessionStorage: Object.fromEntries(Object.entries(sessionStorage)),
  }))
  const serialized = JSON.stringify(storage)
  for (const secret of secrets) {
    assert.ok(
      !serialized.includes(secret),
      'sensitive password material was persisted in browser URL or storage',
    )
  }
}

/**
 * B4 ordinary-user authentication regression.
 *
 * The caller owns the deployment and must already have enabled the controlled OAuth
 * connection named by displayName. This module starts no services and mocks no APIs.
 */
export async function oauthUserRegression({ browser, app, provider, displayName, oidc }) {
  requireInput({ browser, app, provider, displayName, oidc })

  const providerBase = new URL(provider.base)
  const providerOrigin = providerBase.origin
  const providerAuthorizePath = `${providerBase.pathname.replace(/\/$/, '')}/authorize`
  const initialPassword = randomBytes(18).toString('base64url')
  let originalIdentityId
  const secrets = [provider.password, initialPassword, oidc.provider.password]
  const stages = []

  const stage = async (name, operation) => {
    try {
      await operation()
      stages.push(name)
    } catch (error) {
      throw new Error(`B4 user regression failed at ${name}: ${redact(error, secrets)}`)
    }
  }

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
    await stage('external_alice_signup_required_initial_password', async () => {
      await startExternalLogin(page, displayName)
      await completeExternalFlow(
        page,
        app.origin,
        providerOrigin,
        providerAuthorizePath,
        'alice',
        provider.password,
        { expectedTarget: 'callback' },
      )
      await expect(
        page.getByText('此登录方式需要同时设置本地密码，以便之后管理账号安全。', { exact: true }),
      ).toBeVisible({ timeout: TIMEOUT })
      await page.locator('input[autocomplete="username"]').fill('product_user')
      const passwordInputs = page.locator('input[autocomplete="new-password"]')
      await passwordInputs.nth(0).fill(initialPassword)
      await passwordInputs.nth(1).fill(initialPassword)
      await page.getByRole('button', { name: '创建账号并登录', exact: true }).click()
      await expect(page).toHaveURL(`${app.origin}/`, { timeout: TIMEOUT })
    })

    await stage('security_readback_password_and_binding', async () => {
      const identity = await readApi(page, app, '/users/me', 'post-signup identity')
      originalIdentityId = identity?.id
      assert.ok(originalIdentityId, 'post-signup identity read-back did not contain a user id')
      assert.equal(identity.username, 'product_user')
      const methods = await readApi(
        page,
        app,
        '/users/me/login-methods',
        'post-signup login methods',
      )
      const capabilities = await readApi(
        page,
        app,
        '/auth/capabilities',
        'post-signup auth capabilities',
      )
      const diagnostic = {
        stage: 'security_readback_password_and_binding',
        identity: sanitizedIdentity(identity),
        capabilities: {
          mode: capabilities.mode,
          password: capabilities.password,
          email_verification_enabled: capabilities.email_verification_enabled,
          password_reset_enabled: capabilities.password_reset_enabled,
        },
        methods: sanitizedMethods(methods),
      }
      await writeDiagnosticReport(diagnostic)
      assert.equal(methods.password.is_set, true)
      assert.equal(methods.bindings.length, 1)
      assert.equal(methods.bindings[0].display_name, displayName)
      assert.equal(methods.password.verified, false)
      assert.equal(methods.bindings[0].can_unlink, false)
      await expectSecurityMethods(page)
      await expect(page.getByRole('button', { name: '解绑', exact: true })).toBeVisible({
        timeout: 30_000,
      })
    })

    await stage('password_login_verified_fallback_readback', async () => {
      await page.getByRole('button', { name: '退出当前登录', exact: true }).click()
      await expect(page).toHaveURL(/\/login(?:$|\?)/, { timeout: TIMEOUT })
      await loginWithPassword(page, app, 'product_user', initialPassword)
      await expectSecurityMethods(page)
      const methods = await readApi(
        page,
        app,
        '/users/me/login-methods',
        'verified password login methods',
      )
      assert.equal(methods.password.verified, true)
      assert.equal(methods.password.can_login, true)
      assert.equal(methods.bindings[0].can_unlink, true)
    })

    await stage('password_recent_proof_unlink_revokes_session', async () => {
      await page.getByRole('button', { name: '解绑', exact: true }).click()
      await verifyWithPassword(page, initialPassword)
      await page.getByRole('button', { name: '确认解绑', exact: true }).click()
      await assertNoSession(page)
    })

    await stage('password_login_after_unlink', async () => {
      await loginWithPassword(page, app, 'product_user', initialPassword)
    })

    await stage('password_only_delete_is_protected', async () => {
      await expectSecurityMethods(page)
      const methods = await readApi(
        page,
        app,
        '/users/me/login-methods',
        'password-only login methods',
      )
      assert.equal(methods.password.is_set, true)
      assert.equal(methods.password.can_delete, false)
      assert.equal(methods.bindings.length, 0)
      await expect(page.getByRole('button', { name: '删除密码', exact: true })).toBeDisabled()
    })

    await stage('password_recent_proof_link_bob', async () => {
      const oauthRow = page
        .locator('div.rounded-lg.border')
        .filter({ has: page.getByText(displayName, { exact: true }) })
        .filter({ has: page.getByRole('button', { name: '绑定', exact: true }) })
        .first()
      await expect(oauthRow).toBeVisible({ timeout: 30_000 })
      await oauthRow.getByRole('button', { name: '绑定', exact: true }).click()
      await verifyWithPassword(page, initialPassword)
      await page.getByRole('button', { name: '继续', exact: true }).click()
      await completeExternalFlow(
        page,
        app.origin,
        providerOrigin,
        providerAuthorizePath,
        'bob',
        provider.password,
        { expectedTarget: 'callback' },
      )
    })

    await stage('link_confirmed_binding_readback', async () => {
      const confirmLink = page.getByRole('button', { name: '确认绑定', exact: true })
      await expect(confirmLink).toBeVisible({ timeout: TIMEOUT })
      await confirmLink.click()
      await expect(page).toHaveURL(/\/settings\/security(?:$|\?)/, { timeout: TIMEOUT })
      const methods = await readApi(page, app, '/users/me/login-methods', 'post-link login methods')
      assert.equal(methods.bindings.length, 1)
      assert.equal(methods.bindings[0].display_name, displayName)
      assert.equal(methods.bindings[0].can_reauthenticate, false)
    })

    await stage('password_recent_proof_link_oidc_provideradmin', async () => {
      const methods = await readApi(
        page,
        app,
        '/users/me/login-methods',
        'pre-oidc-bind login methods',
      )
      const connection = methods.available_connections.find(
        (item) => item.display_name === oidc.displayName,
      )
      assert.ok(connection, 'enabled OIDC connection was absent from link candidates')
      assert.equal(connection.can_link, true)
      const oidcRow = page
        .locator('div.rounded-lg.border')
        .filter({ has: page.getByText(oidc.displayName, { exact: true }) })
        .filter({ has: page.getByRole('button', { name: '绑定', exact: true }) })
        .first()
      await expect(oidcRow).toBeVisible({ timeout: 30_000 })
      await oidcRow.getByRole('button', { name: '绑定', exact: true }).click()
      await verifyWithPassword(page, initialPassword)
      await page.getByRole('button', { name: '继续', exact: true }).click()
      await completeOIDCFlow(page, app.origin, oidc.provider.issuer, oidc.provider.password, {
        expectedTarget: 'callback',
      })
    })

    await stage('oidc_recent_fallback_readback', async () => {
      const confirmLink = page.getByRole('button', { name: '确认绑定', exact: true })
      await expect(confirmLink).toBeVisible({ timeout: TIMEOUT })
      await confirmLink.click()
      await expect(page).toHaveURL(/\/settings\/security(?:$|\?)/, { timeout: TIMEOUT })
      const methods = await readApi(page, app, '/users/me/login-methods', 'oidc-bound methods')
      const oauthBinding = methods.bindings.find((item) => item.display_name === displayName)
      const oidcBinding = methods.bindings.find((item) => item.display_name === oidc.displayName)
      assert.equal(methods.bindings.length, 2)
      assert.ok(oauthBinding, 'OAuth binding disappeared before password deletion')
      assert.ok(oidcBinding, 'OIDC binding was absent after linking provideradmin')
      assert.equal(oauthBinding.can_reauthenticate, false)
      assert.equal(oidcBinding.can_reauthenticate, true)
    })

    await stage('external_recent_proof_delete_password_revokes_session', async () => {
      await expectSecurityMethods(page)
      await page.getByRole('button', { name: '删除密码', exact: true }).click()
      const externalProof = page.getByRole('button', {
        name: `使用 ${oidc.displayName} 验证`,
        exact: true,
      })
      await expect(externalProof).toBeVisible({ timeout: 30_000 })
      await externalProof.click()
      await completeOIDCFlow(page, app.origin, oidc.provider.issuer, oidc.provider.password, {
        expectedTarget: 'security',
      })
      await expect(page.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible({
        timeout: TIMEOUT,
      })
      await page.getByRole('button', { name: '确认删除', exact: true }).click()
      await assertNoSession(page)
    })

    await stage('external_bob_login_same_user_id', async () => {
      await startExternalLogin(page, displayName)
      await completeExternalFlow(
        page,
        app.origin,
        providerOrigin,
        providerAuthorizePath,
        'bob',
        provider.password,
        { expectedTarget: 'home' },
      )
      await expect(page).toHaveURL(`${app.origin}/`, { timeout: TIMEOUT })
      const identityAfter = await readApi(page, app, '/users/me', 'identity after re-login')
      assert.equal(identityAfter.id, originalIdentityId)
    })

    await stage('sensitive_password_not_persisted', async () => {
      await assertPasswordNotPersisted(page, secrets)
    })

    return stages
  } finally {
    await context.close().catch(() => {})
  }
}

export default oauthUserRegression
