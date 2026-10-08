import { chromium, expect } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { mockStorageVisual } from './storage-visual-fixtures.ts'

const output = fileURLToPath(new URL('../artifacts/storage-quota-visual/', import.meta.url))
const baseURL = 'http://127.0.0.1:4173'
const GiB = 1024 ** 3
const report = []
const failures = []
await mkdir(output, { recursive: true })
const browser = await chromium.launch()
const contextOptions = {
  deviceScaleFactor: 1,
  locale: 'zh-CN',
  timezoneId: 'Asia/Shanghai',
  reducedMotion: 'reduce',
}

function largeLedger(state) {
  Object.assign(state.spaces[0], {
    name: '团队文档与多语言交付 · TranslationArchive20261007',
    capacity_bytes: 200 * GiB,
    available_bytes: 72 * GiB,
    reserved_bytes: 10 * GiB,
    candidate_bytes: 8 * GiB,
    live_bytes: 100 * GiB,
    pending_delete_bytes: 10 * GiB,
  })
}
function diskStates(state) {
  state.disks = [
    {
      roles: ['work', 'objects'],
      state: 'unknown',
      total_bytes: null,
      available_bytes: null,
      minimum_free_bytes: null,
      observed_at: '2026-10-05T01:59:00Z',
    },
    {
      roles: ['metadata_or_cache'],
      state: 'low',
      total_bytes: 512 * GiB,
      available_bytes: 0,
      minimum_free_bytes: 5 * GiB,
      observed_at: '2026-10-05T01:59:00Z',
    },
  ]
}
async function openQuota(page) {
  await page.goto(`${baseURL}/settings/storage`)
  await page
    .locator('[data-connection-id="1"]')
    .getByRole('button', { name: '查看详情', exact: true })
    .click()
  await page
    .locator('.n-drawer:visible [data-storage-space-id="11"]')
    .getByRole('button', { name: '调整配额', exact: true })
    .click()
  const modal = page
    .locator('.n-modal:visible')
    .filter({ has: page.locator('[data-storage-quota-dialog]') })
  await expect(modal).toBeVisible()
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('100')
  await expect(modal).toContainText('拟保存后将超额 28 GiB（30,064,771,072 B）。')
  return modal
}
async function focusEvidence(page, locator, label) {
  if (!(await locator.count()) || !(await locator.isEnabled()))
    return { label, skipped: 'disabled-or-absent' }
  await locator.scrollIntoViewIfNeeded()
  await locator.focus()
  await page.waitForTimeout(80)
  return locator.evaluate((element, name) => {
    const r = element.getBoundingClientRect()
    const x = Math.min(innerWidth - 1, Math.max(1, r.left + r.width / 2))
    const y = Math.min(innerHeight - 1, Math.max(1, r.top + r.height / 2))
    const top = document.elementFromPoint(x, y)
    return {
      label: name,
      focused: document.activeElement === element || element.contains(document.activeElement),
      withinViewport:
        r.left >= -1 && r.top >= -1 && r.right <= innerWidth + 1 && r.bottom <= innerHeight + 1,
      unobscured: !!top && (top === element || element.contains(top)),
      rect: { x: r.x, y: r.y, width: r.width, height: r.height },
    }
  }, label)
}
async function capture(page, name, scene, evidence = {}, focus = []) {
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(350)
  const root = scene.startsWith('quota')
    ? '.n-modal'
    : scene === 'disks'
      ? '[data-testid="storage-disk-diagnostics"]'
      : 'main'
  const focusChecks = []
  for (const [locator, label] of focus) focusChecks.push(await focusEvidence(page, locator, label))
  if (focus.length) {
    await focus[0][0].scrollIntoViewIfNeeded()
    await focus[0][0].focus()
  } else if (scene === 'disks')
    await page.getByTestId('storage-disk-diagnostics').scrollIntoViewIfNeeded()
  else await page.evaluate(() => window.scrollTo(0, 0))
  // Full-page screenshots must begin at scroll zero, otherwise a sticky header
  // is painted halfway down the stitched image after the focus checks above.
  if (!evidence.browserZoom && !scene.startsWith('quota'))
    await page.evaluate(() => window.scrollTo(0, 0))
  await page.waitForTimeout(100)
  if (evidence.browserZoom) {
    const cdp = await page.context().newCDPSession(page)
    try {
      const { data } = await cdp.send('Page.captureScreenshot', {
        format: 'png',
        fromSurface: true,
        captureBeyondViewport: false,
      })
      await writeFile(`${output}/${name}.png`, Buffer.from(data, 'base64'))
    } finally {
      await cdp.detach()
    }
  } else {
    await page.screenshot({
      path: `${output}/${name}.png`,
      fullPage: !scene.startsWith('quota'),
      animations: 'disabled',
    })
  }
  const measures = await page.evaluate((selector) => {
    const roots = [...document.querySelectorAll(selector)].filter(
      (node) => node.getClientRects().length,
    )
    const root = roots.at(-1)
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    const painter = canvas.getContext('2d', { willReadFrequently: true })
    const rgba = (color) => {
      painter.clearRect(0, 0, 1, 1)
      painter.fillStyle = color
      painter.fillRect(0, 0, 1, 1)
      return [...painter.getImageData(0, 0, 1, 1).data]
    }
    const mix = (fg, bg) =>
      fg.slice(0, 3).map((n, i) => (n * fg[3]) / 255 + bg[i] * (1 - fg[3] / 255))
    const luminance = (rgb) =>
      rgb
        .map((n) => n / 255)
        .map((n) => (n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4))
        .reduce((sum, n, i) => sum + n * [0.2126, 0.7152, 0.0722][i], 0)
    const lowContrast = []
    let textSamples = 0
    for (const element of root?.querySelectorAll('*') ?? []) {
      if (
        !element.getClientRects().length ||
        ![...element.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim()) ||
        element.closest(
          '[disabled], [aria-disabled="true"], .n-button--disabled, .n-radio--disabled',
        )
      )
        continue
      const style = getComputedStyle(element)
      if (style.visibility !== 'visible') continue
      let background = [255, 255, 255]
      const ancestors = []
      for (let node = element; node; node = node.parentElement) ancestors.unshift(node)
      for (const node of ancestors)
        background = mix(rgba(getComputedStyle(node).backgroundColor), background)
      const foreground = mix(rgba(style.color), background)
      const a = luminance(foreground),
        b = luminance(background)
      const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
      const large =
        parseFloat(style.fontSize) >= 24 ||
        (parseFloat(style.fontSize) >= 18.66 && Number(style.fontWeight) >= 700)
      const required = large ? 3 : 4.5
      textSamples++
      if (ratio < required)
        lowContrast.push({
          text: element.textContent.trim().slice(0, 80),
          ratio: Number(ratio.toFixed(2)),
          required,
          disabledControl: !!element.closest('.n-base-selection--disabled'),
          color: style.color,
          background,
        })
    }
    const overflowingElements = [...(root?.querySelectorAll('*') ?? [])]
      .filter((element) => {
        if (!element.getClientRects().length || element.closest('thead')) return false
        const r = element.getBoundingClientRect()
        const s = getComputedStyle(element)
        return (
          s.visibility === 'visible' && r.width > 1 && (r.left < -1 || r.right > innerWidth + 1)
        )
      })
      .slice(0, 10)
      .map((element) => ({
        tag: element.tagName,
        text: element.textContent.trim().slice(0, 50),
        class: element.className,
      }))
    return {
      viewport: { width: innerWidth, height: innerHeight },
      scrollWidth: document.documentElement.scrollWidth,
      scrollHeight: document.documentElement.scrollHeight,
      devicePixelRatio,
      textSamples,
      lowContrast,
      overflowingElements,
    }
  }, root)
  const entry = {
    name: `${name}.png`,
    scene,
    ...evidence,
    ...measures,
    overflow: measures.scrollWidth > measures.viewport.width,
    focusChecks,
    contrastMethod:
      'Computed text/background contrast; disabled controls reported separately, not treated as WCAG text failures.',
    manualReview: 'pending',
  }
  report.push(entry)
  if (
    entry.overflow ||
    focusChecks.some(
      (item) => !item.skipped && (!item.focused || !item.withinViewport || !item.unobscured),
    )
  )
    failures.push(name)
  await writeFile(
    `${output}/report.json`,
    JSON.stringify(
      { generatedAt: new Date().toISOString(), baseURL, screenshots: report, failures },
      null,
      2,
    ),
  )
  console.log(
    `${name}: overflow=${entry.overflow}, lowContrast=${entry.lowContrast.length}, focus=${JSON.stringify(focusChecks.map(({ label, focused, withinViewport, unobscured }) => ({ label, focused, withinViewport, unobscured })))}`,
  )
}
async function scenes(page, state, prefix, evidence = {}, extra = false) {
  for (const space of state.spaces) {
    space.capacity_bytes = null
    space.available_bytes = null
  }
  await page.goto(`${baseURL}/settings/storage`)
  await page.locator('[data-storage-space-id="11"]').getByText('不限额', { exact: true }).waitFor()
  await expect(
    page
      .getByRole('navigation', { name: '设置', exact: true })
      .getByRole('link', { name: '文件存储', exact: true }),
  ).toBeInViewport({ ratio: 1 })
  if (!evidence.browserZoom)
    await capture(page, `${prefix}-personal-unlimited`, 'personal', evidence)
  largeLedger(state)
  const modal = await openQuota(page)
  const quotaInput = modal.getByRole('textbox', { name: '空间配额', exact: true })
  await capture(page, `${prefix}-quota-lower`, 'quota-lower', evidence, [
    [quotaInput, 'quota-input'],
    [modal.getByRole('button', { name: '保存配额', exact: true }), 'save-quota'],
  ])
  if (extra) {
    state.spaces[0].management_generation++
    await modal.getByRole('button', { name: '保存配额', exact: true }).click()
    const review = modal.getByRole('button', { name: '保留草稿并采用当前基线', exact: true })
    await expect(review).toBeVisible()
    await capture(page, `${prefix}-quota-conflict`, 'quota-conflict', evidence, [
      [review, 'review-baseline'],
    ])
    await review.click()
    state.quotaResponse = 'lost'
    await modal.getByRole('button', { name: '保存配额', exact: true }).click()
    const acknowledge = modal.getByRole('button', { name: '已核对，采用当前基线', exact: true })
    await expect(acknowledge).toBeEnabled()
    await capture(page, `${prefix}-quota-unknown`, 'quota-unknown', evidence, [
      [acknowledge, 'acknowledge-unknown'],
      [modal.getByRole('button', { name: '取消', exact: true }), 'cancel-unknown'],
    ])
    await capture(page, `${prefix}-quota-unknown-header`, 'quota-unknown', evidence, [
      [modal.locator('.n-card-header__close'), 'close-unknown'],
    ])
  }
  // Reload navigation is deliberately accepted only in this isolated evidence fixture.
  page.once('dialog', (dialog) => dialog.accept())
  await page.goto(`${baseURL}/admin/storage`)
  diskStates(state)
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await page
    .getByTestId('storage-disk-diagnostics')
    .getByText('余量未知', { exact: true })
    .waitFor()
  await capture(page, `${prefix}-disks-unknown-low`, 'disks', evidence)
  state.policy = {
    ...state.policy,
    logical_limit_bytes: null,
    default_space_capacity_bytes: 200 * GiB,
  }
  await page.getByRole('tab', { name: '存储策略', exact: true }).click()
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  const policyInput = page.getByRole('textbox', { name: '新站点空间默认配额', exact: true })
  await expect(policyInput).toHaveValue('200')
  await policyInput.fill('250')
  await capture(page, `${prefix}-policy-dual`, 'policy', evidence, [
    [policyInput, 'default-space-quota'],
    [page.getByRole('button', { name: '保存策略', exact: true }), 'save-policy'],
  ])
}
try {
  for (const [width, height] of [
    [1440, 960],
    [390, 844],
    [320, 844],
  ]) {
    for (const theme of ['light', 'dark']) {
      const context = await browser.newContext({ ...contextOptions, viewport: { width, height } })
      try {
        const page = await context.newPage()
        const state = await mockStorageVisual(page, { theme })
        await scenes(
          page,
          state,
          `${width}-${theme}`,
          { theme, configuredViewport: { width, height } },
          width === 390,
        )
      } finally {
        await context.close()
      }
    }
  }
  const extension = `${output}/zoom-extension`
  await mkdir(extension, { recursive: true })
  await writeFile(
    `${extension}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'Quota zoom evidence',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
  )
  await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
  for (const theme of ['light', 'dark']) {
    const context = await chromium.launchPersistentContext('', {
      ...contextOptions,
      channel: 'chromium',
      headless: true,
      viewport: { width: 1440, height: 960 },
      args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
    })
    try {
      const worker =
        context.serviceWorkers()[0] ??
        (await context.waitForEvent('serviceworker', { timeout: 10000 }))
      const page = await context.newPage()
      const state = await mockStorageVisual(page, { theme })
      await page.goto(`${baseURL}/settings/storage`)
      const unzoomed = await page.evaluate(() => ({ innerWidth, innerHeight, devicePixelRatio }))
      const factor = await worker.evaluate(async () => {
        const [tab] = await globalThis.chrome.tabs.query({ url: 'http://127.0.0.1:4173/*' })
        await globalThis.chrome.tabs.setZoom(tab.id, 2)
        return globalThis.chrome.tabs.getZoom(tab.id)
      })
      await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
      const zoomed = await page.evaluate(() => ({ innerWidth, innerHeight, devicePixelRatio }))
      if (factor !== 2 || unzoomed.innerWidth !== 1440 || unzoomed.devicePixelRatio !== 1)
        throw new Error('Real browser zoom was not established')
      await scenes(page, state, `1440-${theme}-real-200pct`, {
        theme,
        browserZoom: factor,
        method: 'chrome.tabs.setZoom',
        unzoomed,
        zoomed,
      })
    } finally {
      await context.close()
    }
  }
} finally {
  await writeFile(
    `${output}/report.json`,
    JSON.stringify(
      { generatedAt: new Date().toISOString(), baseURL, screenshots: report, failures },
      null,
      2,
    ),
  )
  await browser.close()
}
if (failures.length) throw new Error(`Review overflow or focus failures: ${failures.join(', ')}`)
