import assert from 'node:assert/strict'
import { mkdir, writeFile, rm } from 'node:fs/promises'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'

async function dimensions(page) {
  await page.evaluate(() => document.fonts.ready)
  return page.evaluate(() => ({
    width: innerWidth,
    height: innerHeight,
    device_pixel_ratio: devicePixelRatio,
    document_width: document.documentElement.scrollWidth,
    body_width: document.body.scrollWidth,
  }))
}

async function assertLayout(page, scene) {
  const layout = await dimensions(page)
  assert.ok(layout.document_width <= layout.width + 1, `${scene}: document overflows horizontally`)
  assert.ok(layout.body_width <= layout.width + 1, `${scene}: body overflows horizontally`)
  return { scene, ...layout }
}

async function screenshot(page, directory, name, { zoom = false } = {}) {
  const filename = `${name}.png`
  if (zoom) {
    const session = await page.context().newCDPSession(page)
    try {
      const { data } = await session.send('Page.captureScreenshot', {
        format: 'png',
        fromSurface: true,
        captureBeyondViewport: false,
      })
      await writeFile(path.join(directory, filename), Buffer.from(data, 'base64'))
    } finally {
      await session.detach()
    }
  } else {
    await page.screenshot({ path: path.join(directory, filename), animations: 'disabled' })
  }
  return filename
}

async function openPolicy(page, app) {
  await page.goto(`${app.origin}/admin/authentication`)
  const edit = page.getByRole('button', { name: '编辑政策', exact: true })
  await expect(edit).toBeVisible({ timeout: 30_000 })
  await edit.click()
  const editor = page.getByTestId('auth-local-account-editor')
  await expect(editor).toBeVisible({ timeout: 20_000 })
  await expect(editor.getByTestId('auth-local-account-save')).toBeDisabled()
  return editor
}

async function keyboardPolicy(page, app) {
  await page.goto(`${app.origin}/admin/authentication`)
  const edit = page.getByRole('button', { name: '编辑政策', exact: true })
  await expect(edit).toBeVisible({ timeout: 30_000 })
  await edit.focus()
  await page.keyboard.press('Enter')
  const editor = page.getByTestId('auth-local-account-editor')
  await expect(editor).toBeVisible({ timeout: 20_000 })
  const enrollment = editor.getByTestId('auth-local-account-enrollment')
  const initial = await enrollment.getAttribute('aria-checked')
  assert.ok(['true', 'false'].includes(initial), 'Enrollment switch is not keyboard accessible')
  await enrollment.focus()
  await page.keyboard.press('Space')
  await expect(enrollment).toHaveAttribute('aria-checked', initial === 'true' ? 'false' : 'true')
  await page.keyboard.press('Space')
  await expect(enrollment).toHaveAttribute('aria-checked', initial)
  await expect(editor.getByTestId('auth-local-account-save')).toBeDisabled()
  await page.keyboard.press('Tab')
  const focus = await page.evaluate(() => {
    const element = document.activeElement
    const bounds = element?.getBoundingClientRect()
    return {
      within_drawer: Boolean(element?.closest('[data-testid="auth-local-account-editor"]')),
      visible: Boolean(bounds?.width && bounds?.height),
      role: element?.getAttribute('role') ?? element?.tagName.toLowerCase(),
    }
  })
  assert.ok(focus.within_drawer && focus.visible, 'Keyboard focus left the policy editor')
  return {
    enter_opens_policy: true,
    space_toggles_and_restores_policy: true,
    restored_draft_cannot_save: true,
    tab_focus: focus,
  }
}

async function setZoom(worker, origin, factor) {
  return worker.evaluate(
    async ({ origin, factor }) => {
      const [tab] = await chrome.tabs.query({ url: `${origin}/*` })
      if (!tab) throw new Error('No B5 product tab is available for browser zoom')
      await chrome.tabs.setZoom(tab.id, factor)
      return chrome.tabs.getZoom(tab.id)
    },
    { origin, factor },
  )
}

export async function verifyB5ProductVisuals({
  browser,
  app,
  runDir,
  adminStorageState,
  chromiumPath,
}) {
  const resolvedRun = path.resolve(runDir)
  assert.ok(
    path.basename(resolvedRun).startsWith('run-'),
    'B5 visual evidence requires an isolated run',
  )
  assert.ok(/^(sqlite|postgresql)-(oidc|oauth)$/.test(app.name), 'Unknown B5 deployment cell')
  const directory = path.join(resolvedRun, 'visual', app.name)
  await mkdir(directory, { recursive: true })
  const output = {}
  const mobile = await browser.newContext({
    storageState: adminStorageState,
    viewport: { width: 390, height: 844 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    reducedMotion: 'reduce',
  })
  try {
    const page = await mobile.newPage()
    await page.goto(`${app.origin}/admin/authentication`)
    await expect(page.getByRole('button', { name: '编辑政策', exact: true })).toBeVisible({
      timeout: 30_000,
    })
    const summary = await assertLayout(page, 'admin-summary-390px')
    summary.screenshot = await screenshot(page, directory, '390-admin-summary')
    await openPolicy(page, app)
    const drawer = await assertLayout(page, 'local-policy-drawer-390px')
    drawer.screenshot = await screenshot(page, directory, '390-local-policy')
    output.viewport_390px = { summary, drawer }
    const keyboard = await keyboardPolicy(page, app)
    keyboard.screenshot = await screenshot(page, directory, '390-keyboard-policy')
    output.keyboard_operation = keyboard
  } finally {
    await mobile.close()
  }

  const extension = path.join(directory, 'zoom-extension')
  await mkdir(extension, { recursive: false })
  await writeFile(
    path.join(extension, 'manifest.json'),
    JSON.stringify({
      manifest_version: 3,
      name: 'B5 product browser zoom verification',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
    'utf8',
  )
  await writeFile(
    path.join(extension, 'worker.js'),
    'chrome.runtime.onInstalled.addListener(() => {});',
    'utf8',
  )
  const context = await chromium.launchPersistentContext('', {
    ...(chromiumPath ? { executablePath: chromiumPath } : { channel: 'chromium' }),
    headless: true,
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    reducedMotion: 'reduce',
    args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
  })
  try {
    await context.addCookies(adminStorageState.cookies)
    const state = adminStorageState.origins.find((entry) => entry.origin === app.origin)
    assert.ok(state, 'The real administrator storage state is missing')
    await context.addInitScript((entries) => {
      for (const { name, value } of entries) localStorage.setItem(name, value)
    }, state.localStorage)
    const worker =
      context.serviceWorkers()[0] ??
      (await context.waitForEvent('serviceworker', { timeout: 20_000 }))
    const page = await context.newPage()
    await page.goto(`${app.origin}/admin/authentication`)
    await expect(page.getByRole('button', { name: '编辑政策', exact: true })).toBeVisible({
      timeout: 30_000,
    })
    assert.equal(await setZoom(worker, app.origin, 1), 1)
    await page.waitForFunction(() => innerWidth === 1440 && devicePixelRatio === 1)
    const before = await dimensions(page)
    const factor = await setZoom(worker, app.origin, 2)
    assert.equal(factor, 2)
    await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
    const summary = await assertLayout(page, 'admin-summary-real-200-percent')
    summary.screenshot = await screenshot(page, directory, '200pct-admin-summary', { zoom: true })
    await openPolicy(page, app)
    const drawer = await assertLayout(page, 'local-policy-drawer-real-200-percent')
    drawer.screenshot = await screenshot(page, directory, '200pct-local-policy', { zoom: true })
    output.real_browser_zoom_200_percent = {
      method: 'chrome.tabs.setZoom',
      factor,
      before,
      summary,
      drawer,
    }
  } finally {
    await context.close()
    const resolvedExtension = path.resolve(extension)
    assert.ok(resolvedExtension.startsWith(`${directory}${path.sep}`))
    await rm(resolvedExtension, { recursive: true, force: true })
  }
  await writeFile(
    path.join(directory, 'layout-evidence.json'),
    JSON.stringify(output, null, 2),
    'utf8',
  )
  return output
}
