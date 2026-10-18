import { chromium, expect, test } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import {
  b4ConnectionCalls,
  mockB4Connection,
  oidcConnection,
  type B4ConnectionHandler,
} from './auth-admin-b4-fixtures'
const baseRecord = () => oidcConnection()

test('admin connection test uses a real popup and cancellation keeps saved identity', async ({
  page,
}) => {
  const calls = b4ConnectionCalls()
  let releaseStart: () => void = () => {}
  const record = baseRecord()
  const handler: B4ConnectionHandler = {
    start(body) {
      expect(body.expected_revision).toBe(7)
      expect(body.changes).toEqual({
        oidc: {
          issuer: 'https://candidate.example',
          client_id: 'app-client',
          client_auth_method: 'client_secret_basic',
          scopes: ['openid', 'profile'],
          userinfo_enabled: true,
        },
      })
      expect(body.completion_url).toBe('http://127.0.0.1:4173/auth/complete')
      expect(body.return_to).toBe('/admin/authentication')
      return new Promise((resolve) => {
        releaseStart = () =>
          resolve({
            transaction_id: 'transaction-e2e',
            authorization_url: 'https://identity.example.com/authorize',
            expires_at: new Date(Date.now() + 120_000).toISOString(),
          })
      })
    },
  }
  await mockB4Connection(page, () => record, calls, handler)
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  await drawer.getByLabel('Issuer 地址').fill('https://candidate.example')
  await drawer.getByLabel('Client ID').fill('app-client')
  const popupPromise = page.context().waitForEvent('page')
  await drawer.getByRole('button', { name: '真实测试', exact: true }).click()
  await expect.poll(() => calls.start).toBe(1)
  const popup = await popupPromise
  await expect(popup).toHaveURL('about:blank')
  await expect(popup.locator('body')).toBeAttached()
  expect(await popup.evaluate(() => window.opener)).toBeNull()

  releaseStart()
  await drawer.getByRole('button', { name: '关闭测试窗口' }).click()

  await expect.poll(() => calls.cancel).toBe(1)
  await expect(drawer.getByLabel('Issuer 地址')).toHaveValue('https://candidate.example')
  await expect(drawer.getByText('https://frontend.example/auth/callback/company')).toBeVisible()
  expect(calls.exchange).toBe(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
})

test('captures keyboard and real 200% zoom evidence on auth config', async ({ page }, testInfo) => {
  test.setTimeout(120_000)
  const calls = b4ConnectionCalls()
  const record = oidcConnection()

  if (testInfo.project.name === 'mobile') {
    await mockB4Connection(page, record, calls)
    await page.goto('/admin/authentication')
    await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
    const drawer = page.locator('.n-drawer-content')
    const issuer = drawer.getByTestId('auth-connection-issuer')
    await expect(issuer).toHaveValue('https://identity.example.com')
    await issuer.focus()
    await issuer.press('Control+A')
    await page.keyboard.type('https://candidate.example')
    await expect(issuer).toHaveValue('https://candidate.example')
    await issuer.scrollIntoViewIfNeeded()
    await page.screenshot({
      path: `tests/artifacts/b4-e2e-final/auth-config-keyboard-mobile-${testInfo.project.name}.png`,
      fullPage: true,
    })
    return
  }

  const extensionDir = process.cwd() + '/tests/artifacts/b4-e2e-final/zoom-extension'
  await mkdir(extensionDir, { recursive: true })
  await writeFile(
    `${extensionDir}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'Auth connection zoom evidence',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
  )
  await writeFile(`${extensionDir}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')

  const context = await chromium.launchPersistentContext('', {
    channel: 'chromium',
    executablePath: process.env.LINGUAFLOW_TEST_CHROMIUM,
    headless: true,
    viewport: { width: 1280, height: 720 },
    deviceScaleFactor: 1,
    args: [`--disable-extensions-except=${extensionDir}`, `--load-extension=${extensionDir}`],
  })
  try {
    const worker =
      context.serviceWorkers()[0] ??
      (await context.waitForEvent('serviceworker', { timeout: 10000 }))
    const evidencePage = await context.newPage()
    await mockB4Connection(evidencePage, record, calls)
    await evidencePage.goto('/admin/authentication')
    await evidencePage
      .getByTestId('auth-connection-company')
      .getByRole('button', { name: '编辑' })
      .click()
    const drawer = evidencePage.locator('.n-drawer-content')
    const issuer = drawer.getByTestId('auth-connection-issuer')
    await expect(issuer).toHaveValue('https://identity.example.com')
    await issuer.focus()
    await issuer.press('Control+A')
    await evidencePage.keyboard.type('https://candidate.example')
    await expect(issuer).toHaveValue('https://candidate.example')
    await issuer.scrollIntoViewIfNeeded()
    const cdp = await evidencePage.context().newCDPSession(evidencePage)
    const keyboardShot = await cdp.send('Page.captureScreenshot', {
      format: 'png',
      fromSurface: true,
    })
    await writeFile(
      `tests/artifacts/b4-e2e-final/auth-config-keyboard-desktop-${testInfo.project.name}.png`,
      Buffer.from(keyboardShot.data, 'base64'),
    )

    const factor = await worker.evaluate(async () => {
      const [tab] = await globalThis.chrome.tabs.query({ url: 'http://127.0.0.1:4173/*' })
      await globalThis.chrome.tabs.setZoom(tab.id, 2)
      return globalThis.chrome.tabs.getZoom(tab.id)
    })
    await evidencePage.waitForFunction(() => window.innerWidth <= 640, undefined, {
      timeout: 10000,
    })
    await issuer.scrollIntoViewIfNeeded()
    await issuer.focus()
    const zoomShot = await cdp.send('Page.captureScreenshot', { format: 'png', fromSurface: true })
    await writeFile(
      `tests/artifacts/b4-e2e-final/auth-config-200pct-desktop-${testInfo.project.name}.png`,
      Buffer.from(zoomShot.data, 'base64'),
    )

    const zoomed = await evidencePage.evaluate(() => ({
      innerWidth,
      devicePixelRatio,
      visualViewportScale: visualViewport?.scale ?? null,
      scrollWidth: document.documentElement.scrollWidth,
    }))
    expect(factor).toBe(2)
    expect(zoomed.innerWidth).toBeLessThanOrEqual(640)
    expect(zoomed.scrollWidth).toBeLessThanOrEqual(zoomed.innerWidth)
  } finally {
    await context.close()
  }
})
