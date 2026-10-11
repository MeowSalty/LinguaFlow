import assert from 'node:assert/strict'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'
import { beforeAll, test as base } from 'vitest'
import {
  buildConfigurationBackend,
  createConfigurationBackend,
} from './fixtures/configuration-backend.mjs'
import { createRubyProductProxy } from './fixtures/ruby-product-proxy.mjs'

let metadata
beforeAll(() => {
  metadata = buildConfigurationBackend()
})

const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const backend = await createConfigurationBackend(metadata, signal, {
      category: 'ruby-product',
      upstreamTimeoutSeconds: 180,
      respond: (envelope) =>
        envelope.missing
          ? {
              ruby_output: envelope.missing.map((item) => ({
                id: item.id,
                base: 'alpha',
                text: 'hero',
                kind: 'creative',
                occurrence: 1,
              })),
            }
          : {
              translations: Object.fromEntries(
                Object.entries(envelope.segments)
                  .filter(([, segment]) => segment.translate)
                  .map(([id]) => [id, 'alpha']),
              ),
            },
    })
    onTestFinished(() => backend.writeReport(task))
    try {
      await backend.start()
      await use(backend)
      assert.deepEqual(backend.upstreamErrors, [])
    } finally {
      await backend.close()
    }
  },
  product: async ({ backend }, use) => {
    const proxy = await createRubyProductProxy(backend)
    let browser
    const failures = []
    try {
      // Missing Chromium is a test failure, never a silently skipped acceptance scenario.
      browser = await chromium.launch({
        headless: true,
        executablePath: process.env.LINGUAFLOW_TEST_CHROMIUM,
      })
      const freshPage = async (options = {}) => {
        const context = await browser.newContext({
          viewport: options.viewport ?? { width: 1440, height: 960 },
        })
        await context.addInitScript(
          ({ access, refresh, theme }) => {
            localStorage.setItem('linguaflow.api_base_url', '/api/v1')
            localStorage.setItem('linguaflow.access_token', access)
            localStorage.setItem('linguaflow.refresh_token', refresh)
            localStorage.setItem('linguaflow.theme', theme)
          },
          {
            access: backend.authSession.access_token,
            refresh: backend.authSession.refresh_token,
            theme: options.theme ?? 'light',
          },
        )
        const page = await context.newPage()
        page.on('pageerror', (error) => failures.push(error.message))
        return page
      }
      await use({ ...proxy, freshPage })
      assert.deepEqual(failures, [])
    } finally {
      backend.recordObservation('real product transport', {
        productionBuild: true,
        chromiumVersion: browser?.version() ?? null,
        chromiumExecutable: process.env.LINGUAFLOW_TEST_CHROMIUM ?? 'Playwright default',
        businessResponsesMocked: false,
        sseStreamed: true,
        pageErrors: failures,
        requests: proxy.requests,
      })
      await browser?.close()
      await proxy.close()
    }
  },
})

async function rubyWorkflow(backend) {
  const flow = await backend.makeWorkflow('ruby-product-main')
  const alignment = await backend.request(
    'POST',
    '/backends',
    backend.backendBody('Alignment', 'ruby-product-align', backend.endpoint, flow.credential.id),
    201,
  )
  await backend.request('PUT', `/execution-profiles/${flow.profile.id}`, {
    config: { schema_version: 1, ruby: { enabled: true, preserve_kinds: ['creative'] } },
  })
  await backend.request('PUT', `/execution-plan-templates/${flow.plan.id}`, {
    ruby_retry: { enabled: true, backend_id: alignment.id, max_attempts: 2, concurrency: 1 },
  })
  return flow
}

async function pausingJob(backend, flow, name) {
  const project = await backend.request(
    'POST',
    '/projects',
    { name, source_lang: 'ja', target_lang: 'en', glossary_enabled: false },
    201,
  )
  const form = new FormData()
  form.append(
    'files',
    new Blob(
      [
        JSON.stringify({
          one: '<ruby>勇者<rt>ヒーロー</rt></ruby> one',
          two: '<ruby>勇者<rt>ヒーロー</rt></ruby> two',
          three: '<ruby>勇者<rt>ヒーロー</rt></ruby> three',
        }),
      ],
      { type: 'application/json' },
    ),
    `${name}.json`,
  )
  await backend.request('POST', `/projects/${project.id}/resources`, form)
  backend.holds.add('ruby-product-align')
  const job = await backend.startJob(project, flow.plan)
  await backend.until(
    () => backend.getJob(job.id),
    (item) =>
      item.progress.stages.alignment_requests === 1 && item.progress.stages.pending_alignment > 0,
    'explicit alignment request barrier',
  )
  assert.equal((await backend.request('POST', `/jobs/${job.id}/pause`)).status, 'pausing')
  return { job, project }
}

async function counts(page, running, pausing, paused, active) {
  for (const [status, value] of Object.entries({ running, pausing, paused }))
    await expect(
      page.getByTestId(`operation-count-${status}`).locator('[data-count-value]'),
    ).toHaveText(String(value))
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName(
    `当前任务 ${active}`,
  )
}

async function openDetail(page, product, item) {
  await page.goto(
    `${product.origin}/operations?task_type=translation&task_id=${item.job.id}&project_id=${item.project.id}&state=active`,
  )
  const drawer = page.locator('.n-drawer:visible')
  await expect(drawer.getByTestId('job-overview-scroll')).toBeVisible()
  return drawer
}

const streamRequests = (product, id) =>
  product.requests.filter(
    (item) => item.path === `/api/v1/jobs/${id}/stream` && item.status === 200,
  )

test('T32a fresh browsers, reload, sole pausing counts and real SSE reconnect converge only after safe pause', async ({
  backend,
  product,
}) => {
  const flow = await rubyWorkflow(backend)
  const item = await pausingJob(backend, flow, 'T32 sole pausing')
  const page = await product.freshPage()
  // No task ID, cached job, or SSE subscription is supplied to global discovery.
  for (const route of ['/operations?status=pausing', '/', '/stats']) {
    await page.goto(`${product.origin}${route}`)
    await counts(page, 0, 1, 0, 1)
  }
  await page.goto(`${product.origin}/operations?status=pausing`)
  await expect(page.getByText(item.project.name, { exact: true }).first()).toBeVisible()
  await page.reload()
  await counts(page, 0, 1, 0, 1)
  assert.equal(
    product.requests.some((entry) => entry.path.endsWith('/stream')),
    false,
    'global discovery must not subscribe per task',
  )
  assert.ok(
    product.requests.some(
      (entry) =>
        entry.path === '/api/v1/operations' &&
        entry.statusFilter === 'pausing' &&
        entry.state == null,
    ),
  )
  for (const options of [
    { theme: 'dark' },
    { theme: 'light', viewport: { width: 390, height: 844 } },
  ]) {
    const fresh = await product.freshPage(options)
    await fresh.goto(`${product.origin}/operations?status=pausing`)
    await counts(fresh, 0, 1, 0, 1)
    await expect(fresh.getByText(item.project.name, { exact: true }).first()).toBeVisible()
    await fresh.screenshot({
      path: path.join(
        backend.runDir,
        `pausing-${options.theme}-${options.viewport ? 'mobile' : 'desktop'}.png`,
      ),
      fullPage: true,
    })
    await fresh.context().close()
  }
  const drawer = await openDetail(page, product, item)
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  await expect(drawer.getByRole('button', { name: '取消', exact: true })).toBeVisible()
  for (const name of ['恢复', '暂停', '重试'])
    await expect(drawer.getByRole('button', { name, exact: true })).toBeHidden()
  await backend.until(
    () => streamRequests(product, item.job.id),
    (items) => items.some((entry) => entry.bytes > 0),
    'real streaming SSE response delivered',
  )
  const beforeOffline = product.requests.length
  const beforeStreams = streamRequests(product, item.job.id).length
  await page.context().setOffline(true)
  product.disconnectStreams()
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeHidden()
  assert.equal((await backend.getJob(item.job.id)).status, 'pausing')
  await page.context().setOffline(false)
  await backend.until(
    () => streamRequests(product, item.job.id).length,
    (count) => count > beforeStreams,
    'SSE reconnected over real proxy',
  )
  await backend.until(
    () => product.requests.slice(beforeOffline),
    (items) =>
      items.some(
        (entry) =>
          entry.path === '/api/v1/operations' &&
          entry.state === 'active' &&
          entry.cursor == null &&
          entry.status === 200,
      ),
    'online recovery starts discovery from first page',
  )
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  backend.release('ruby-product-align')
  await backend.until(
    () => backend.getJob(item.job.id),
    (job) => job.status === 'paused',
    'safe pause after releasing upstream barrier',
  )
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('已暂停')
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeVisible()
  await drawer.getByRole('button', { name: '关闭', exact: true }).click()
  await counts(page, 0, 0, 1, 1)
  backend.recordObservation('T32a acceptance', {
    freshContexts: 3,
    reload: true,
    pages: ['operations', 'home', 'stats'],
    solePausingCounts: [0, 1, 0],
    activeCount: 1,
    disconnectedAndReconnectedSSE: true,
    restoreOnlyAfterServerPaused: true,
  })
})

test('T32b genuine cursor discovery, restart while pausing, and authorized cancel resist late upstream completion', async ({
  backend,
  product,
}) => {
  const flow = await rubyWorkflow(backend)
  const settled = []
  for (const name of ['T32 page one', 'T32 page two']) {
    const item = await pausingJob(backend, flow, name)
    backend.release('ruby-product-align')
    await backend.until(
      () => backend.getJob(item.job.id),
      (job) => job.status === 'paused',
      'prepare real paused page',
    )
    settled.push(item)
  }
  const restarting = await pausingJob(backend, flow, 'T32 restart pausing')
  product.capActivePageSize(1)
  const page = await product.freshPage()
  await page.goto(`${product.origin}/settings/preferences`)
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName('当前任务 3')
  await page.getByTestId('global-job-tracker-trigger').click()
  const list = page.getByTestId('global-job-tracker-list')
  for (const item of [...settled, restarting])
    await expect(
      list.getByRole('button', { name: new RegExp(`${item.project.name} 翻译 #${item.job.id}`) }),
    ).toBeVisible()
  const discovery = product.requests.filter(
    (entry) =>
      entry.path === '/api/v1/operations' && entry.state === 'active' && entry.status === 200,
  )
  assert.equal(discovery[0].cursor, null)
  assert.ok(
    new Set(discovery.filter((entry) => entry.cursor).map((entry) => entry.cursor)).size >= 2,
    'three genuine backend pages traversed',
  )
  const drawer = await openDetail(page, product, restarting)
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  const requestsBeforeRestart = backend.calls.length
  await backend.restart()
  const recovered = await backend.until(
    () => backend.getJob(restarting.job.id),
    (job) => job.status === 'paused',
    'process restart recovery',
  )
  assert.equal(recovered.progress.stages.draining_requests, 0)
  assert.equal(
    backend.calls.length,
    requestsBeforeRestart,
    'restart must retain pause intent without dispatching new model requests',
  )
  backend.release('ruby-product-align')
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('已暂停', { timeout: 30_000 })
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeVisible()
  const cancelling = await pausingJob(backend, flow, 'T32 cancel pausing')
  const cancelledDrawer = await openDetail(page, product, cancelling)
  await expect(cancelledDrawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  const held = backend.calls.filter(
    (call) => call.model === 'ruby-product-align' && call.completedAt == null,
  )
  assert.ok(held.length > 0)
  await cancelledDrawer.getByRole('button', { name: '取消', exact: true }).click()
  await expect(cancelledDrawer.locator('.n-drawer-header .n-tag')).toHaveText('已取消')
  const cancelled = await backend.getJob(cancelling.job.id)
  assert.equal(cancelled.status, 'cancelled')
  const cancelledAt = Date.now()
  backend.release('ruby-product-align')
  await backend.until(
    () => held,
    (calls) => calls.every((call) => call.completedAt >= cancelledAt),
    'late upstream responses after authorized cancellation',
  )
  const observationsBefore = product.requests.length
  await page.reload()
  await expect(cancelledDrawer.locator('.n-drawer-header .n-tag')).toHaveText('已取消')
  await backend.until(
    () => product.requests.slice(observationsBefore),
    (items) =>
      items.some(
        (entry) =>
          entry.path === '/api/v1/operations' && entry.state === 'active' && entry.status === 200,
      ),
    'post-cancel authoritative rediscovery',
  )
  assert.equal((await backend.getJob(cancelling.job.id)).status, 'cancelled')
  const active = await backend.request('GET', '/operations?state=active')
  assert.ok(
    active.items.every(
      (item) =>
        item.task_type !== 'translation' || String(item.task_id) !== String(cancelling.job.id),
    ),
  )
  const terminal = await backend.request('GET', '/operations?status=cancelled')
  assert.equal(
    terminal.items.find((item) => String(item.task_id) === String(cancelling.job.id))?.status,
    'cancelled',
  )
  assert.equal(
    product.requests.filter(
      (entry) =>
        entry.method === 'POST' && entry.path === `/api/v1/jobs/${cancelling.job.id}/cancel`,
    ).length,
    1,
  )
  await cancelledDrawer.getByRole('button', { name: '关闭', exact: true }).click()
  await page.goto(`${product.origin}/operations?state=active`)
  await counts(page, 0, 0, 3, 3)
  backend.recordObservation('T32b acceptance', {
    genuineCursorPages: 3,
    restartRecoveredPaused: true,
    restartDispatchedModelRequests: 0,
    cancellationInitiatedThroughUI: true,
    delayedUpstreamCompletedAfterCancel: held.length,
    cancelledStatusStableAfterReload: true,
  })
})
