import assert from 'node:assert/strict'
import { access, mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'
import { b4ArtifactsDir, createB4Runtime } from './authentication-b4-runtime.mjs'
import { oauthUserRegression } from './authentication-b4-user-regression.mjs'

const frontendDir = path.resolve(b4ArtifactsDir, '../../..')
const chromiumPath =
  process.env.LINGUAFLOW_TEST_CHROMIUM ||
  path.join(
    process.env.LOCALAPPDATA ?? '',
    'ms-playwright',
    'chromium-1248',
    'chrome-win64',
    'chrome.exe',
  )
const browserProtocol =
  process.env.LINGUAFLOW_B4_PROTOCOL ||
  (process.platform === 'win32' && process.env.LINGUAFLOW_TEST_ENABLE_EDGE === '1'
    ? 'edge'
    : 'chromium')
if (browserProtocol !== 'chromium' && browserProtocol !== 'edge')
  throw new Error(`Unsupported browser protocol: ${browserProtocol}`)

let browser
const metadata = {
  objective: 'B4 frontend product authentication acceptance',
  tested_at: new Date().toISOString(),
  application_source: 'frontend/dist production build',
  no_fixtures_used: true,
  browser: null,
  matrix: [],
  errors: [],
  limitations: [
    'Provider identities are isolated local acceptance identities, not production credentials.',
    'The backend service endpoints are real processes and the management page is the built Vue application.',
  ],
}

async function accessToken(page) {
  return page.evaluate(() => {
    const token = localStorage.getItem('linguaflow.access_token')
    if (!token) throw new Error('admin access token is unavailable for read-back')
    return token
  })
}

async function adminRead(page, app, id) {
  const token = await accessToken(page)
  const response = await page.request.fetch(`${app.apiBase}/admin/auth-connections/${id}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  assert.equal(
    response.status(),
    200,
    `admin connection read-back failed with ${response.status()}`,
  )
  return await response.json()
}

async function adminListForName(page, app, displayName) {
  const token = await accessToken(page)
  const response = await page.request.fetch(`${app.apiBase}/admin/auth-connections`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  assert.equal(
    response.status(),
    200,
    `admin connection list read-back failed with ${response.status()}`,
  )
  const payload = await response.json()
  const records = Array.isArray(payload) ? payload : (payload?.items ?? [])
  const record = records.find((item) => item?.configuration?.display_name === displayName)
  assert.ok(
    record,
    `created connection was not visible through the read-only admin API: ${displayName}`,
  )
  return record
}

async function login(page, app, identifier, password) {
  await page.goto(`${app.origin}/login`)
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible({
    timeout: 30_000,
  })
  await page.locator('input[autocomplete="username"]').fill(identifier)
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(`${app.origin}/`, { timeout: 30_000 })
}

async function clickTestId(page, testId, timeout = 20_000) {
  const control = page.getByTestId(testId)
  await expect(control).toBeVisible({ timeout })
  await expect(control).toBeEnabled({ timeout })
  await control.click()
}

async function fillTestId(page, testId, value) {
  const control = page.getByTestId(testId)
  await expect(control).toBeVisible({ timeout: 20_000 })
  await control.fill(value)
}

async function expectToast(page, text) {
  await expect(page.getByText(text, { exact: true }).first()).toBeVisible({ timeout: 20_000 })
}

async function assertSensitiveNotStored(page, forbidden) {
  const dump = await page.evaluate(() => ({
    url: location.href,
    localStorage: Object.fromEntries(Object.entries(localStorage)),
    sessionStorage: Object.fromEntries(Object.entries(sessionStorage)),
  }))
  const serialized = JSON.stringify(dump)
  for (const value of forbidden.filter(Boolean)) {
    assert.ok(
      !serialized.includes(value),
      'sensitive authentication material was exposed in browser URL or storage',
    )
  }
  assert.ok(
    !dump.localStorage['linguaflow.connection-test-verifier'],
    'connection-test verifier must not be persisted under localStorage',
  )
  assert.ok(
    !dump.sessionStorage['linguaflow.connection-test-verifier'],
    'connection-test verifier must not be persisted under sessionStorage',
  )
}

async function authorize(page, providerKind, provider) {
  const popupPromise = page.waitForEvent('popup', { timeout: 30_000 })
  const testApiFailure = page.waitForResponse(
    (response) => {
      const url = new URL(response.url())
      return url.pathname.endsWith('/test') && response.status() >= 400
    },
    { timeout: 0 },
  )
  await clickTestId(page, 'auth-connection-test')
  const firstEvent = await Promise.race([popupPromise, testApiFailure])
  if (typeof firstEvent.status === 'function') {
    let body
    try {
      body = await firstEvent.json()
    } catch {
      body = null
    }
    throw new Error(`real-test API failed with ${firstEvent.status}: ${JSON.stringify(body)}`)
  }
  const popup = firstEvent
  const popupClosed = new Promise((resolve) => {
    popup.once('close', () => resolve(true))
  })
  let providerUrl = ''
  try {
    const hasAuthForm = await Promise.race([
      popup
        .locator('#password')
        .waitFor({ state: 'visible', timeout: 45_000 })
        .then(() => true),
      popupClosed.then(() => false),
    ])
    if (hasAuthForm) {
      if (
        await popup
          .locator('#username')
          .isVisible()
          .catch(() => false)
      )
        await popup.locator('#username').fill('provideradmin')
      await popup.locator('#password').fill(provider.password)
      providerUrl = popup.url()
      if (providerKind === 'oidc') await popup.locator('button[type="submit"]').first().click()
      else await popup.locator('#oauth-approve').click()
    }
    await Promise.race([popupClosed, new Promise((resolve) => setTimeout(resolve, 45_000))])
  } finally {
    if (!popup.isClosed()) await popup.close().catch(() => {})
  }
  return providerUrl
}

async function expectTestResult(page) {
  const resultCard = page
    .getByTestId('auth-connection-editor')
    .locator('.n-card')
    .filter({ hasText: '真实测试结果' })
    .last()
  await expect(
    resultCard.locator('.n-card-header__main').getByText('真实测试结果', { exact: true }),
  ).toBeVisible({ timeout: 45_000 })
  await expect(resultCard.getByText(/(?:未确认)?支持近期认证/u)).toBeVisible({
    timeout: 45_000,
  })
  await expect(resultCard.locator('section.rounded-lg')).toHaveCount(3)
}

async function saveBasicInfo(page, displayName) {
  await fillTestId(page, 'auth-connection-name', displayName)
  await clickTestId(page, 'auth-connection-save-presentation')
  await expectToast(page, '已保存')
}

async function selectSecretReplace(page) {
  await page.getByTestId('auth-connection-secret-mode').click()
  await page
    .locator('.n-base-select-option')
    .filter({ hasText: '替换 Secret' })
    .getByText('替换 Secret', { exact: true })
    .click()
}

async function assertAdminIdentity(page, app) {
  const identity = await page.evaluate(async (apiBase) => {
    const response = await fetch(`${apiBase}/users/me`, {
      headers: { Authorization: `Bearer ${localStorage.getItem('linguaflow.access_token')}` },
    })
    if (!response.ok) throw new Error(`users/me admin identity failed with ${response.status}`)
    return response.json()
  }, app.apiBase)
  assert.equal(identity?.role, 'admin', 'management workflow did not use an authenticated admin')
  assert.ok(identity?.username, 'users/me did not return an administrator username')
}

async function adminIdentity(page, app) {
  return page.evaluate(async (apiBase) => {
    const response = await fetch(`${apiBase}/users/me`, {
      headers: { Authorization: `Bearer ${localStorage.getItem('linguaflow.access_token')}` },
    })
    if (!response.ok) throw new Error(`users/me admin identity failed with ${response.status}`)
    return response.json()
  }, app.apiBase)
}

async function adminMethods(page, app) {
  return page.evaluate(async (apiBase) => {
    const response = await fetch(`${apiBase}/users/me/login-methods`, {
      headers: { Authorization: `Bearer ${localStorage.getItem('linguaflow.access_token')}` },
    })
    if (!response.ok) throw new Error(`admin login-methods failed with ${response.status}`)
    return response.json()
  }, app.apiBase)
}

async function enableAllAuthPurposes(page) {
  for (const purpose of ['login', 'signup', 'link']) {
    const toggle = page.getByTestId(`auth-connection-purpose-${purpose}`)
    await expect(toggle).toBeVisible({ timeout: 20_000 })
    const active = await toggle.evaluate((element) =>
      element.classList.contains('n-switch--active'),
    )
    if (!active) await toggle.click()
    await expect(toggle).toHaveClass(/n-switch--active/u)
  }
}

async function configureProtocol(page, kind, runtime) {
  if (kind === 'oidc') {
    await fillTestId(page, 'auth-connection-issuer', runtime.keycloak.issuer)
    await fillTestId(page, 'auth-connection-client-id', runtime.keycloak.clientID)
    await selectSecretReplace(page)
    await fillTestId(page, 'auth-connection-secret', runtime.keycloak.clientSecret)
    await enableAllAuthPurposes(page)
  } else {
    await fillTestId(
      page,
      'auth-connection-authorization-endpoint',
      `${runtime.oauth.base}/authorize`,
    )
    await fillTestId(page, 'auth-connection-token-endpoint', `${runtime.oauth.base}/token`)
    await fillTestId(page, 'auth-connection-profile-endpoint', `${runtime.oauth.base}/user`)
    await fillTestId(page, 'auth-connection-client-id', 'b3-basic')
    await page.getByTestId('auth-connection-subject-type').click()
    await page
      .locator('.n-base-select-option')
      .filter({ hasText: '整数' })
      .getByText('整数', { exact: true })
      .click()
    await fillTestId(page, 'auth-connection-subject-pointer', '/id')
    await selectSecretReplace(page)
    await fillTestId(page, 'auth-connection-secret', runtime.oauth.clientSecret)

    await enableAllAuthPurposes(page)
  }
  await clickTestId(page, 'auth-connection-save-configuration')
  await expectToast(page, '已保存')
}

async function prepareUserOidc(runtime, app, page, managedName) {
  const userOidcName = `${managedName}-user-oidc`
  await clickTestId(page, 'auth-connection-close')
  await expect(page.getByTestId('auth-connection-editor')).toBeHidden({ timeout: 20_000 })
  await clickTestId(page, 'auth-connections-create')
  await page.getByRole('dialog').locator('input[aria-label="显示名称"]').fill(userOidcName)
  await page.getByRole('dialog').getByRole('button', { name: '创建连接', exact: true }).click()
  await expect(page.getByTestId('auth-connection-editor')).toBeVisible({ timeout: 30_000 })
  await saveBasicInfo(page, userOidcName)
  await configureProtocol(page, 'oidc', runtime)
  await authorize(page, 'oidc', runtime.keycloak)
  await expectTestResult(page)
  await clickTestId(page, 'auth-connection-enable')
  await expectToast(page, '连接已启用。')
  const connection = await adminListForName(page, app, userOidcName)
  const readBack = await adminRead(page, app, connection.id)
  assert.equal(readBack?.capabilities?.can_login, true)
  assert.equal(readBack?.capabilities?.can_signup, true)
  assert.equal(readBack?.capabilities?.can_link, true)
  runtime.userOIDCByDeployment.set(app.kind, {
    displayName: userOidcName,
    provider: runtime.keycloak,
    connection_id: connection.id,
  })
  return runtime.userOIDCByDeployment.get(app.kind)
}

async function disableConnectionById(page, app, connectionId, label) {
  await clickTestId(page, 'auth-connection-close')
  await expect(page.getByTestId('auth-connection-editor')).toBeHidden({ timeout: 20_000 })
  await clickTestId(page, `auth-connection-open-${connectionId}`)
  await expect(page.getByTestId('auth-connection-editor')).toBeVisible({ timeout: 20_000 })
  await clickTestId(page, 'auth-connection-disable')
  await expectToast(page, '连接已停用。')
  const readBack = await adminRead(page, app, connectionId)
  assert.equal(readBack?.state, 'disabled', `${label} did not end explicitly disabled`)
  assert.ok(readBack?.configuration_complete, `${label} ended with incomplete configuration`)
  return readBack
}

async function productScenario(runtime, deploymentKind, protocolKind) {
  const app = await runtime.startDeployment(deploymentKind)
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
  const shortName = `${deploymentKind}-${protocolKind}`
  const cell = {
    deployment: deploymentKind,
    database: deploymentKind === 'postgresql' ? 'postgresql' : 'sqlite',
    protocol: protocolKind,
    network:
      deploymentKind === 'sqlite'
        ? { ui_origin: app.origin, api_base: app.apiBase, same_origin: true }
        : { ui_origin: app.origin, api_base: app.apiBase, gateway: true, same_origin: false },
    status: 'failed',
    ui_workflows: [],
  }
  metadata.matrix.push(cell)

  const failedRequests = []
  let capturedVerifier = ''
  const exchangeEvents = []
  await context.route('**/api/v1/auth/external/exchange', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const event = {
      method: request.method(),
      path: url.pathname,
      body_property_keys: [],
      captured_verifier: false,
    }
    try {
      const parsed = request.postDataJSON()
      if (parsed && typeof parsed === 'object') {
        event.body_property_keys = Object.keys(parsed).sort()
        if (typeof parsed.verifier === 'string') {
          capturedVerifier = parsed.verifier
          event.captured_verifier = true
        }
      }
    } catch {
      /* Payload metadata remains sanitized. */
    }
    exchangeEvents.push(event)
    await route.continue()
  })
  page.on('pageerror', (error) => {
    cell.page_error = error.message
  })
  page.on('requestfailed', (request) => {
    if (!request.url().includes('/api/v1/auth/external/exchange')) {
      failedRequests.push(`${request.method()} ${new URL(request.url()).pathname}`)
    }
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (url.pathname.endsWith('/auth/external/exchange') || response.status() >= 400) {
      cell.http_events = cell.http_events ?? []
      cell.http_events.push({
        method: response.request().method(),
        path: url.pathname,
        status: response.status(),
      })
    }
  })

  try {
    await login(page, app, 'admin', app.adminPassword)
    cell.ui_workflows.push('admin_password_login', 'vue_admin_route')
    await page.goto(`${app.origin}/admin/authentication`)
    await expect(page.getByTestId('auth-connections-refresh')).toBeVisible({ timeout: 30_000 })

    await clickTestId(page, 'auth-connections-create')
    if (protocolKind === 'controlled_oauth2') {
      await page
        .locator('.n-form-item')
        .filter({ hasText: '连接协议' })
        .locator('.n-base-selection')
        .click()
      await page
        .locator('.n-base-select-option')
        .filter({ hasText: '通用 OAuth 2.0' })
        .last()
        .click()
    }
    await page
      .getByRole('dialog')
      .locator('input[aria-label="显示名称"]')
      .fill(`${shortName}-editing`)
    await page.getByRole('dialog').getByRole('button', { name: '创建连接', exact: true }).click()
    await expect(page.getByTestId('auth-connection-editor')).toBeVisible({ timeout: 30_000 })

    const callbackUrl = await page
      .locator('code')
      .filter({ hasText: app.publicBase })
      .first()
      .innerText()
    assert.match(callbackUrl, /\/api\/v1\/auth\/external\/[A-Za-z0-9-]+\/callback$/u)
    cell.ui_workflows.push('ui_connection_create')

    await saveBasicInfo(page, `${shortName}-saved`)
    cell.ui_workflows.push('ui_basic_info_save')
    const created = await adminListForName(page, app, `${shortName}-saved`)
    cell.connection_id = created.id
    await configureProtocol(page, protocolKind, runtime)
    cell.ui_workflows.push('ui_configuration_save')
    await assertAdminIdentity(page, app)

    // Candidate save, first real provider round-trip, then production enable.
    const firstPopupUrl = await authorize(
      page,
      protocolKind,
      protocolKind === 'oidc' ? runtime.keycloak : runtime.oauth,
    )
    const firstSecret =
      protocolKind === 'oidc' ? runtime.keycloak.clientSecret : runtime.oauth.clientSecret
    assert.ok(
      !firstPopupUrl.includes(firstSecret),
      'client secret appeared in the provider popup URL',
    )
    if (capturedVerifier)
      assert.ok(
        !firstPopupUrl.includes(capturedVerifier),
        'connection verifier appeared in the popup URL',
      )
    await assertSensitiveNotStored(page, [firstSecret, capturedVerifier])
    await expectTestResult(page)
    cell.ui_workflows.push('real_popup_provider_authentication')
    await clickTestId(page, 'auth-connection-enable')
    await expectToast(page, '连接已启用。')
    cell.ui_workflows.push('ui_enable')
    const enabledReadBack = await adminRead(page, app, cell.connection_id)
    assert.equal(enabledReadBack.state, 'enabled')
    if (protocolKind === 'controlled_oauth2') {
      assert.equal(enabledReadBack.capabilities.can_login, true)
      assert.equal(enabledReadBack.capabilities.can_signup, true)
      assert.equal(enabledReadBack.capabilities.can_link, true)
    }
    const adminIdentityBefore = await adminIdentity(page, app)
    const adminMethodsBefore = await adminMethods(page, app)

    // Test a deliberately unsaved candidate against the same original service.
    if (protocolKind === 'oidc') {
      await page.getByTestId('auth-connection-userinfo-enabled').click()
    } else {
      const scopesItem = page.locator('.n-form-item').filter({ hasText: 'Scopes' }).first()
      await expect(scopesItem).toBeVisible({ timeout: 20_000 })
      await scopesItem.locator('button').first().click()
      const scopeInput = scopesItem.locator('input').last()
      await scopeInput.fill('profile')
      await scopeInput.press('Enter')
      await expect(
        scopesItem.locator('.n-tag').filter({ hasText: 'profile', exact: true }),
      ).toBeVisible({ timeout: 20_000 })
    }
    const candidatePopupUrl = await authorize(
      page,
      protocolKind,
      protocolKind === 'oidc' ? runtime.keycloak : runtime.oauth,
    )
    assert.ok(
      !candidatePopupUrl.includes(firstSecret),
      'client secret appeared in the candidate-test popup URL',
    )
    await assertSensitiveNotStored(page, [firstSecret, capturedVerifier])
    await expectTestResult(page)
    cell.ui_workflows.push('ui_unsaved_candidate_test_against_original_service')
    const candidateStillSaved = await adminRead(page, app, cell.connection_id)
    assert.equal(candidateStillSaved.state, 'enabled')
    assert.equal(candidateStillSaved.aggregate_revision, enabledReadBack.aggregate_revision)
    assert.deepEqual(candidateStillSaved.configuration, enabledReadBack.configuration)
    const adminIdentityAfter = await adminIdentity(page, app)
    const adminMethodsAfter = await adminMethods(page, app)
    assert.deepEqual(adminIdentityAfter, adminIdentityBefore)
    assert.equal(
      adminMethodsBefore.bindings.length,
      adminMethodsAfter.bindings.length,
      'credential-metadata side effects changed the admin binding count',
    )
    await clickTestId(page, 'auth-connection-save-configuration')
    await expectToast(page, '已保存')
    cell.ui_workflows.push('ui_modified_configuration_save')
    if (protocolKind === 'controlled_oauth2') {
      const userOidc = await prepareUserOidc(runtime, app, page, `${shortName}-saved`)
      cell.user_oidc_connector = {
        display_name: userOidc.displayName,
        state: 'enabled',
        capabilities: { can_login: true, can_signup: true, can_link: true },
      }
      cell.user_regressions = await oauthUserRegression({
        browser,
        app,
        provider: runtime.oauth,
        displayName: `${shortName}-saved`,
        oidc: {
          displayName: userOidc.displayName,
          provider: {
            issuer: runtime.keycloak.issuer,
            password: runtime.keycloak.password,
          },
        },
      })
      await disableConnectionById(
        page,
        app,
        cell.connection_id,
        'managed controlled OAuth connection',
      )
      await disableConnectionById(page, app, userOidc.connection_id, 'user OIDC connector')
    }
    cell.ui_workflows.push('ui_disable')
    const readBack = await adminRead(page, app, cell.connection_id)
    assert.equal(readBack?.state, 'disabled', 'connection did not end explicitly disabled')
    assert.ok(readBack?.configuration_complete, 'final saved configuration was incomplete')
    cell.saved_state = 'disabled'
    cell.ui_workflows.push('api_admin_read_back')
    if (capturedVerifier) {
      cell.sensitive_assertions = {
        verifier_observed_in_exchange_request: true,
        verifier_absent_from_popup_urls_and_storage: true,
      }
    }
    cell.status = 'passed'
  } catch (error) {
    cell.error = error.message
    try {
      const screenshot = path.join(b4ArtifactsDir, `${shortName}-failure.png`)
      const dom = path.join(b4ArtifactsDir, `${shortName}-failure.html`)
      await page.screenshot({ path: screenshot, fullPage: true })
      await writeFile(dom, await page.content(), 'utf8')
      cell.failure_artifacts = [
        `tests/artifacts/authentication-product/${path.basename(screenshot)}`,
        `tests/artifacts/authentication-product/${path.basename(dom)}`,
      ]
    } catch {
      /* Keep the primary failure when best-effort diagnostics fail. */
    }
  } finally {
    if (cell.connection_id) {
      try {
        const readBack = await adminRead(page, app, cell.connection_id)
        cell.api_read_back = {
          kind: readBack.kind,
          state: readBack.state,
          configuration_complete: readBack.configuration_complete,
          expected_configuration_after_modified_save:
            protocolKind === 'oidc'
              ? { userinfo_enabled: false }
              : {
                  client_id: 'b3-basic',
                  client_auth_method: 'client_secret_basic',
                  profile_endpoint: `${runtime.oauth.base}/user`,
                  scopes: ['profile'],
                },
        }
      } catch (error) {
        cell.final_read_back_error = error.message
      }
    }
    if (failedRequests.length) cell.failed_request_paths = [...new Set(failedRequests)].slice(0, 20)
    await context.close().catch(() => {})
  }
}

async function main() {
  const dist = path.join(frontendDir, 'dist/index.html')
  await access(dist).catch(() => {
    throw new Error('frontend/dist is missing; run the current production build before B4')
  })
  const targetPlans = {
    'sqlite-oidc': [['sqlite', 'oidc']],
    'sqlite-controlled': [['sqlite', 'controlled_oauth2']],
    'postgresql-oidc': [['postgresql', 'oidc']],
    'postgresql-controlled': [['postgresql', 'controlled_oauth2']],
    full: [
      ['sqlite', 'oidc'],
      ['sqlite', 'controlled_oauth2'],
      ['postgresql', 'oidc'],
      ['postgresql', 'controlled_oauth2'],
    ],
  }
  const selectedTarget = process.env.LINGUAFLOW_B4_TARGET ?? 'full'
  const targetPlan = targetPlans[selectedTarget]
  if (!targetPlan) throw new Error(`Unknown LINGUAFLOW_B4_TARGET: ${selectedTarget}`)
  metadata.run_scope = selectedTarget
  const runtime = await createB4Runtime()
  try {
    const launchOptions = { headless: true, executablePath: chromiumPath }
    if (browserProtocol === 'edge')
      browser = await chromium.launch({ ...launchOptions, channel: 'msedge' })
    else browser = await chromium.launch(launchOptions)
    metadata.browser = {
      kind: browserProtocol,
      version: await browser.version(),
      expected_chromium_build: 1248,
    }
    for (const [deployment, protocol] of targetPlan) {
      try {
        await productScenario(runtime, deployment, protocol)
      } catch (error) {
        metadata.errors.push(`${deployment}/${protocol}: ${error.message}`)
      }
      await mkdir(b4ArtifactsDir, { recursive: true })
      await writeFile(
        path.join(b4ArtifactsDir, 'report.json'),
        JSON.stringify(metadata, null, 2),
        'utf8',
      )
    }
    metadata.status = metadata.matrix.every((cell) => cell.status === 'passed')
      ? 'passed'
      : 'failed'
    const report = path.join(b4ArtifactsDir, 'report.json')
    await writeFile(report, JSON.stringify(metadata, null, 2), 'utf8')
    console.log(
      `B4 matrix: ${metadata.matrix
        .map((cell) => `${cell.deployment}/${cell.protocol}=${cell.status}`)
        .join(', ')}`,
    )
    console.log(`Sanitized report: ${report}`)
    if (metadata.status !== 'passed') {
      throw new Error(
        `B4 matrix failed: ${metadata.matrix
          .map((cell) => `${cell.deployment}/${cell.protocol}=${cell.status}`)
          .join(', ')}`,
      )
    }
  } catch (error) {
    metadata.status = 'failed'
    metadata.error = error.message
    await mkdir(b4ArtifactsDir, { recursive: true })
    await writeFile(
      path.join(b4ArtifactsDir, 'report.json'),
      JSON.stringify(metadata, null, 2),
      'utf8',
    )
    throw error
  } finally {
    await browser?.close().catch(() => {})
    await runtime.close().catch(() => {})
  }
}

await main()
