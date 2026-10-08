import { chromium } from '@playwright/test'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { mockStorageVisual } from './storage-visual-fixtures.ts'

// Run against an already-built production preview:
// node --experimental-strip-types tests/e2e/capture-storage-visual.mjs before|after
const phase = process.argv[2] ?? 'after'
const zoomOnly = process.argv.includes('--zoom-only')
const output = fileURLToPath(new URL(`../artifacts/storage-redesign/${phase}/`, import.meta.url))
await mkdir(output, { recursive: true })
const browser = await chromium.launch()
const report = zoomOnly
  ? JSON.parse(await readFile(`${output}/report.json`, 'utf8')).filter(
      (entry) => !entry.browserZoom,
    )
  : []
async function capture(page, name, evidence = {}) {
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(450)
  const overlay = await page.locator('.n-drawer:visible, .n-modal:visible').count()
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
      fullPage: !overlay,
      animations: 'disabled',
    })
  }
  const dimensions = await page.evaluate(() => ({
    viewport: innerWidth,
    scroll: document.documentElement.scrollWidth,
  }))
  const contrast = await page.evaluate(() => {
    const canvas = document.createElement('canvas')
    canvas.width = canvas.height = 1
    const painter = canvas.getContext('2d', { willReadFrequently: true })
    const rgba = (color) => {
      painter.clearRect(0, 0, 1, 1)
      painter.fillStyle = color
      painter.fillRect(0, 0, 1, 1)
      return [...painter.getImageData(0, 0, 1, 1).data]
    }
    const mix = (foreground, background) =>
      foreground
        .slice(0, 3)
        .map(
          (value, index) =>
            (value * foreground[3]) / 255 + background[index] * (1 - foreground[3] / 255),
        )
    const luminance = (color) =>
      color
        .map((value) => value / 255)
        .map((value) => (value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4))
        .reduce((total, value, index) => total + value * [0.2126, 0.7152, 0.0722][index], 0)
    const samples = []
    for (const element of document.querySelectorAll(
      '[data-testid="storage-manager"] *, .storage-manager-drawer *, .diagnostic-summary *, .diagnostic-table *, .n-card *, .n-dropdown-menu *, .n-dialog *',
    )) {
      if (
        !element.getClientRects().length ||
        ![...element.childNodes].some((node) => node.nodeType === 3 && node.textContent.trim()) ||
        element.closest('[disabled], [aria-disabled="true"], .n-button--disabled')
      )
        continue
      const style = getComputedStyle(element)
      if (style.visibility !== 'visible') continue
      let background = [255, 255, 255]
      const parents = []
      for (let parent = element; parent; parent = parent.parentElement) parents.unshift(parent)
      for (const parent of parents)
        background = mix(rgba(getComputedStyle(parent).backgroundColor), background)
      const foreground = mix(rgba(style.color), background)
      const first = luminance(foreground)
      const second = luminance(background)
      const ratio = (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05)
      if (ratio < 4.5)
        samples.push({
          text: element.textContent.trim().slice(0, 65),
          ratio: Number(ratio.toFixed(2)),
          color: style.color,
          background,
        })
    }
    return samples
  })
  report.push({
    name,
    ...evidence,
    ...dimensions,
    overflow: dimensions.scroll > dimensions.viewport,
    contrastBelow45: contrast,
  })
  console.log(name, JSON.stringify(dimensions))
}
try {
  for (const [width, height] of zoomOnly
    ? []
    : [
        [1440, 960],
        [390, 844],
      ]) {
    for (const theme of ['light', 'dark']) {
      const context = await browser.newContext({
        viewport: { width, height },
        deviceScaleFactor: 1,
        locale: 'zh-CN',
        timezoneId: 'Asia/Shanghai',
        reducedMotion: 'reduce',
      })
      const page = await context.newPage()
      await mockStorageVisual(page, { theme })
      const prefix = `${width}-${theme}`
      await page.goto('http://127.0.0.1:4173/settings/storage')
      await page.getByRole('button', { name: '查看详情', exact: true }).first().waitFor()
      await capture(page, `${prefix}-personal`)
      await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
      await page.locator('.n-drawer:visible').waitFor()
      await capture(page, `${prefix}-drawer-spaces`)
      if (phase === 'after') {
        const drawer = page.locator('.n-drawer:visible')
        await drawer.getByRole('button', { name: '更多操作', exact: true }).click()
        await capture(page, `${prefix}-drawer-more-menu`)
        if (width === 390) {
          await page.locator('.n-dropdown-option-body').filter({ hasText: '撤销授权' }).click()
          await page.locator('.n-dialog:visible').waitFor()
          await capture(page, `${prefix}-revoke-confirmation`)
          await page
            .locator('.n-dialog:visible')
            .getByRole('button', { name: '取消', exact: true })
            .click()
        } else {
          await drawer.getByRole('button', { name: '更多操作', exact: true }).click()
        }
        await drawer.locator('summary').filter({ hasText: '容量明细' }).first().click()
        await capture(page, `${prefix}-drawer-capacity`)
        await drawer.getByRole('button', { name: '新建空间', exact: true }).click()
        await page.locator('.n-modal:visible').waitFor()
        await capture(page, `${prefix}-form-space`)
        await page
          .locator('.n-modal:visible')
          .getByRole('button', { name: '取消', exact: true })
          .click()
        await drawer.getByRole('tab', { name: '连接与授权', exact: true }).click()
        await capture(page, `${prefix}-drawer-authorization`)
        await drawer.getByRole('button', { name: '更新授权', exact: true }).click()
        await page.locator('.n-modal:visible').waitFor()
        await capture(page, `${prefix}-form-authorization`)
        await page
          .locator('.n-modal:visible')
          .getByRole('button', { name: '取消', exact: true })
          .click()
        await drawer.getByRole('tab', { name: '检测历史', exact: true }).click()
        await drawer.locator('summary').filter({ hasText: '#10' }).click()
        await drawer.getByText('本次未激活授权', { exact: true }).waitFor()
        await capture(page, `${prefix}-drawer-history`)
        await page.goto('http://127.0.0.1:4173/settings/storage')
        await page.getByRole('button', { name: '新建连接', exact: true }).click()
        await capture(page, `${prefix}-form-connection`)
      }
      await page.goto('http://127.0.0.1:4173/admin/storage')
      await page.getByText(phase === 'before' ? '只读诊断' : '诊断摘要', { exact: true }).waitFor()
      await capture(page, `${prefix}-admin-overview`)
      if (phase === 'after') {
        await page.getByRole('button', { name: '展开空间 #11 的占用分项', exact: true }).click()
        await capture(page, `${prefix}-admin-expanded`)
        await page.getByRole('tab', { name: '存储连接', exact: true }).click()
        await page.getByRole('button', { name: '查看详情', exact: true }).waitFor()
        await capture(page, `${prefix}-admin-connections`)
        await page.getByRole('tab', { name: '存储策略', exact: true }).click()
        await capture(page, `${prefix}-admin-policy`)
      }
      await context.close()
    }
  }
  if (phase === 'after') {
    for (const scenario of zoomOnly
      ? []
      : [
          'empty',
          'loading',
          'error',
          'permission',
          'maintenance',
          'missing-auth',
          'unknown',
          'over-capacity',
          'stale',
        ]) {
      const context = await browser.newContext({
        viewport: { width: 390, height: 844 },
        locale: 'zh-CN',
        timezoneId: 'Asia/Shanghai',
        reducedMotion: 'reduce',
      })
      const page = await context.newPage()
      const state = await mockStorageVisual(page, {
        scenario: scenario === 'stale' ? 'populated' : scenario,
      })
      await page.goto('http://127.0.0.1:4173/settings/storage')
      if (scenario === 'loading') await page.locator('.n-skeleton').first().waitFor()
      else if (scenario === 'error' || scenario === 'permission')
        await page.locator('.n-alert').first().waitFor()
      else if (scenario === 'empty') await page.getByText('暂无存储连接', { exact: true }).waitFor()
      else await page.getByRole('button', { name: '查看详情', exact: true }).first().waitFor()
      if (scenario === 'stale') {
        state.failed = true
        await page.getByRole('button', { name: '刷新存储', exact: true }).click()
        await page.locator('.n-alert').first().waitFor()
      }
      await capture(page, `390-light-state-${scenario}`)
      if (['maintenance', 'missing-auth', 'unknown'].includes(scenario)) {
        await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
        await page
          .locator('.n-drawer:visible')
          .getByRole('tab', { name: '连接与授权', exact: true })
          .click()
        await capture(page, `390-light-state-${scenario}-drawer`)
      }
      await context.close()
    }
    // 720 CSS px at DPR 2 models the reflow of a 1440 px display at 200% zoom.
    for (const [width, height, scale] of zoomOnly
      ? []
      : [
          [1024, 768, 1],
          [320, 720, 1],
          [720, 480, 2],
        ]) {
      const context = await browser.newContext({
        viewport: { width, height },
        deviceScaleFactor: scale,
        locale: 'zh-CN',
        timezoneId: 'Asia/Shanghai',
        reducedMotion: 'reduce',
      })
      const page = await context.newPage()
      await mockStorageVisual(page)
      const prefix = `${width}-light${scale === 2 ? '-200pct-reflow' : ''}`
      await page.goto('http://127.0.0.1:4173/settings/storage')
      await page.getByRole('button', { name: '查看详情', exact: true }).first().waitFor()
      await capture(page, `${prefix}-personal`)
      await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
      await page.locator('.n-drawer:visible').waitFor()
      await capture(page, `${prefix}-drawer`)
      await page.goto('http://127.0.0.1:4173/admin/storage')
      await page.getByText('诊断摘要', { exact: true }).waitFor()
      await capture(page, `${prefix}-admin`)
      await context.close()
    }
    // Chrome's extension API changes the browser's actual page zoom. This is
    // separate evidence from the DPR/viewport reflow cases above.
    const extension = fileURLToPath(
      new URL('../artifacts/storage-redesign/zoom-extension/', import.meta.url),
    )
    await mkdir(extension, { recursive: true })
    await writeFile(
      `${extension}/manifest.json`,
      JSON.stringify({
        manifest_version: 3,
        name: 'Storage zoom evidence',
        version: '1.0',
        permissions: ['tabs'],
        background: { service_worker: 'worker.js' },
      }),
    )
    await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
    for (const theme of ['light', 'dark']) {
      const context = await chromium.launchPersistentContext('', {
        channel: 'chromium',
        headless: true,
        viewport: { width: 1440, height: 960 },
        locale: 'zh-CN',
        timezoneId: 'Asia/Shanghai',
        reducedMotion: 'reduce',
        args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
      })
      try {
        const worker =
          context.serviceWorkers()[0] ??
          (await context.waitForEvent('serviceworker', { timeout: 10000 }))
        const page = await context.newPage()
        await mockStorageVisual(page, { theme })
        await page.goto('http://127.0.0.1:4173/settings/storage')
        const unzoomed = await page.evaluate(() => ({ innerWidth, innerHeight, devicePixelRatio }))
        const factor = await worker.evaluate(async () => {
          const [tab] = await globalThis.chrome.tabs.query({ url: 'http://127.0.0.1:4173/*' })
          await globalThis.chrome.tabs.setZoom(tab.id, 2)
          return globalThis.chrome.tabs.getZoom(tab.id)
        })
        await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
        const zoomed = await page.evaluate(() => ({ innerWidth, innerHeight, devicePixelRatio }))
        if (factor !== 2 || unzoomed.innerWidth !== 1440 || unzoomed.devicePixelRatio !== 1)
          throw new Error('Chrome did not apply the requested real 200% page zoom')
        const evidence = { browserZoom: factor, unzoomed, zoomed, method: 'chrome.tabs.setZoom' }
        const prefix = `1440-${theme}-real-200pct`
        await page.getByRole('button', { name: '查看详情', exact: true }).first().waitFor()
        await capture(page, `${prefix}-personal`, evidence)
        await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
        await capture(page, `${prefix}-personal-bottom`, evidence)
        await page.evaluate(() => window.scrollTo(0, 0))
        await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
        await page.locator('.n-drawer:visible').waitFor()
        await capture(page, `${prefix}-drawer`, evidence)
        await page.goto('http://127.0.0.1:4173/admin/storage')
        await page.getByText('诊断摘要', { exact: true }).waitFor()
        await capture(page, `${prefix}-admin`, evidence)
        await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
        await capture(page, `${prefix}-admin-bottom`, evidence)
      } finally {
        await context.close()
      }
    }
  }
} finally {
  await writeFile(`${output}/report.json`, JSON.stringify(report, null, 2))
  await browser.close()
}
if (phase === 'after' && report.some((entry) => entry.overflow))
  throw new Error('Storage screenshots contain document overflow; inspect report.json')
