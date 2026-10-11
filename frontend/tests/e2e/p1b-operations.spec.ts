import { chromium, expect, test, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import type { ApiSchemas } from '../../src/api/client'
import { emptyCounts, json, mockApp, summaryFixture } from './fixtures'

const projectName = 'P1b 暂停中的翻译项目'
const timestamp = '2026-10-10T00:00:00.123456789Z'

async function pausingApp(page: Page, theme: 'light' | 'dark' = 'light') {
  await mockApp(page, { theme })
  const state = {
    status: 'pausing' as 'pausing' | 'paused',
    queries: [] as URL[],
    summaryQueries: [] as URL[],
    errors: [] as string[],
    legacySummary: false,
  }
  page.on('pageerror', (error) => state.errors.push(error.message))
  const job = (): ApiSchemas['Job'] => ({
    id: 42,
    project_id: 7,
    execution_plan_id: 1,
    status: state.status,
    can_delete: false,
    finished_at: null,
    trigger_type: 'manual',
    created_at: timestamp,
    updated_at: state.status === 'pausing' ? timestamp : '2026-10-10T00:00:01.123456789Z',
    progress: {
      total_resources: 1,
      completed_resources: 0,
      failed_resources: 0,
      progress_total: 10,
      progress_completed: 2,
      stages: {
        main_requests: 0,
        pending_alignment: 1,
        alignment_requests: 0,
        saving_requests: 0,
        ready_to_commit: 0,
        confirmed_work: 2,
        unknown_requests: 0,
        draining_requests: 0,
        as_of: timestamp,
      },
    },
    job_resources: [],
  })
  const operation = (): ApiSchemas['OperationSummary'] => ({
    task_type: 'translation',
    task_id: '42',
    project_id: 7,
    project_name: projectName,
    trigger_type: 'manual',
    status: state.status,
    can_delete: false,
    finished_at: null,
    created_at: timestamp,
    updated_at: job().updated_at,
    progress: job().progress,
    // Even a stale capability hint must not expose resume/pause/retry while pausing.
    supported_actions: ['view', 'pause', 'resume', 'cancel', 'retry'],
  })
  await page.route('**/api/v1/operations/summary**', (route) => {
    state.summaryQueries.push(new URL(route.request().url()))
    const total = { ...emptyCounts, [state.status]: 1, recent_failed: 9 }
    if (state.legacySummary) delete (total as Partial<typeof total>).pausing
    return json(route, {
      ...summaryFixture,
      total,
      by_type: { translation: total, glossary_sync: emptyCounts, storage: emptyCounts },
    })
  })
  await page.route('**/api/v1/operations?**', (route) => {
    const url = new URL(route.request().url())
    state.queries.push(url)
    const status = url.searchParams.get('status')
    const match =
      url.searchParams.get('state') !== 'terminal' && (!status || status === state.status)
    return json(route, { items: match ? [operation()] : [] })
  })
  await page.route('**/api/v1/projects/7', (route) =>
    json(route, {
      id: 7,
      name: projectName,
      source_lang: 'en',
      target_lang: 'zh',
      owner_user_id: 1,
    }),
  )
  await page.route('**/api/v1/jobs/42', (route) => json(route, job()))
  await page.route('**/api/v1/jobs/42/stream**', (route) =>
    route.fulfill({
      contentType: 'text/event-stream',
      body: ': connected\n\n',
    }),
  )
  return state
}

async function expectPausingCounts(page: Page) {
  const grid = page.getByTestId('operation-counts')
  await expect(grid.locator('[data-count-value]')).toHaveCount(7)
  for (const [key, value] of Object.entries({
    running: 0,
    pending: 0,
    pausing: 1,
    paused: 0,
    waiting_retry: 0,
    needs_action: 0,
    recent_failed: 9,
  })) {
    await expect(
      page.getByTestId(`operation-count-${key}`).locator('[data-count-value]'),
    ).toHaveText(String(value))
  }
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName('当前任务 1')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
}

async function expectColumns(page: Page, columns: number) {
  await expect
    .poll(() =>
      page
        .getByTestId('operation-counts')
        .evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(' ').length),
    )
    .toBe(columns)
}

for (const theme of ['light', 'dark'] as const) {
  test(`sole pausing task is independently counted on home, stats and task center in ${theme}`, async ({
    page,
  }, testInfo) => {
    const state = await pausingApp(page, theme)
    for (const path of ['/', '/stats', '/operations?status=pausing']) {
      await page.goto(path)
      await expectPausingCounts(page)
      await expectColumns(page, testInfo.project.name === 'mobile' ? 2 : 7)
      if (path === '/') {
        const recovery = page
          .locator('section')
          .filter({ has: page.getByRole('heading', { name: '暂停待继续', exact: true }) })
        await expect(recovery).toContainText('没有暂停待继续的任务')
        await expect(recovery).not.toContainText(projectName)
        expect(state.queries.some((url) => url.searchParams.get('status') === 'paused')).toBe(true)
      }
      await page.screenshot({
        path: testInfo.outputPath(
          `${theme}-${path === '/' ? 'home' : path.startsWith('/stats') ? 'stats' : 'operations'}.png`,
        ),
        fullPage: true,
      })
    }
    const status = page.locator('.n-base-selection').filter({ hasText: '暂停中' })
    await expect(status).toBeVisible()
    await page.reload()
    await expectPausingCounts(page)
    await expect(page.getByText(projectName, { exact: true })).toBeVisible()
    await page.getByTestId('global-job-tracker-trigger').click()
    await expect(
      page.getByTestId('global-job-tracker-list').getByRole('button', { name: /P1b.*暂停中/ }),
    ).toBeVisible()
    if (testInfo.project.name === 'chromium') {
      await page.setViewportSize({ width: 900, height: 900 })
      await expectColumns(page, 3)
    }
    expect(state.errors).toEqual([])
  })
}

test('pausing selector preserves status/state exclusivity and summary dimensions', async ({
  page,
}) => {
  const state = await pausingApp(page)
  await page.goto(
    '/operations?task_type=translation&project_id=7&trigger_type=manual&status=pausing',
  )
  await expectPausingCounts(page)
  await page.locator('.n-base-selection').filter({ hasText: '暂停中' }).click()
  await page
    .locator('.n-base-select-option')
    .filter({ hasText: /^活动任务$/ })
    .click()
  await expect(page).toHaveURL(/state=active/)
  expect(new URL(page.url()).searchParams.has('status')).toBe(false)
  await page.locator('.n-base-selection').filter({ hasText: '活动任务' }).click()
  await page
    .locator('.n-base-select-option')
    .filter({ hasText: /^暂停中$/ })
    .click()
  await expect(page).toHaveURL(/status=pausing/)
  expect(new URL(page.url()).searchParams.has('state')).toBe(false)
  await expect
    .poll(() => state.queries.filter((url) => url.searchParams.get('status') === 'pausing').length)
    .toBeGreaterThan(0)
  for (const url of state.queries)
    expect(url.searchParams.has('state') && url.searchParams.has('status')).toBe(false)
  for (const url of state.summaryQueries)
    expect(
      [...url.searchParams.keys()].every((key) =>
        ['task_type', 'project_id', 'trigger_type'].includes(key),
      ),
    ).toBe(true)
})

test('zero draining observations do not enable resume until a server-confirmed pause survives reload', async ({
  page,
}) => {
  const state = await pausingApp(page)
  await page.goto('/operations?task_type=translation&task_id=42&project_id=7')
  const drawer = page.locator('.n-drawer:visible')
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  await expect(drawer.getByRole('status')).toContainText('请求已收尾，仍在等待保存确认。')
  for (const name of ['恢复', '暂停', '重试'])
    await expect(drawer.getByRole('button', { name, exact: true })).toBeHidden()
  await expect(drawer.getByRole('button', { name: '取消', exact: true })).toBeVisible()
  state.status = 'paused'
  await page.reload()
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('已暂停')
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeVisible()
  await expect(
    page.getByTestId('operation-count-pausing').locator('[data-count-value]'),
  ).toHaveText('0')
  await expect(page.getByTestId('operation-count-paused').locator('[data-count-value]')).toHaveText(
    '1',
  )
  expect(state.errors).toEqual([])
})

test('a legacy missing pausing count is zero while discovered task status stays pausing', async ({
  page,
}) => {
  const state = await pausingApp(page)
  state.legacySummary = true
  await page.goto('/operations?status=pausing')
  await expect(
    page.getByTestId('operation-count-pausing').locator('[data-count-value]'),
  ).toHaveText('0')
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName('当前任务 0')
  await expect(page.getByText(projectName, { exact: true })).toBeVisible()
  await expect(page.locator('.n-tag').filter({ hasText: /^暂停中$/ })).toBeVisible()
})

test('pausing count cards reflow at real 200 percent browser zoom in both themes', async ({
  browserName,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium' || browserName !== 'chromium',
    'Actual Chrome zoom is exercised once',
  )
  const extension = testInfo.outputPath('zoom-extension')
  await mkdir(extension, { recursive: true })
  await writeFile(
    `${extension}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'P1b counts zoom verification',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
  )
  await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
  const context = await chromium.launchPersistentContext('', {
    channel: 'chromium',
    executablePath: process.env.LINGUAFLOW_TEST_CHROMIUM,
    headless: true,
    baseURL: 'http://127.0.0.1:4173',
    viewport: { width: 1440, height: 960 },
    deviceScaleFactor: 1,
    args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
  })
  try {
    const worker = context.serviceWorkers()[0] ?? (await context.waitForEvent('serviceworker'))
    for (const theme of ['light', 'dark'] as const) {
      const page = await context.newPage()
      const state = await pausingApp(page, theme)
      await page.goto('/operations?status=pausing')
      const zoom = await worker.evaluate(async () => {
        const api = (
          globalThis as unknown as {
            chrome: {
              tabs: {
                query(query: { url: string }): Promise<{ id: number }[]>
                setZoom(id: number, factor: number): Promise<void>
                getZoom(id: number): Promise<number>
              }
            }
          }
        ).chrome
        const [tab] = await api.tabs.query({ url: 'http://127.0.0.1:4173/*' })
        await api.tabs.setZoom(tab!.id, 2)
        return api.tabs.getZoom(tab!.id)
      })
      expect(zoom).toBe(2)
      for (const path of ['/operations?status=pausing', '/', '/stats']) {
        await page.goto(path)
        await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
        await expectPausingCounts(page)
        await expectColumns(page, 2)
        await page.getByTestId('global-job-tracker-trigger').click()
        await expect(
          page.getByTestId('global-job-tracker-list').getByRole('button', { name: /P1b.*暂停中/ }),
        ).toBeVisible()
        await page.screenshot({
          path: testInfo.outputPath(
            `200pct-${theme}-${path === '/' ? 'home' : path.startsWith('/stats') ? 'stats' : 'operations'}.png`,
          ),
        })
      }
      expect(state.errors).toEqual([])
      await page.close()
    }
  } finally {
    await context.close()
  }
})
