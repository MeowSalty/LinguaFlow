import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { mockStorageVisual } from './storage-visual-fixtures.ts'
import { regressionApp } from './workspace-regression-fixtures.ts'
import {
  assertTaskHeader,
  assertUnobscured,
  mockTaskHeader,
  openAndCheckTaskPanel,
  scrollWorkspacePane,
  selectWorkspaceItem,
  selectionClear,
} from './task-header-visual-helpers.ts'

// Run against an already-built production preview at http://127.0.0.1:4173.
// task frontend:test:task-header-visual -- after
const phase = process.argv[2] ?? 'after'
if (!/^[a-z0-9-]+$/.test(phase)) throw new Error('Use a simple lowercase evidence directory name')
const output = fileURLToPath(new URL(`../artifacts/task-header/${phase}/`, import.meta.url))
await mkdir(output, { recursive: true })
const report = []
const failures = []
const baseURL = 'http://127.0.0.1:4173'

async function screenshot(page, name, evidence) {
  await page.evaluate(() => document.fonts.ready)
  // Raw CDP captures do not support Playwright's animations: 'disabled' option.
  // Wait for finite transitions so real-zoom evidence does not capture a fading bar.
  await page.evaluate(async () => {
    await Promise.all(
      document
        .getAnimations()
        .filter((animation) => animation.effect?.getComputedTiming().iterations !== Infinity)
        .map((animation) => animation.finished.catch(() => undefined)),
    )
  })
  if (evidence.browserZoom) {
    const session = await page.context().newCDPSession(page)
    try {
      const { data } = await session.send('Page.captureScreenshot', {
        format: 'png',
        fromSurface: true,
        captureBeyondViewport: false,
      })
      await writeFile(`${output}/${name}.png`, Buffer.from(data, 'base64'))
    } finally {
      await session.detach()
    }
  } else {
    await page.screenshot({
      path: `${output}/${name}.png`,
      fullPage: false,
      animations: 'disabled',
    })
  }
  const dimensions = await page.evaluate(() => ({
    width: innerWidth,
    height: innerHeight,
    scrollWidth: document.documentElement.scrollWidth,
    scrollY,
    devicePixelRatio,
  }))
  report.push({ name, ...evidence, ...dimensions, visualReview: 'pending' })
  console.log(name, JSON.stringify(dimensions))
}

async function setZoom(worker, factor) {
  return worker.evaluate(
    async ({ baseURL, factor }) => {
      const [tab] = await globalThis.chrome.tabs.query({ url: `${baseURL}/*` })
      if (!tab) throw new Error('No preview tab is available for real browser zoom')
      await globalThis.chrome.tabs.setZoom(tab.id, factor)
      return globalThis.chrome.tabs.getZoom(tab.id)
    },
    { baseURL, factor },
  )
}

async function captureScenario(context, theme, scenario, worker) {
  const page = await context.newPage()
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  const editor = scenario === 'project-editor'
  const project = scenario !== 'storage'
  try {
    if (project) await regressionApp(page, { theme, populated: true })
    else await mockStorageVisual(page, { theme })
    await mockTaskHeader(page)
    await page.setViewportSize({ width: worker ? 1440 : 1280, height: 960 })
    await page.goto(
      `${baseURL}${project ? (editor ? '/projects/7?edit=71' : '/projects/7') : '/settings/storage'}`,
    )
    if (worker) {
      await setZoom(worker, 1)
      await page.waitForFunction(() => innerWidth === 1440 && devicePixelRatio === 1)
    }
    if (project) await selectWorkspaceItem(page, editor)
    else await page.getByRole('button', { name: '查看详情', exact: true }).first().waitFor()

    let evidence = { theme, scenario, viewportMode: '320-css-pixels' }
    if (worker) {
      const unzoomed = await page.evaluate(() => ({
        width: innerWidth,
        height: innerHeight,
        devicePixelRatio,
      }))
      const browserZoom = await setZoom(worker, 2)
      await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
      if (browserZoom !== 2 || unzoomed.width !== 1440 || unzoomed.devicePixelRatio !== 1)
        throw new Error('Chrome did not apply real 200% browser zoom')
      evidence = { theme, scenario, browserZoom, method: 'chrome.tabs.setZoom', unzoomed }
    } else {
      await page.setViewportSize({ width: 320, height: 720 })
    }

    if (project) evidence.paneScroll = await scrollWorkspacePane(page, editor)
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    evidence.trigger = await assertTaskHeader(page)
    const prefix = `${worker ? '1440-real-200pct' : '320'}-${theme}-${scenario}`
    if (project) await assertUnobscured(selectionClear(page))
    await screenshot(page, `${prefix}-closed-bottom`, evidence)
    evidence.panel = await openAndCheckTaskPanel(page)
    await screenshot(page, `${prefix}-panel`, evidence)
    await page.getByTestId('global-job-tracker-list').evaluate((element) => {
      element.scrollTop = element.scrollHeight
    })
    await screenshot(page, `${prefix}-panel-list-bottom`, evidence)
    await page
      .getByTestId('global-job-tracker-panel')
      .getByRole('button', { name: '关闭', exact: true })
      .click()
    if (project) {
      const clear = selectionClear(page)
      await assertUnobscured(clear)
      await clear.click()
      await clear.waitFor({ state: 'hidden' })
    }
    if (errors.length) throw new Error(`Browser errors: ${errors.join('; ')}`)
  } finally {
    await page.close()
  }
}

async function runScenario(context, theme, scenario, worker) {
  try {
    await captureScenario(context, theme, scenario, worker)
  } catch (error) {
    failures.push({ theme, scenario, realZoom: !!worker, error: String(error) })
    console.error(theme, scenario, String(error))
  }
}

const browser = await chromium.launch()
try {
  for (const theme of ['light', 'dark']) {
    for (const scenario of ['storage', 'project-browser', 'project-editor']) {
      const context = await browser.newContext({
        viewport: { width: 1280, height: 960 },
        deviceScaleFactor: 1,
        locale: 'zh-CN',
        timezoneId: 'Asia/Shanghai',
        reducedMotion: 'reduce',
      })
      try {
        await runScenario(context, theme, scenario)
      } finally {
        await context.close()
      }
    }
  }
} finally {
  await browser.close()
}

// DPR alone is not page zoom. Chrome's extension API changes actual browser zoom.
const extension = fileURLToPath(
  new URL('../artifacts/task-header/zoom-extension/', import.meta.url),
)
await mkdir(extension, { recursive: true })
await writeFile(
  `${extension}/manifest.json`,
  JSON.stringify({
    manifest_version: 3,
    name: 'Task header zoom evidence',
    version: '1.0',
    permissions: ['tabs'],
    background: { service_worker: 'worker.js' },
  }),
)
await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
try {
  for (const theme of ['light', 'dark']) {
    const context = await chromium.launchPersistentContext('', {
      channel: 'chromium',
      headless: true,
      viewport: { width: 1440, height: 960 },
      deviceScaleFactor: 1,
      locale: 'zh-CN',
      timezoneId: 'Asia/Shanghai',
      reducedMotion: 'reduce',
      args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
    })
    try {
      const worker =
        context.serviceWorkers()[0] ??
        (await context.waitForEvent('serviceworker', { timeout: 10000 }))
      for (const scenario of ['storage', 'project-browser', 'project-editor'])
        await runScenario(context, theme, scenario, worker)
    } finally {
      await context.close()
    }
  }
} finally {
  await writeFile(`${output}/report.json`, JSON.stringify({ captures: report, failures }, null, 2))
}
if (failures.length)
  throw new Error(`${failures.length} task-header visual scenarios failed; inspect report.json`)
console.log(`Saved ${report.length} screenshots. Visual inspection remains required: ${output}`)
