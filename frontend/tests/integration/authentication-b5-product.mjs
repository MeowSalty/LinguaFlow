import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'
import { createB5RunDirectory, createB5Runtime } from './authentication-b5-runtime.mjs'
import {
  bindB5Alternative,
  prepareB5AccountMaintenance,
  changeB5R1Password,
  loginB5ExternalAccount,
  freshOidcProofAndMaintenance,
} from './authentication-b5-user-regression.mjs'
import { recoverB5LocalLogin } from './authentication-b5-recovery.mjs'
import { verifyB5ProductVisuals } from './authentication-b5-visual.mjs'

const chromiumPath =
  process.env.LINGUAFLOW_TEST_CHROMIUM ||
  path.join(
    process.env.LOCALAPPDATA ?? '',
    'ms-playwright',
    'chromium-1248',
    'chrome-win64',
    'chrome.exe',
  )
const browserKind = process.env.LINGUAFLOW_B5_BROWSER ?? 'chromium'
if (!['chromium', 'edge'].includes(browserKind))
  throw new Error(`Unsupported B5 browser: ${browserKind}`)

const protocols = ['oidc', 'oauth']
const deployments = ['sqlite', 'postgresql']
const matrix = deployments.flatMap((deployment) =>
  protocols.map((protocol) => ({ deployment, protocol, name: `${deployment}-${protocol}` })),
)
const registrationCases = ['R1', 'R2', 'R3', 'R4', 'R5', 'R6']
const maintenanceCases = [
  'management_create_edit_test_enable',
  'candidate_and_patch_wire_contract',
  'oauth_signup_maintenance_dependencies',
  'global_registration_gate',
  'existing_login_and_binding',
  'ordinary_user_maintenance',
  'fresh_oidc_maintenance_for_all_active_users_and_admin',
  'pure_external_local_shutdown',
  'cli_issue_restore_local_login',
  'recovered_password_login_and_token_replay_rejected',
  'cli_revoke_unused_authorization',
  'viewport_390px',
  'keyboard_operation',
  'real_browser_zoom_200_percent',
]

function newCheck(id) {
  return { id, status: 'unverified', consumed: false, verified: false, evidence: [] }
}

function newCell({ deployment, protocol, name }) {
  const cases = Object.fromEntries(registrationCases.map((id) => [id, newCheck(id)]))
  if (protocol === 'oauth') {
    for (const id of ['R3', 'R5', 'R6']) {
      cases[id] = {
        id,
        status: 'not_applicable',
        consumed: false,
        verified: false,
        reason:
          'The B5 OAuth matrix requires R1/R2/R4; fresh OIDC maintenance is tested separately.',
        evidence: [],
      }
    }
  }
  const checks = {
    ...cases,
    ...Object.fromEntries(maintenanceCases.map((id) => [id, newCheck(id)])),
  }
  if (protocol === 'oidc')
    checks.oauth_signup_maintenance_dependencies = {
      ...newCheck('oauth_signup_maintenance_dependencies'),
      status: 'not_applicable',
      reason: 'The OIDC registration connection provides its own verified fresh maintenance path.',
    }
  return {
    deployment,
    database: deployment,
    protocol,
    name,
    status: 'unverified',
    network: null,
    checks,
  }
}

const report = {
  objective: 'B5 T16 formal product authentication integration',
  tested_at: new Date().toISOString(),
  application_source: 'frontend/dist production build',
  backend_revision_expected: '2bd3da822a2b10df7e67fab48ce81ba7254dd1e1',
  browser: null,
  status: 'running',
  matrix: matrix.map(newCell),
  runtime: {
    status: 'unverified',
    database_isolation: 'unverified',
    identity_isolation: 'unverified',
  },
  notes: [
    'A check is consumed only after its real product workflow starts; unverified means no workflow result was observed.',
    'No product API is mocked. The controlled OAuth provider and Keycloak are real local HTTP services.',
    'The report contains request property names and sanitized evidence, never credentials or tokens.',
  ],
}

let runtime
let browser
let runDir

async function saveReport() {
  await mkdir(runDir, { recursive: true })
  await writeFile(path.join(runDir, 'report.json'), JSON.stringify(report, null, 2), 'utf8')
}

async function check(cell, id, operation) {
  const item = cell.checks[id]
  item.consumed = true
  item.started_at = new Date().toISOString()
  await saveReport()
  try {
    const evidence = await operation()
    item.status = 'passed'
    item.verified = true
    item.evidence = Array.isArray(evidence) ? evidence : evidence ? [evidence] : []
    item.completed_at = new Date().toISOString()
    await saveReport()
    return true
  } catch (error) {
    item.status = 'failed'
    item.verified = false
    item.error = String(error?.message ?? error).slice(0, 1200)
    item.completed_at = new Date().toISOString()
    await saveReport()
    throw error
  }
}

function markUnverifiedAfterFailure(cell, reason) {
  for (const item of Object.values(cell.checks)) {
    if (item.status !== 'unverified') continue
    item.reason = reason
  }
}

async function requestShapeListener(page) {
  const shapes = []
  await page.route('**/api/v1/admin/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (
      !url.pathname.includes('/admin/auth-connections') &&
      !url.pathname.includes('/admin/local-account')
    )
      return route.continue()
    const event = {
      method: request.method(),
      path: url.pathname,
      property_names: [],
      changes_property_names: [],
      query_property_names: [...url.searchParams.keys()].sort(),
      purpose: url.searchParams.get('purpose'),
    }
    try {
      const body = request.postDataJSON()
      if (body && typeof body === 'object') {
        event.property_names = Object.keys(body).sort()
        if (body.changes && typeof body.changes === 'object')
          event.changes_property_names = Object.keys(body.changes).sort()
        if (typeof body.purpose === 'string') event.purpose = body.purpose
      }
    } catch {
      // GET requests and non-JSON requests carry no shape evidence.
    }
    shapes.push(event)
    await route.continue()
  })
  return shapes
}

async function accessToken(page) {
  return page.evaluate(() => {
    const token = localStorage.getItem('linguaflow.access_token')
    if (!token) throw new Error('Expected a real authenticated product session')
    return token
  })
}

async function api(page, app, pathname, { method = 'GET', body } = {}) {
  const token = await accessToken(page)
  const response = await page.request.fetch(`${app.apiBase}${pathname}`, {
    method,
    headers: { Authorization: `Bearer ${token}` },
    ...(body === undefined ? {} : { data: body }),
  })
  let value = null
  try {
    value = await response.json()
  } catch {
    // Empty success and problem responses are returned with their actual status.
  }
  return { response, value }
}

async function expectAPI(page, app, pathname, options) {
  const result = await api(page, app, pathname, options)
  assert.ok(
    result.response.ok(),
    `${options?.method ?? 'GET'} ${pathname} returned ${result.response.status()}`,
  )
  return result.value
}

async function login(page, app, username, password) {
  await page.goto(`${app.origin}/login`)
  await expect(page.getByRole('button', { name: /登录/u }).first()).toBeVisible({ timeout: 30_000 })
  await page.locator('input[autocomplete="username"]').fill(username)
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page
    .getByRole('button', { name: /登录/u })
    .first()
    .click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 30_000 })
}

async function clickTestId(page, id, timeout = 25_000) {
  const control = page.getByTestId(id)
  await expect(control).toBeVisible({ timeout })
  await expect(control).toBeEnabled({ timeout })
  await control.click()
}

async function fillTestId(page, id, value) {
  const control = page.getByTestId(id)
  await expect(control).toBeVisible({ timeout: 25_000 })
  await control.fill(value)
}

async function expectToast(page, pattern) {
  await expect(page.getByText(pattern, { exact: false }).first()).toBeVisible({ timeout: 20_000 })
}

async function setSwitch(page, id, enabled) {
  const control = page.getByTestId(id)
  await expect(control).toBeVisible({ timeout: 20_000 })
  const active = await control.evaluate(
    (element) =>
      element.classList.contains('n-switch--active') ||
      element.getAttribute('aria-checked') === 'true',
  )
  if (active !== enabled) await control.click()
  await expect
    .poll(() =>
      control.evaluate(
        (element) =>
          element.classList.contains('n-switch--active') ||
          element.getAttribute('aria-checked') === 'true',
      ),
    )
    .toBe(enabled)
}

async function chooseValue(page, id, value) {
  const control = page.getByTestId(id)
  const nativeSelect = control.locator('select')
  if (await nativeSelect.count()) {
    await nativeSelect.selectOption(value)
    return
  }
  await expect(page.locator('.n-base-select-menu:visible')).toHaveCount(0)
  await control.click()
  const labels = {
    none: /^(?:无|不要求|不需要|none)$/iu,
    optional: /^(?:可选|选填|optional)$/iu,
    required: /^(?:必需|必填|required)$/iu,
  }
  const option = page.locator('.n-base-select-option:visible').filter({ hasText: labels[value] })
  await expect(option.first()).toBeVisible({ timeout: 10_000 })
  await option.first().click()
  await expect(control.locator('.n-base-selection-label')).toHaveText(labels[value])
  await expect(page.locator('.n-base-select-menu:visible')).toHaveCount(0)
}

async function setSecretReplace(page) {
  await page.getByTestId('auth-connection-secret-mode').click()
  await page
    .locator('.n-base-select-option')
    .filter({ hasText: '替换 Secret' })
    .getByText('替换 Secret', { exact: true })
    .click()
}

async function setPurposeSwitches(page, purposes = {}) {
  for (const purpose of ['login', 'signup', 'link']) {
    await setSwitch(page, `auth-connection-purpose-${purpose}`, purposes[purpose] ?? true)
  }
}

async function saveConnectionConfiguration(page) {
  const responsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === 'PATCH' &&
      /\/admin\/auth-connections\/[^/]+$/u.test(new URL(response.url()).pathname),
    { timeout: 20_000 },
  )
  await clickTestId(page, 'auth-connection-save-configuration')
  const response = await responsePromise
  assert.equal(response.status(), 200, 'Saved connection configuration was rejected')
  await expectToast(page, '已保存')
}

async function adminRead(page, app, id) {
  return expectAPI(page, app, `/admin/auth-connections/${id}`)
}

async function adminListForName(page, app, displayName) {
  const payload = await expectAPI(page, app, '/admin/auth-connections')
  const records = Array.isArray(payload) ? payload : (payload?.items ?? [])
  const found = records.find((item) => item?.configuration?.display_name === displayName)
  assert.ok(found, `Admin API read-back omitted connection ${displayName}`)
  return found
}

async function configureProtocol(page, kind, providers, purposes = {}) {
  if (kind === 'oidc') {
    await fillTestId(page, 'auth-connection-issuer', providers.oidc.issuer)
    await fillTestId(page, 'auth-connection-client-id', providers.oidc.clientID)
    await setSecretReplace(page)
    await fillTestId(page, 'auth-connection-secret', providers.oidc.clientSecret)
  } else {
    await fillTestId(
      page,
      'auth-connection-authorization-endpoint',
      `${providers.oauth.base}/authorize`,
    )
    await fillTestId(page, 'auth-connection-token-endpoint', `${providers.oauth.base}/token`)
    await fillTestId(page, 'auth-connection-profile-endpoint', `${providers.oauth.base}/user`)
    await fillTestId(page, 'auth-connection-client-id', providers.oauth.clientID)
    await page.getByTestId('auth-connection-subject-type').click()
    await page
      .locator('.n-base-select-option')
      .filter({ hasText: '整数' })
      .getByText('整数', { exact: true })
      .click()
    await fillTestId(page, 'auth-connection-subject-pointer', '/id')
    await setSecretReplace(page)
    await fillTestId(page, 'auth-connection-secret', providers.oauth.clientSecret)
  }
  await setPurposeSwitches(page, purposes)
}

async function setExternalSignupRequirements(page, email, password) {
  await chooseValue(page, 'auth-connection-signup-email-requirement', email)
  await chooseValue(page, 'auth-connection-signup-password-requirement', password)
}

async function authorizeConnectionTest(page, kind, provider, username) {
  await page.context().clearCookies()
  const popupPromise = page.waitForEvent('popup', { timeout: 30_000 })
  const failurePromise = page
    .waitForResponse(
      (response) => new URL(response.url()).pathname.endsWith('/test') && response.status() >= 400,
      { timeout: 0 },
    )
    .catch(() => null)
  await clickTestId(page, 'auth-connection-test')
  const first = await Promise.race([
    popupPromise,
    failurePromise.then((response) => {
      if (!response) return new Promise(() => {})
      throw new Error(`Real connection test returned HTTP ${response.status()}`)
    }),
  ])
  const popup = first
  const closed = new Promise((resolve) => popup.once('close', resolve))
  try {
    await popup.locator('#password').waitFor({ state: 'visible', timeout: 45_000 })
    if (
      await popup
        .locator('#username')
        .isVisible()
        .catch(() => false)
    )
      await popup.locator('#username').fill(username)
    await popup.locator('#password').fill(provider.password)
    if (kind === 'oidc') await popup.locator('button[type="submit"]').first().click()
    else await popup.locator('#oauth-approve').click()
    await Promise.race([closed, popup.waitForTimeout(45_000)])
  } finally {
    if (!popup.isClosed()) await popup.close().catch(() => {})
  }
  const resultCard = page
    .getByTestId('auth-connection-editor')
    .locator('.n-card')
    .filter({ hasText: '真实测试结果' })
    .last()
  await expect(resultCard.getByText('真实测试结果', { exact: true })).toBeVisible({
    timeout: 45_000,
  })
  await expect(resultCard.getByText(/支持近期认证/u)).toBeVisible({ timeout: 45_000 })
  return { provider_round_trip: true, real_test_result_visible: true }
}

async function attemptEnable(page, app, connectionID, expectedBlocked) {
  const responsePromise = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith(`/auth-connections/${connectionID}/enable`),
    { timeout: 20_000 },
  )
  await clickTestId(page, 'auth-connection-enable')
  const response = await responsePromise
  const result = await response.json()
  if (expectedBlocked) {
    assert.ok(response.status() >= 400, 'Expected the maintenance dependency to reject enable')
    await expect(page.getByText(/维护|独立|后备|可选密码/u).first()).toBeVisible({
      timeout: 20_000,
    })
    const value = await adminRead(page, app, connectionID)
    assert.notEqual(
      value.state,
      'enabled',
      'OAuth signup without a maintenance dependency was enabled',
    )
    return { enabled: false, blocked_by_maintenance_dependency: true }
  }
  assert.equal(
    response.status(),
    200,
    `Enable rejected: ${JSON.stringify({ status: response.status(), type: result?.type, issues: result?.issues?.map((issue) => ({ code: issue.code, field: issue.field })) })}`,
  )
  await expectToast(page, '连接已启用')
  const value = await adminRead(page, app, connectionID)
  assert.equal(value.state, 'enabled', 'Connection UI did not enable the saved provider')
  return { enabled: true, capabilities: value.capabilities }
}

async function createConnection(
  page,
  app,
  kind,
  displayName,
  providers,
  signupPassword,
  options = {},
) {
  await page.goto(`${app.origin}/admin/authentication`)
  await clickTestId(page, 'auth-connections-create')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible({ timeout: 20_000 })
  if (kind === 'oauth') {
    await dialog.locator('.n-base-selection').first().click()
    await page
      .locator('.n-base-select-option')
      .filter({ hasText: /通用 OAuth 2\.0|Generic OAuth 2\.0/u })
      .click()
  }
  await dialog.locator('input[aria-label="显示名称"]').fill(displayName)
  await dialog.getByRole('button', { name: '创建连接', exact: true }).click()
  await expect(page.getByTestId('auth-connection-editor')).toBeVisible({ timeout: 30_000 })
  await fillTestId(page, 'auth-connection-description', 'B5 isolated product acceptance')
  await clickTestId(page, 'auth-connection-save-presentation')
  await expectToast(page, '已保存')
  await configureProtocol(page, kind, providers, options.purposes)
  await setExternalSignupRequirements(
    page,
    options.emailRequirement ?? 'optional',
    options.passwordRequirement ?? signupPassword,
  )
  await saveConnectionConfiguration(page)
  const configured = await adminListForName(page, app, displayName)
  assert.deepEqual(configured.configuration.policy.signup, {
    email_requirement: options.emailRequirement ?? 'optional',
    password_requirement: options.passwordRequirement ?? signupPassword,
  })
  const identity =
    kind === 'oidc'
      ? providers.oidc.identities[app.name].admin
      : providers.oauth.identities[app.name].admin
  const externalProvider = kind === 'oidc' ? providers.oidc : providers.oauth
  await authorizeConnectionTest(page, kind, externalProvider, identity)
  const created = await adminListForName(page, app, displayName)
  if (options.enable !== false) await attemptEnable(page, app, created.id, false)
  return { id: created.id, displayName, kind, identity }
}

async function openConnectionEditor(page, id) {
  if (await page.getByTestId('auth-connection-editor').isVisible()) {
    await clickTestId(page, 'auth-connection-close')
    await expect(page.getByTestId('auth-connection-editor')).toBeHidden({ timeout: 20_000 })
  }
  await clickTestId(page, `auth-connection-open-${id}`)
  await expect(page.getByTestId('auth-connection-editor')).toBeVisible({ timeout: 20_000 })
}

async function setLocalPolicy(page, app, options) {
  await page.goto(`${app.origin}/admin/authentication`)
  const current = await expectAPI(page, app, '/admin/local-account')
  await clickTestId(page, 'auth-local-account-edit')
  await setSwitch(page, 'auth-local-account-password-login', options.passwordLogin)
  await setSwitch(page, 'auth-local-account-username-login', options.usernameLogin)
  await setSwitch(page, 'auth-local-account-enrollment', options.passwordEnrollment)
  await setSwitch(page, 'auth-local-account-signup', options.signupEnabled)
  if (options.emailRequirement)
    await chooseValue(page, 'auth-local-account-email-requirement', options.emailRequirement)
  const expected = {
    ...current.policy,
    password_login_enabled: options.passwordLogin,
    username_login_enabled: options.usernameLogin,
    password_enrollment_enabled: options.passwordEnrollment,
    signup: {
      ...current.policy.signup,
      enabled: options.signupEnabled,
      email_requirement: options.emailRequirement ?? current.policy.signup.email_requirement,
    },
  }
  if (JSON.stringify(expected) !== JSON.stringify(current.policy)) {
    await clickTestId(page, 'auth-local-account-save')
    await expectToast(page, '本地政策已保存。')
  } else await expect(page.getByTestId('auth-local-account-save')).toBeDisabled()
  const updated = await expectAPI(page, app, '/admin/local-account')
  assert.ok(updated.revision >= current.revision)
  assert.deepEqual(
    {
      password_login_enabled: updated.policy.password_login_enabled,
      username_login_enabled: updated.policy.username_login_enabled,
      password_enrollment_enabled: updated.policy.password_enrollment_enabled,
      signup_enabled: updated.policy.signup.enabled,
      email_requirement: updated.policy.signup.email_requirement,
    },
    {
      password_login_enabled: options.passwordLogin,
      username_login_enabled: options.usernameLogin,
      password_enrollment_enabled: options.passwordEnrollment,
      signup_enabled: options.signupEnabled,
      email_requirement: options.emailRequirement ?? updated.policy.signup.email_requirement,
    },
  )
  await clickTestId(page, 'auth-local-account-close')
  await expect(page.getByTestId('auth-local-account-editor')).toBeHidden()
  return updated
}

async function recordUserRequests(page) {
  const requests = []
  await page.route('**/api/v1/auth/{register,external/complete}', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    if (
      request.method() !== 'POST' ||
      (!url.pathname.endsWith('/auth/register') &&
        !url.pathname.endsWith('/auth/external/complete'))
    )
      return route.continue()
    const event = { path: url.pathname, property_names: [] }
    try {
      const body = request.postDataJSON()
      if (body && typeof body === 'object') event.property_names = Object.keys(body).sort()
    } catch {
      // Keep property names only; never retain authentication request values.
    }
    requests.push(event)
    await route.continue()
  })
  return requests
}

async function registerLocal(page, app, username, password, email) {
  await page.goto(`${app.origin}/register`)
  await expect(page.locator('input[autocomplete="username"]')).toBeVisible({ timeout: 20_000 })
  await page.locator('input[autocomplete="username"]').fill(username)
  const passwords = page.locator('input[autocomplete="new-password"]')
  await passwords.first().fill(password)
  if ((await passwords.count()) > 1) await passwords.nth(1).fill(password)
  if (email !== undefined) await page.locator('input[autocomplete="email"]').fill(email)
  await page
    .getByRole('button', { name: /注册|创建账号/u })
    .last()
    .click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 30_000 })
  const identity = await expectAPI(page, app, '/users/me')
  assert.equal(identity.username, username)
  return identity
}

async function startExternalSignup(page, app, displayName) {
  await page.goto(`${app.origin}/login`)
  await page.evaluate(() => {
    for (const storage of [localStorage, sessionStorage]) {
      for (const key of Object.keys(storage)) {
        if (key.startsWith('linguaflow.') && /token|auth|external|user|session/u.test(key))
          storage.removeItem(key)
      }
    }
  })
  await page.goto(`${app.origin}/register`)
  const button = page.getByRole('button', { name: `使用 ${displayName} 继续`, exact: true })
  await expect(button).toBeVisible({ timeout: 20_000 })
  await button.click()
}

async function completeProviderLogin(page, app, kind, provider, username) {
  const providerOrigin =
    kind === 'oidc' ? new URL(provider.issuer).origin : new URL(provider.base).origin
  await page.waitForURL((url) => url.origin === providerOrigin, { timeout: 30_000 })
  if (kind === 'oidc') {
    await expect(page.locator('#password')).toBeVisible({ timeout: 45_000 })
    const userInput = page.locator('#username')
    if (await userInput.isVisible().catch(() => false)) await userInput.fill(username)
    await page.locator('#password').fill(provider.password)
    await page.locator('button[type="submit"]').first().click()
  } else {
    await expect(page.locator('#username')).toBeVisible({ timeout: 30_000 })
    await page.locator('#username').fill(username)
    await page.locator('#password').fill(provider.password)
    await page.locator('#oauth-approve').click()
  }
  await expect(page).toHaveURL(/\/auth\/complete(?:$|\?)/u, { timeout: 45_000 })
  await expect(page.getByRole('button', { name: '创建账号并登录', exact: true })).toBeVisible({
    timeout: 30_000,
  })
  assert.equal(new URL(page.url()).origin, app.origin)
}

async function completeExternalSignup(page, app, kind, provider, identity, options) {
  await page.context().clearCookies()
  const exchangedPromise = page.waitForResponse(
    (response) => new URL(response.url()).pathname.endsWith('/auth/external/exchange'),
    { timeout: 45_000 },
  )
  await startExternalSignup(page, app, options.displayName)
  await completeProviderLogin(page, app, kind, provider, identity)
  const exchanged = await exchangedPromise
  assert.equal(exchanged.status(), 200, 'The external ticket exchange failed')
  const snapshot = await exchanged.json()
  assert.equal(snapshot.kind, 'signup_confirmation')
  assert.equal(
    snapshot.signup?.password_requirement,
    options.passwordRequirement,
    `Signup snapshot disagreed with the saved requirements: ${JSON.stringify(snapshot.signup)}`,
  )
  const passwordInput = page.locator('input[autocomplete="new-password"]').first()
  if (options.passwordRequirement === 'none') {
    await expect(passwordInput).toHaveCount(0)
  } else if (options.passwordRequirement === 'required') {
    await expect(passwordInput).toBeVisible()
  } else {
    const enable = page.getByRole('switch')
    await expect(enable).toHaveAttribute('aria-checked', 'false')
    if (options.setPassword) await enable.click()
    if (options.setPassword) {
      await expect(passwordInput).toBeVisible()
    } else {
      await expect(passwordInput).toHaveCount(0)
    }
    assert.ok(await enable.isVisible())
  }
  if (options.accountUsername) {
    const usernameField = page.locator('input[autocomplete="username"]')
    if (await usernameField.isVisible().catch(() => false))
      await usernameField.fill(options.accountUsername)
  }
  if (options.email !== undefined)
    await page.locator('input[autocomplete="email"]').fill(options.email)
  const completeRequests = options.requests
  const passwordSelected =
    options.passwordRequirement === 'required' || options.setPassword === true
  let emptyPasswordBlocked = false
  if (passwordSelected) {
    const attempts = completeRequests.length
    await page.getByRole('button', { name: '创建账号并登录', exact: true }).click()
    await expect(page.getByText('新密码至少需要 8 个 Unicode 字符')).toBeVisible()
    assert.equal(completeRequests.length, attempts, 'An empty selected password reached completion')
    emptyPasswordBlocked = true
    await passwordInput.fill(options.initialPassword)
    const confirmation = page.locator('input[autocomplete="new-password"]').nth(1)
    if (await confirmation.count()) await confirmation.fill(options.initialPassword)
  }
  const before = completeRequests.length
  await page.getByRole('button', { name: '创建账号并登录', exact: true }).click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 45_000 })
  const event = completeRequests
    .slice(before)
    .find((item) => item.path.endsWith('/auth/external/complete'))
  assert.ok(event, 'The real external confirmation request was not observed')
  const includesInitialPassword = event.property_names.includes('initial_password')
  assert.equal(
    includesInitialPassword,
    options.setPassword === true || options.passwordRequirement === 'required',
    'External confirmation did not include or omit initial_password as selected by the user',
  )
  const account = await expectAPI(page, app, '/users/me')
  const methods = await expectAPI(page, app, '/users/me/login-methods')
  assert.equal(methods.password.is_set, includesInitialPassword)
  assert.ok(methods.bindings.some((binding) => binding.display_name === options.displayName))
  return {
    id: account.id,
    username: account.username,
    password_set: methods.password.is_set,
    external_binding: options.displayName,
    complete_property_names: event.property_names,
    empty_selected_password_blocked: emptyPasswordBlocked,
  }
}

async function updateConnectionRequirements(
  page,
  app,
  connection,
  kind,
  provider,
  identity,
  email,
  password,
) {
  await page.goto(`${app.origin}/admin/authentication`)
  await openConnectionEditor(page, connection.id)
  await setExternalSignupRequirements(page, email, password)
  await authorizeConnectionTest(page, kind, provider, identity)
  await saveConnectionConfiguration(page)
  const saved = await adminRead(page, app, connection.id)
  assert.equal(saved.state, 'enabled')
  assert.deepEqual(
    saved.configuration.policy.signup,
    {
      email_requirement: email,
      password_requirement: password,
    },
    'The saved signup requirements differ from the selected values',
  )
  return saved
}

async function rejectOAuthSignupWithoutMaintenance(page, app, connection) {
  const evidence = []
  for (const requirement of ['none', 'optional']) {
    await page.goto(`${app.origin}/admin/authentication`)
    await openConnectionEditor(page, connection.id)
    const before = await adminRead(page, app, connection.id)
    await setExternalSignupRequirements(page, 'optional', requirement)
    const responsePromise = page.waitForResponse(
      (response) =>
        response.request().method() === 'PATCH' &&
        new URL(response.url()).pathname.endsWith(`/auth-connections/${connection.id}`),
      { timeout: 20_000 },
    )
    await expect(page.getByTestId('auth-connection-save-configuration')).toBeEnabled()
    await clickTestId(page, 'auth-connection-save-configuration')
    const response = await responsePromise
    const problem = await response.json()
    assert.equal(response.status(), 409)
    assert.equal(problem.type, 'urn:linguaflow:auth-connection-configuration-incomplete')
    assert.ok(problem.issues?.some((issue) => issue.code === 'maintenance-path-required'))
    const after = await adminRead(page, app, connection.id)
    assert.equal(after.aggregate_revision, before.aggregate_revision)
    assert.deepEqual(after.configuration.policy, before.configuration.policy)
    evidence.push({
      password_requirement: requirement,
      status: response.status(),
      problem_type: problem.type,
      issue_codes: problem.issues.map((issue) => issue.code),
      saved_policy_unchanged: true,
    })
  }
  await page.goto(`${app.origin}/admin/authentication`)
  return evidence
}

async function prepareExternalPoliciesForLocalShutdown(page, app, connections) {
  const policies = []
  for (const connection of connections) {
    await page.goto(`${app.origin}/admin/authentication`)
    await openConnectionEditor(page, connection.id)
    const before = await adminRead(page, app, connection.id)
    if (connection.kind === 'oidc')
      await setExternalSignupRequirements(
        page,
        before.configuration.policy.signup.email_requirement,
        'none',
      )
    else await setSwitch(page, 'auth-connection-purpose-signup', false)
    if (await page.getByTestId('auth-connection-save-configuration').isEnabled())
      await saveConnectionConfiguration(page)
    const saved = await adminRead(page, app, connection.id)
    assert.equal(saved.state, 'enabled')
    assert.equal(saved.configuration.policy.login_enabled, true)
    assert.equal(saved.configuration.policy.link_enabled, true)
    if (connection.kind === 'oidc')
      assert.equal(saved.configuration.policy.signup.password_requirement, 'none')
    else assert.equal(saved.configuration.policy.signup_enabled, false)
    policies.push({
      connection_id: connection.id,
      kind: connection.kind,
      signup_enabled: saved.configuration.policy.signup_enabled,
      password_requirement: saved.configuration.policy.signup.password_requirement,
    })
  }
  return policies
}

async function logout(page) {
  const origin = new URL(page.url()).origin
  await page.goto(`${origin}/settings/security`)
  const button = page.getByRole('button', { name: /退出当前登录|退出登录/u }).first()
  await expect(button).toBeVisible({ timeout: 20_000 })
  await button.click()
  await expect(page).toHaveURL(/\/login(?:$|\?)/u, { timeout: 20_000 })
}

async function setGlobalRegistration(page, app, enabled) {
  await page.goto(`${app.origin}/admin/settings`)
  const control = page.getByRole('switch', { name: '允许公开注册', exact: true })
  await expect(control).toBeVisible({ timeout: 30_000 })
  const current = await expectAPI(page, app, '/admin/settings')
  if (current.settings.registration_enabled !== enabled) {
    await control.click()
    await page.getByRole('button', { name: '保存注册设置', exact: true }).click()
  }
  await expect
    .poll(async () => (await expectAPI(page, app, '/admin/settings')).settings.registration_enabled)
    .toBe(enabled)
}

async function expectLocalSignupBlocked(page, app) {
  const context = await newContext(app)
  try {
    const publicPage = await context.newPage()
    await publicPage.goto(`${app.origin}/register`)
    await expect(
      publicPage.getByText('此实例暂未开放注册。已有账号仍可登录。', { exact: true }),
    ).toBeVisible({
      timeout: 20_000,
    })
    await expect(publicPage.locator('input[autocomplete="username"]')).toHaveCount(0)
    await expect(publicPage.getByRole('button', { name: /^使用 .+ 继续$/u })).toHaveCount(0)
  } finally {
    await context.close()
  }
}

async function newContext(app, options = {}) {
  const context = await browser.newContext({
    baseURL: app.origin,
    viewport: { width: 1440, height: 1000 },
    ...options,
  })
  await context.addInitScript(
    ([apiBase]) => {
      localStorage.setItem('linguaflow.api_base_url', apiBase)
      localStorage.setItem('linguaflow.api_mode', 'server')
      localStorage.setItem('linguaflow.service.mode', 'server')
    },
    [app.apiBase],
  )
  return context
}

async function captureSessionTokens(page) {
  const tokens = await page.evaluate(() => ({
    access_token: localStorage.getItem('linguaflow.access_token'),
    refresh_token: localStorage.getItem('linguaflow.refresh_token'),
  }))
  assert.ok(tokens.access_token, 'The target administrator session lost its access token')
  assert.ok(tokens.refresh_token, 'The target administrator session lost its refresh token')
  return tokens
}

async function attemptBlockedLocalRegistration(page, app, requests, username, password, email) {
  await page.goto(`${app.origin}/register`)
  await expect(page.locator('input[autocomplete="username"]')).toBeVisible({ timeout: 20_000 })
  await page.locator('input[autocomplete="username"]').fill(username)
  const passwords = page.locator('input[autocomplete="new-password"]')
  await passwords.first().fill(password)
  if ((await passwords.count()) > 1) await passwords.nth(1).fill(password)
  await page.locator('input[autocomplete="email"]').fill(email)
  const before = requests.length
  await page
    .getByRole('button', { name: /注册|创建账号/u })
    .last()
    .click()
  await page.waitForTimeout(1500)
  assert.equal(requests.length, before, 'A blocked local registration reached the product API')
  assert.equal(new URL(page.url()).pathname, '/register')
}

function cellStatus(cell) {
  const checks = Object.values(cell.checks)
  if (checks.some((item) => item.status === 'failed')) return 'failed'
  if (checks.some((item) => item.status === 'unverified')) return 'partial'
  return 'passed'
}

function cellComplete(cell) {
  return Object.values(cell.checks).every(
    (item) => item.status === 'passed' || item.status === 'not_applicable',
  )
}

async function runCell(app, cell) {
  const context = await newContext(app)
  const page = await context.newPage()
  const requests = await recordUserRequests(page)
  const adminRequests = await requestShapeListener(page)
  const accounts = []
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  try {
    await login(page, app, 'admin', app.adminPassword)
    await setLocalPolicy(page, app, {
      passwordLogin: true,
      usernameLogin: true,
      passwordEnrollment: true,
      signupEnabled: true,
      emailRequirement: 'optional',
    })

    await check(cell, 'R1', async () => {
      await logout(page)
      await login(page, app, 'admin', app.adminPassword)
      await setGlobalRegistration(page, app, false)
      await expectLocalSignupBlocked(page, app)
      await setGlobalRegistration(page, app, true)
      await logout(page)
      const account = await registerLocal(page, app, `${app.name}_r1`, 'B5-Password-R1!', undefined)
      accounts.push({ ...account, password: 'B5-Password-R1!', oidcIdentity: app.identities.link })
      const event = requests.at(-1)
      assert.equal(event?.path.endsWith('/auth/register'), true)
      assert.equal(event.property_names.includes('email'), false)
      await logout(page)
      return {
        username: account.username,
        register_property_names: event.property_names,
        email_omitted: true,
        global_registration_gate: 'closed_local_signup_hidden_then_restored',
      }
    })

    await login(page, app, 'admin', app.adminPassword)
    const policy = await setLocalPolicy(page, app, {
      passwordLogin: true,
      usernameLogin: true,
      passwordEnrollment: true,
      signupEnabled: true,
      emailRequirement: 'required',
    })
    assert.equal(
      policy.capabilities.can_signup,
      true,
      'Required email signup remains available without SMTP; validation blocks empty or malformed input',
    )
    await logout(page)

    await check(cell, 'R2', async () => {
      const bypassStatuses = []
      for (const email of [undefined, 'not-an-email']) {
        const response = await page.request.post(`${app.apiBase}/auth/register`, {
          data: {
            username: `${app.name}_r2_bypass`,
            password: 'B5-Password-R2!',
            ...(email === undefined ? {} : { email }),
          },
        })
        assert.equal(
          response.status(),
          400,
          'The server accepted a required missing or invalid email',
        )
        bypassStatuses.push(response.status())
      }
      await attemptBlockedLocalRegistration(
        page,
        app,
        requests,
        `${app.name}_r2`,
        'B5-Password-R2!',
        '',
      )
      await attemptBlockedLocalRegistration(
        page,
        app,
        requests,
        `${app.name}_r2_invalid`,
        'B5-Password-R2!',
        'not-an-email',
      )
      const account = await registerLocal(
        page,
        app,
        `${app.name}_r2`,
        'B5-Password-R2!',
        `${app.name}.r2@example.invalid`,
      )
      accounts.push({
        ...account,
        password: 'B5-Password-R2!',
        oidcIdentity: app.identities.restore,
      })
      const event = requests.at(-1)
      assert.equal(event?.path.endsWith('/auth/register'), true)
      assert.equal(event.property_names.includes('email'), true)
      const methods = await expectAPI(page, app, '/users/me/login-methods')
      assert.equal(methods.email.verified, false)
      assert.equal(methods.email.can_recover, false)
      await logout(page)
      return {
        username: account.username,
        blocked_inputs: ['missing_email', 'invalid_email'],
        server_bypass_statuses: bypassStatuses,
        email_verified: methods.email.verified,
        register_property_names: event.property_names,
      }
    })

    const providers = {
      oidc: { ...app.keycloak, identities: runtime.identities },
      oauth: { ...app.oauth, identities: runtime.identities },
    }
    const displayName = `${app.name} B5 signup`
    const signupKind = cell.protocol
    const signupProvider = cell.protocol === 'oidc' ? providers.oidc : providers.oauth
    await login(page, app, 'admin', app.adminPassword)
    const signupConnection = await createConnection(
      page,
      app,
      signupKind,
      displayName,
      providers,
      cell.protocol === 'oidc' ? 'none' : 'required',
      { purposes: { login: true, signup: true, link: true } },
    )
    await check(cell, 'management_create_edit_test_enable', async () => {
      const saved = await adminRead(page, app, signupConnection.id)
      assert.equal(saved.state, 'enabled')
      assert.equal(saved.validation.fresh_supported, cell.protocol === 'oidc')
      return {
        real_test: true,
        state: saved.state,
        fresh_supported: saved.validation.fresh_supported,
      }
    })
    await check(cell, 'candidate_and_patch_wire_contract', () => {
      const patches = adminRequests.filter((request) => request.method === 'PATCH')
      assert.ok(patches.some((request) => request.path.endsWith('/admin/local-account')))
      for (const request of patches) {
        assert.ok(request.property_names.includes('expected_revision'))
        if (request.path.endsWith('/admin/local-account'))
          assert.deepEqual(request.property_names, ['expected_revision', 'policy'])
        else assert.equal(request.property_names.includes('changes'), false)
      }
      const directories = adminRequests.filter((request) => request.path.endsWith('/attributes'))
      for (const purpose of ['login', 'signup', 'link']) {
        assert.ok(
          directories.some(
            (request) =>
              request.method === 'GET' &&
              request.purpose === purpose &&
              request.query_property_names.includes('expected_revision'),
          ),
        )
        assert.ok(
          directories.some(
            (request) =>
              request.method === 'POST' &&
              request.purpose === purpose &&
              ['changes', 'expected_revision', 'purpose'].every((key) =>
                request.property_names.includes(key),
              ),
          ),
        )
      }
      return { patch_shapes: patches, directory_shapes: directories }
    })

    if (cell.protocol === 'oidc') {
      await check(cell, 'R3', async () => {
        const account = await completeExternalSignup(
          page,
          app,
          signupKind,
          signupProvider,
          app.identities.member,
          {
            displayName,
            passwordRequirement: 'none',
            initialPassword: 'B5-External-R3!',
            requests,
            accountUsername: `${app.name}_r3`,
          },
        )
        accounts.push({
          ...account,
          oidcIdentity: app.identities.member,
          primaryIdentity: app.identities.member,
        })
        await logout(page)
        return {
          username: account.username,
          password_set: account.password_set,
          complete_property_names: account.complete_property_names,
        }
      })

      await login(page, app, 'admin', app.adminPassword)
      await setLocalPolicy(page, app, {
        passwordLogin: true,
        usernameLogin: true,
        passwordEnrollment: true,
        signupEnabled: false,
      })
      await updateConnectionRequirements(
        page,
        app,
        signupConnection,
        signupKind,
        signupProvider,
        signupConnection.identity,
        'optional',
        'required',
      )
      await logout(page)
      await check(cell, 'R4', async () => {
        const account = await completeExternalSignup(
          page,
          app,
          signupKind,
          signupProvider,
          app.identities.required,
          {
            displayName,
            passwordRequirement: 'required',
            initialPassword: 'B5-External-R4!',
            requests,
            accountUsername: `${app.name}_r4`,
          },
        )
        accounts.push({
          ...account,
          password: 'B5-External-R4!',
          oidcIdentity: app.identities.required,
          primaryIdentity: app.identities.required,
        })
        const methods = await expectAPI(page, app, '/users/me/login-methods')
        assert.equal(methods.password.is_set, true)
        await logout(page)
        return {
          username: account.username,
          password_set: methods.password.is_set,
          empty_selected_password_blocked: account.empty_selected_password_blocked,
          complete_property_names: account.complete_property_names,
        }
      })

      await login(page, app, 'admin', app.adminPassword)
      await updateConnectionRequirements(
        page,
        app,
        signupConnection,
        signupKind,
        signupProvider,
        signupConnection.identity,
        'optional',
        'optional',
      )
      await logout(page)
      await check(cell, 'R5', async () => {
        const account = await completeExternalSignup(
          page,
          app,
          signupKind,
          signupProvider,
          app.identities.optionalSkip,
          {
            displayName,
            passwordRequirement: 'optional',
            setPassword: false,
            initialPassword: 'B5-External-R5!',
            requests,
            accountUsername: `${app.name}_r5`,
          },
        )
        accounts.push({
          ...account,
          oidcIdentity: app.identities.optionalSkip,
          primaryIdentity: app.identities.optionalSkip,
        })
        await logout(page)
        return {
          username: account.username,
          password_set: account.password_set,
          complete_property_names: account.complete_property_names,
        }
      })

      await check(cell, 'R6', async () => {
        const account = await completeExternalSignup(
          page,
          app,
          signupKind,
          signupProvider,
          app.identities.optionalSet,
          {
            displayName,
            passwordRequirement: 'optional',
            setPassword: true,
            initialPassword: 'B5-External-R6!',
            requests,
            accountUsername: `${app.name}_r6`,
          },
        )
        accounts.push({
          ...account,
          password: 'B5-External-R6!',
          oidcIdentity: app.identities.optionalSet,
          primaryIdentity: app.identities.optionalSet,
        })
        const methods = await expectAPI(page, app, '/users/me/login-methods')
        assert.equal(methods.password.is_set, true)
        await logout(page)
        return {
          username: account.username,
          password_set: methods.password.is_set,
          empty_selected_password_blocked: account.empty_selected_password_blocked,
          complete_property_names: account.complete_property_names,
        }
      })
    } else {
      await logout(page)
      await login(page, app, 'admin', app.adminPassword)
      await setLocalPolicy(page, app, {
        passwordLogin: true,
        usernameLogin: true,
        passwordEnrollment: true,
        signupEnabled: false,
      })
      await logout(page)
      await check(cell, 'R4', async () => {
        const account = await completeExternalSignup(
          page,
          app,
          signupKind,
          signupProvider,
          app.identities.required,
          {
            displayName,
            passwordRequirement: 'required',
            initialPassword: 'B5-External-R4!',
            requests,
            accountUsername: `${app.name}_r4`,
          },
        )
        accounts.push({
          ...account,
          password: 'B5-External-R4!',
          oidcIdentity: app.identities.required,
          primaryIdentity: app.identities.required,
        })
        const methods = await expectAPI(page, app, '/users/me/login-methods')
        assert.equal(methods.password.is_set, true)
        await logout(page)
        return {
          username: account.username,
          password_set: methods.password.is_set,
          complete_property_names: account.complete_property_names,
        }
      })
    }

    await login(page, app, 'admin', app.adminPassword)
    const administrator = await expectAPI(page, app, '/users/me')
    accounts.push({
      ...administrator,
      password: app.adminPassword,
      oidcIdentity: app.identities.admin,
    })
    const closedPolicy = await expectAPI(page, app, '/admin/local-account')
    const closedSettings = await expectAPI(page, app, '/admin/settings')
    assert.equal(
      closedPolicy.policy.signup.enabled,
      false,
      'The registration phase must leave local signup closed before maintenance checks',
    )
    cell.closed_state = {
      local_signup_enabled: closedPolicy.policy.signup.enabled,
      global_registration_enabled: closedSettings.settings.registration_enabled,
      password_login_enabled: closedPolicy.policy.password_login_enabled,
      username_login_enabled: closedPolicy.policy.username_login_enabled,
      password_enrollment_enabled: closedPolicy.policy.password_enrollment_enabled,
    }

    const adminStorageState = await context.storageState()
    const visuals = await verifyB5ProductVisuals({
      browser,
      app,
      runDir,
      adminStorageState,
      chromiumPath,
    })
    for (const [id, evidence] of Object.entries(visuals)) await check(cell, id, () => evidence)

    const maintenanceConnection =
      cell.protocol === 'oidc'
        ? signupConnection
        : await createConnection(
            page,
            app,
            'oidc',
            `${app.name} B5 OIDC maintenance`,
            providers,
            'optional',
            { purposes: { login: true, signup: true, link: true } },
          )
    if (cell.protocol === 'oauth')
      await check(cell, 'oauth_signup_maintenance_dependencies', () =>
        rejectOAuthSignupWithoutMaintenance(page, app, signupConnection),
      )
    await bindB5Alternative({
      page,
      app,
      accountPassword: app.adminPassword,
      primary: null,
      primaryProvider: null,
      primaryIdentity: null,
      secondary: maintenanceConnection,
      secondaryProvider: providers.oidc,
      secondaryIdentity: app.identities.admin,
    })
    await check(cell, 'ordinary_user_maintenance', async () => {
      const account = accounts.find((entry) => entry.username === `${app.name}_r1`)
      const { new_password, ...evidence } = await changeB5R1Password({ browser, app, account })
      account.password = new_password
      runtime.secrets.push(new_password)
      return evidence
    })
    await check(cell, 'fresh_oidc_maintenance_for_all_active_users_and_admin', async () => {
      const evidence = await prepareB5AccountMaintenance({
        browser,
        app,
        accounts,
        maintenanceConnection,
        oidc: providers.oidc,
        signupConnection,
        signupProvider,
      })
      const active = await expectAPI(page, app, '/admin/users?limit=100')
      const ids = (active.items ?? [])
        .filter((entry) => entry.active)
        .map((entry) => entry.id)
        .sort((a, b) => a - b)
      assert.deepEqual(
        ids,
        accounts.map((entry) => entry.id).sort((a, b) => a - b),
        'Every active user including administrators must be verified',
      )
      return evidence
    })
    const alternateConnection = await createConnection(
      page,
      app,
      'oidc',
      `${app.name} B5 alternate`,
      providers,
      'none',
      { purposes: { login: true, signup: false, link: true } },
    )
    const r1Account = accounts.find((entry) => entry.username === `${app.name}_r1`)
    await check(cell, 'existing_login_and_binding', async () => {
      await setGlobalRegistration(page, app, false)
      await expectLocalSignupBlocked(page, app)
      await logout(page)
      await loginB5ExternalAccount({
        page,
        app,
        connection: maintenanceConnection,
        provider: providers.oidc,
        identity: app.identities.admin,
      })
      assert.equal((await expectAPI(page, app, '/users/me')).id, administrator.id)
      const userContext = await newContext(app)
      const userPage = await userContext.newPage()
      let binding
      try {
        await login(userPage, app, r1Account.username, r1Account.password)
        binding = await bindB5Alternative({
          page: userPage,
          app,
          accountID: r1Account.id,
          accountPassword: r1Account.password,
          secondary: alternateConnection,
          secondaryProvider: providers.oidc,
          secondaryIdentity: r1Account.oidcIdentity,
        })
      } finally {
        await userContext.close().catch(() => {})
      }
      if (cell.protocol === 'oauth') {
        const oauthContext = await newContext(app)
        const oauthPage = await oauthContext.newPage()
        try {
          const r4Account = accounts.find((entry) => entry.username === `${app.name}_r4`)
          await loginB5ExternalAccount({
            page: oauthPage,
            app,
            connection: signupConnection,
            provider: signupProvider,
            identity: r4Account.primaryIdentity,
          })
          assert.equal((await expectAPI(oauthPage, app, '/users/me')).id, r4Account.id)
        } finally {
          await oauthContext.close().catch(() => {})
        }
      }
      await setGlobalRegistration(page, app, true)
      return { global_signup_closed: true, existing_provider_login: true, binding }
    })
    await check(cell, 'global_registration_gate', () => cell.checks.R1.evidence)
    const oldSession = await captureSessionTokens(page)
    const shutdownPolicies = await prepareExternalPoliciesForLocalShutdown(page, app, [
      ...new Map(
        [signupConnection, maintenanceConnection, alternateConnection].map((entry) => [
          entry.id,
          entry,
        ]),
      ).values(),
    ])
    const beforeShutdown = {
      local: await expectAPI(page, app, '/admin/local-account'),
      connections: await expectAPI(page, app, '/admin/auth-connections'),
      settings: await expectAPI(page, app, '/admin/settings'),
    }
    await check(cell, 'pure_external_local_shutdown', async () => {
      await setLocalPolicy(page, app, {
        passwordLogin: false,
        usernameLogin: false,
        passwordEnrollment: false,
        signupEnabled: false,
      })
      const capabilitiesResponse = await page.request.get(`${app.apiBase}/auth/capabilities`)
      assert.equal(capabilitiesResponse.status(), 200)
      const capabilities = await capabilitiesResponse.json()
      assert.equal(capabilities.local_account?.can_login, false)
      assert.equal(capabilities.local_account?.can_signup, false)
      const oldPassword = await page.request.post(`${app.apiBase}/auth/login`, {
        data: { identifier: 'admin', password: app.adminPassword },
      })
      assert.equal(oldPassword.status(), 401, 'Shutdown left the old administrator password usable')
      const oldPasswordSession = await page.request.post(`${app.apiBase}/auth/login`, {
        data: { identifier: r1Account.username, password: r1Account.password },
      })
      assert.equal(oldPasswordSession.status(), 401)
      await page.goto(`${app.origin}/settings/security`)
      await expect(page.locator('input[autocomplete="current-password"]')).toHaveCount(0)
      await expect(page.locator('input[autocomplete="new-password"]')).toHaveCount(0)
      const maintenance = await freshOidcProofAndMaintenance({
        browser,
        app,
        accountID: r1Account.id,
        oidc: providers.oidc,
        oidcIdentity: r1Account.oidcIdentity,
        oidcDisplayName: maintenanceConnection.displayName,
        removableDisplayName: alternateConnection.displayName,
      })
      return {
        public_capabilities: { can_login: false, can_signup: false },
        old_admin_password_rejected: true,
        ordinary_user_password_rejected: true,
        administrator_password_controls_hidden: true,
        explicit_external_signup_adjustments: shutdownPolicies,
        ordinary_user_fresh_maintenance: maintenance,
        local_policy: {
          password_login: false,
          username_login: false,
          password_enrollment: false,
          signup: false,
        },
      }
    })

    const recovery = await recoverB5LocalLogin({
      runtime,
      app,
      username: 'admin',
      oldPassword: app.adminPassword,
      oldSessions: [oldSession],
    })
    assert.ok(recovery?.password, 'Recovery helper did not return a new administrator password')
    runtime.secrets.push(recovery.password)
    const recoveryEvidence = recovery.evidence ?? {}
    for (const key of [
      'issue_restore',
      'consumed_authorization_replay',
      'consumed_authorization_revoke',
      'unused_authorization_revoke',
      'revoked_authorization_restore',
    ]) {
      assert.equal(
        recoveryEvidence[key],
        key === 'consumed_authorization_replay' ||
          key === 'consumed_authorization_revoke' ||
          key === 'revoked_authorization_restore'
          ? 'rejected'
          : 'passed',
        `Recovery evidence check failed: ${key}`,
      )
    }
    assert.equal(recoveryEvidence.old_admin_password, 'rejected')
    assert.equal(recoveryEvidence.old_sessions.length, 1)
    assert.equal(recoveryEvidence.local_login_restored, true)
    assert.equal(recoveryEvidence.local_signup_closed, true)

    await check(cell, 'cli_issue_restore_local_login', () => ({
      target: 'admin',
      token_and_password_private_files: true,
      stdin_used: false,
      issue_restore: recoveryEvidence.issue_restore,
      revoked_authorization_restore_rejected: recoveryEvidence.revoked_authorization_restore,
    }))

    await check(cell, 'recovered_password_login_and_token_replay_rejected', async () => {
      const context = await newContext(app)
      const recoveredPage = await context.newPage()
      try {
        await login(recoveredPage, app, 'admin', recovery.password)
        const capabilitiesResponse = await recoveredPage.request.get(
          `${app.apiBase}/auth/capabilities`,
        )
        assert.equal(capabilitiesResponse.status(), 200)
        const capabilities = await capabilitiesResponse.json()
        assert.equal(capabilities.local_account?.can_login, true)
        assert.equal(capabilities.local_account?.can_signup, false)
        const after = {
          local: await expectAPI(recoveredPage, app, '/admin/local-account'),
          connections: await expectAPI(recoveredPage, app, '/admin/auth-connections'),
          settings: await expectAPI(recoveredPage, app, '/admin/settings'),
        }
        assert.deepEqual(after.connections, beforeShutdown.connections)
        assert.deepEqual(after.settings, beforeShutdown.settings)
        assert.equal(after.local.policy.password_login_enabled, true)
        assert.equal(after.local.policy.username_login_enabled, true)
        assert.equal(after.local.policy.signup.enabled, false)
        assert.equal(after.local.policy.password_enrollment_enabled, false)
        assert.equal(
          after.local.policy.email_login_enabled,
          beforeShutdown.local.policy.email_login_enabled,
        )
        assert.equal(
          after.local.policy.signup.email_requirement,
          beforeShutdown.local.policy.signup.email_requirement,
        )
        assert.ok(after.local.revision >= beforeShutdown.local.revision)
        return {
          new_admin_password_ui_login: true,
          public_capabilities: { can_login: true, can_signup: false },
          old_password_rejected: recoveryEvidence.old_admin_password === 'rejected',
          old_session_rejections: recoveryEvidence.old_sessions,
          preserved: ['connections', 'global_settings', 'email_login', 'email_requirement'],
        }
      } finally {
        await context.close().catch(() => {})
      }
    })

    await check(cell, 'cli_revoke_unused_authorization', () => ({
      unused_revoke_passed: recoveryEvidence.unused_authorization_revoke === 'passed',
      consumed_replay_rejected: recoveryEvidence.consumed_authorization_replay === 'rejected',
      consumed_revoke_rejected: recoveryEvidence.consumed_authorization_revoke === 'rejected',
    }))

    for (const item of Object.values(cell.checks)) {
      if (item.status === 'unverified')
        item.reason = 'Pending the maintenance runner increment; not yet wired into runCell.'
    }
  } catch (error) {
    const redact = (value) => {
      let output = String(value)
      for (const secret of runtime.secrets)
        if (secret) output = output.replaceAll(secret, '[redacted]')
      return output.slice(0, 1800)
    }
    cell.diagnostic = {
      route: new URL(page.url()).pathname,
      page_errors: pageErrors.map(redact),
      dialog_alerts: (await page.locator('[role="dialog"] .n-alert').allInnerTexts()).map(redact),
    }
    const directory = path.join(runDir, 'diagnostic')
    await mkdir(directory, { recursive: true })
    await page
      .screenshot({
        path: path.join(directory, `${cell.name}.png`),
        mask: [page.locator('input'), page.locator('textarea')],
        animations: 'disabled',
      })
      .catch(() => {})
    await saveReport()
    throw error
  } finally {
    await context.close().catch(() => {})
  }
}

async function main() {
  runDir = await createB5RunDirectory()
  await saveReport()
  runtime = await createB5Runtime({ runDir })
  report.source_versions = {
    frontend_revision: runtime.sourceManifest.frontend_revision,
    backend_revision: runtime.sourceManifest.backend_revision,
    frontend_dist_sha256: runtime.sourceManifest.frontend_dist_sha256,
    openapi_bundle_sha256: runtime.sourceManifest.openapi_bundle_sha256,
    manifest: 'source-manifest.json',
  }
  report.runtime.database_isolation = 'verified_by_per_cell_runtime'
  report.runtime.identity_isolation = 'verified_by_per_cell_subject_prefixes'
  report.runtime.status = 'ready'
  await saveReport()
  let passed = true
  try {
    browser = await chromium.launch({ executablePath: chromiumPath, headless: true })
    report.browser = { kind: browserKind, engine: 'chromium', headless: true }
    await saveReport()
    for (const name of runtime.cellNames) {
      const app = await runtime.startCell(name)
      const cell = report.matrix.find((item) => item.name === name)
      cell.network = app.network
      await saveReport()
      try {
        await runCell(app, cell)
      } catch (error) {
        passed = false
        cell.status = 'failed'
        cell.failure = String(error?.message ?? error).slice(0, 1200)
        markUnverifiedAfterFailure(cell, 'A prior cell workflow failed before this check.')
      } finally {
        cell.status = cell.status === 'failed' ? 'failed' : cellStatus(cell)
        await saveReport()
      }
    }
  } catch (error) {
    passed = false
    report.status = 'failed'
    report.error = String(error?.message ?? error).slice(0, 1200)
    await saveReport()
  } finally {
    await browser?.close().catch(() => {})
    await runtime?.close().catch(() => {})
    const complete = passed && report.matrix.every(cellComplete)
    report.status = complete ? 'passed' : passed ? 'partial' : 'failed'
    await saveReport()
  }
  return report.status === 'passed'
}

process.exitCode = (await main()) ? 0 : 1
