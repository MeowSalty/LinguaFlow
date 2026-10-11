import { chromium, expect, test, type Locator, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import type { ApiSchemas } from '../../src/api/client'
import type { SSEEvent } from '../../src/composables/sseShared'
import { json } from './fixtures'
import { regressionApp } from './workspace-regression-fixtures'

const timestamp = '2026-10-09T00:00:00Z'
const stages: ApiSchemas['JobStageCounts'] = {
  main_requests: 2,
  pending_alignment: 8,
  alignment_requests: 3,
  saving_requests: 4,
  ready_to_commit: 5,
  confirmed_work: 7,
  unknown_requests: 1,
  draining_requests: 9,
  as_of: timestamp,
}

function stageJob(
  status: ApiSchemas['Job']['status'] = 'running',
  observation: ApiSchemas['JobStageCounts'] | undefined = stages,
): ApiSchemas['Job'] {
  const started = new Date(Date.now() - 60_000).toISOString()
  return {
    id: 42,
    project_id: 7,
    execution_plan_id: 101,
    status,
    can_delete: false,
    finished_at: null,
    trigger_type: 'manual',
    created_at: started,
    updated_at: started,
    started_at: started,
    execution_config: {},
    progress: {
      total_resources: 1,
      completed_resources: 0,
      failed_resources: 0,
      progress_total: 40,
      progress_completed: 10,
      stages: observation,
    },
    job_resources: [
      {
        id: 421,
        resource_id: 71,
        status: 'running',
        segment_count: 40,
        completed_segments: 10,
        skipped_segments: 0,
        work_weight: 1,
        created_at: started,
        updated_at: started,
        rounds: [
          {
            round_index: 0,
            mode: 'translate',
            status: 'running',
            segment_total: 40,
            segment_completed: 10,
            started_at: started,
          },
        ],
      },
    ],
  }
}

type TestStream = EventTarget & { url: string; closed: boolean }
type StreamWindow = Window & { __jobStageStreams: TestStream[] }

async function stageApp(page: Page, job: ApiSchemas['Job'], theme: 'light' | 'dark' = 'light') {
  const fixture = await regressionApp(page, { theme })
  const state = { job, eventCursors: [] as (string | null)[] }
  await page.addInitScript(() => {
    const host = window as unknown as StreamWindow
    host.__jobStageStreams = []
    class TestEventSource extends EventTarget {
      url: string
      closed = false
      onopen: (() => void) | null = null
      onerror: (() => void) | null = null
      constructor(url: string) {
        super()
        this.url = url
        host.__jobStageStreams.push(this)
        queueMicrotask(() => this.onopen?.())
      }
      close() {
        this.closed = true
      }
    }
    Object.defineProperty(window, 'EventSource', { value: TestEventSource })
  })
  await page.route('**/api/v1/jobs/42', (route) => json(route, state.job))
  await page.route('**/api/v1/projects/7/jobs*', (route) => json(route, { items: [state.job] }))
  await page.route('**/api/v1/jobs/42/events?**', (route) => json(route, { items: [] }))
  return { ...fixture, state }
}

async function openJob(page: Page) {
  await page.goto('/projects/7?tab=jobs')
  await page.getByRole('button', { name: '详情', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  await expect(drawer.getByTestId('job-overview-scroll')).toBeVisible()
  return drawer
}

async function emitStageEvent(page: Page, observation: ApiSchemas['JobStageCounts'], seq = 200) {
  const event: SSEEvent = {
    job_id: 42,
    type: 'stage_counts',
    level: 'info',
    message: 'stage counts observation',
    created_at: observation.as_of,
    seq,
    metadata: { stages: observation },
  }
  await page.waitForFunction(() =>
    (window as unknown as StreamWindow).__jobStageStreams.some(
      (stream) => stream.url.includes('/jobs/42/stream') && !stream.closed,
    ),
  )
  await page.evaluate((incoming) => {
    const stream = (window as unknown as StreamWindow).__jobStageStreams.findLast(
      (item) => item.url.includes('/jobs/42/stream') && !item.closed,
    )!
    stream.dispatchEvent(new MessageEvent(incoming.type, { data: JSON.stringify(incoming) }))
  }, event)
}

function valueFor(region: Locator, label: string) {
  return region
    .locator('dt')
    .filter({ hasText: new RegExp(`^${label}$`) })
    .locator('+ dd')
}

async function assertStageLayout(drawer: Locator) {
  const region = drawer.getByRole('region', { name: '阶段详情', exact: true })
  const toggle = region.locator('summary')
  await toggle.focus()
  await expect(toggle).toBeFocused()
  await toggle.press('Enter')
  await expect(valueFor(region, '待确认候选')).toHaveText('5 个候选段落')
  await expect(valueFor(region, '未知请求')).toHaveText('1 个请求')
  await expect(region.locator('time')).toHaveAttribute('datetime', timestamp)
  // Naive UI slides the drawer in; measure after it settles, with the same strict bounds.
  await expect(async () => {
    const metrics = await region.evaluate((element) => ({
      width: element.clientWidth,
      contentWidth: element.scrollWidth,
      left: element.getBoundingClientRect().left,
      right: element.getBoundingClientRect().right,
      viewport: innerWidth,
    }))
    expect(metrics.contentWidth).toBeLessThanOrEqual(metrics.width + 1)
    expect(metrics.left).toBeGreaterThanOrEqual(0)
    expect(metrics.right).toBeLessThanOrEqual(metrics.viewport)
  }).toPass({ timeout: 5_000 })
  await toggle.press('Space')
  await expect(valueFor(region, '待确认候选')).toBeHidden()
}

test('stage observations keep distinct units and do not change overall progress', async ({
  page,
}, testInfo) => {
  const fixture = await stageApp(page, stageJob())
  const drawer = await openJob(page)
  const region = drawer.getByRole('region', { name: '阶段详情', exact: true })
  await expect(valueFor(region, '主请求')).toHaveText(/^2\s*个请求$/)
  await expect(valueFor(region, '待完成注音候选')).toHaveText(/^8\s*个候选段落$/)
  await expect(valueFor(region, '注音请求')).toHaveText(/^3\s*个请求$/)
  await expect(valueFor(region, '保存中请求')).toHaveText(/^4\s*个请求$/)
  await expect(valueFor(region, '已确认工作量')).toHaveText(/^7\s*段落 × 轮$/)
  await expect(region).toContainText('待完成注音候选包含正在对齐的段落。')
  await expect(drawer.getByText('25%', { exact: true })).toBeVisible()
  await assertStageLayout(drawer)
  await emitStageEvent(page, { ...stages, confirmed_work: 11, as_of: '2026-10-09T00:00:01Z' })
  await expect(valueFor(region, '已确认工作量')).toHaveText(/^11\s*段落 × 轮$/)
  await expect(drawer.getByText('25%', { exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('stage-details.png'), animations: 'disabled' })
  expect(fixture.errors).toEqual([])
})

test('pausing with zero draining requests still waits for the server and hides ETA', async ({
  page,
}) => {
  const fixture = await stageApp(page, stageJob('pausing'))
  const drawer = await openJob(page)
  const notice = drawer.getByRole('status')
  await expect(notice).toContainText('还有 9 个请求等待收尾')
  await expect(drawer.getByText('预计剩余', { exact: true })).toHaveCount(0)
  await expect(drawer.getByText('预计完成', { exact: true })).toHaveCount(0)
  await emitStageEvent(page, { ...stages, draining_requests: 0, as_of: '2026-10-09T00:00:01Z' })
  await expect(notice).toContainText('请求已收尾，仍在等待保存确认。')
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeHidden()
  await expect(drawer.getByRole('button', { name: '取消', exact: true })).toBeVisible()
  expect(fixture.errors).toEqual([])
})

for (const status of ['running', 'pausing'] as const) {
  test(`${status} without stages hides stage information and preserves pause feedback`, async ({
    page,
  }) => {
    const job = stageJob(status)
    delete job.progress.stages
    const fixture = await stageApp(page, job)
    const drawer = await openJob(page)
    await expect(drawer.getByRole('region', { name: '阶段详情', exact: true })).toHaveCount(0)
    const notice = drawer.getByText('暂停中，正在等待请求收尾与保存确认。', { exact: true })
    if (status === 'pausing') await expect(notice).toBeVisible()
    else await expect(notice).toHaveCount(0)
    expect(fixture.errors).toEqual([])
  })
}

test('stage-only event pages remain pageable without exposing snapshots as logs or replaying them', async ({
  page,
}) => {
  const fixture = await stageApp(page, stageJob('paused'))
  await page.route('**/api/v1/jobs/42/events?**', (route) => {
    const before = new URL(route.request().url()).searchParams.get('before_seq')
    fixture.state.eventCursors.push(before)
    const event: SSEEvent = before
      ? {
          job_id: 42,
          type: 'job_started',
          level: 'info',
          message: '任务已开始',
          seq: 99,
          created_at: timestamp,
        }
      : {
          job_id: 42,
          type: 'stage_counts',
          level: 'error',
          message: 'SECRET_CANDIDATE_STAGE_PAYLOAD',
          seq: 100,
          created_at: timestamp,
          metadata: { stages: { ...stages, confirmed_work: 999, as_of: '2026-10-09T00:01:00Z' } },
        }
    return json(route, { items: [event], next_before_seq: before ? undefined : 100 })
  })
  const drawer = await openJob(page)
  await expect(
    valueFor(drawer.getByRole('region', { name: '阶段详情' }), '已确认工作量'),
  ).toHaveText(/^7\s*段落 × 轮$/)
  await drawer.locator('.n-tabs-tab[data-name="events"]').click()
  await expect(drawer.getByText('SECRET_CANDIDATE_STAGE_PAYLOAD')).toHaveCount(0)
  await expect(drawer.getByText('已加载 0 条记录', { exact: true })).toBeVisible()
  await drawer.getByRole('button', { name: '加载更早记录', exact: true }).click()
  await expect(drawer.getByText('任务已开始', { exact: true })).toBeVisible()
  await expect(drawer.getByText('已加载 1 条记录', { exact: true })).toBeVisible()
  expect(fixture.state.eventCursors).toContain('100')
  await drawer.getByRole('button', { name: '异常', exact: true }).click()
  await expect(drawer.getByText('SECRET_CANDIDATE_STAGE_PAYLOAD')).toHaveCount(0)
  expect(fixture.errors).toEqual([])
})

test('stage details reflow and remain keyboard operable at real 200 percent browser zoom', async ({
  browserName,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium' || browserName !== 'chromium',
    'Chrome extension exercises actual browser zoom once',
  )
  const extension = testInfo.outputPath('zoom-extension')
  await mkdir(extension, { recursive: true })
  await writeFile(
    `${extension}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'Stage details zoom verification',
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
    const page = await context.newPage()
    const fixture = await stageApp(page, stageJob('pausing'), 'dark')
    const drawer = await openJob(page)
    expect(await page.evaluate(() => ({ width: innerWidth, dpr: devicePixelRatio }))).toEqual({
      width: 1440,
      dpr: 1,
    })
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
    await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
    await assertStageLayout(drawer)
    await page.screenshot({ path: testInfo.outputPath('stage-details-200pct-dark.png') })
    expect(fixture.errors).toEqual([])
  } finally {
    await context.close()
  }
})
