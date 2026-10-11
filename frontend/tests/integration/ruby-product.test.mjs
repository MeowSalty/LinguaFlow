import assert from 'node:assert/strict'
import path from 'node:path'
import { chromium, expect } from '@playwright/test'
import { beforeAll, test as base } from 'vitest'
import {
  buildConfigurationBackend,
  createConfigurationBackend,
} from './fixtures/configuration-backend.mjs'
import { createRubyProductProxy } from './fixtures/ruby-product-proxy.mjs'
import {
  assertRubyResults,
  makeRubyProject,
  makeRubyWorkflow,
  observeRubyRequest,
  rubyResponse,
} from './fixtures/ruby-backend.mjs'

let metadata
beforeAll(() => {
  metadata = buildConfigurationBackend()
})

const test = base.extend({
  backend: async ({ task, signal, onTestFinished }, use) => {
    const backend = await createConfigurationBackend(metadata, signal, {
      category: 'ruby-product',
      upstreamTimeoutSeconds: 180,
      respond: rubyResponse,
      observeRequest: observeRubyRequest,
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

test('P2 browser configuration, four-member pause drain and resume preserve confirmed work and request accounting', async ({
  backend,
  product,
}) => {
  const flow = await makeRubyWorkflow(backend, {
    mainModel: 'ruby-product-main',
    alignmentModel: 'ruby-product-align',
    mainBatch: 4,
  })
  const page = await product.freshPage()
  const configuration = [
    ['每次注音最多段数', '4'],
    ['每次注音最多字词数', '0'],
    ['最大凑批等待时间（毫秒）', '100'],
  ]
  await page.goto(`${product.origin}/execution-plan-templates`)
  const planCard = page.locator('.lf-interactive-card').filter({
    has: page.getByRole('heading', { name: flow.plan.name, exact: true }),
  })
  await planCard.getByRole('button', { name: '编辑', exact: true }).click()
  const editor = page.locator('.n-drawer:visible')
  await expect(editor.getByLabel(configuration[0][0], { exact: true })).toBeHidden()
  await editor.getByText('合批设置', { exact: true }).click()
  for (const [label, value] of configuration) {
    const field = editor.getByLabel(label, { exact: true })
    await expect(field).toHaveValue('')
    await field.fill(value)
    await field.press('Tab')
  }
  await expect(editor.getByLabel('注音请求并发上限', { exact: true })).toHaveValue('1')
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === 'PUT' &&
      new URL(response.url()).pathname === `/api/v1/execution-plan-templates/${flow.plan.id}`,
  )
  await editor.getByRole('button', { name: '保存', exact: true }).click()
  assert.equal((await saved).status(), 200)
  await expect(editor).toBeHidden()
  await page.reload()
  await planCard.getByRole('button', { name: '编辑', exact: true }).click()
  for (const [label, value] of configuration) {
    await expect(editor.getByLabel(label, { exact: true })).toBeVisible()
    await expect(editor.getByLabel(label, { exact: true })).toHaveValue(value)
  }
  await editor.screenshot({ path: path.join(backend.runDir, 'p2-configuration-readback.png') })
  await editor.getByRole('button', { name: '取消', exact: true }).click()
  const stored = await backend.request('GET', `/execution-plan-templates/${flow.plan.id}`)
  assert.deepEqual(stored.ruby_retry, {
    enabled: true,
    backend_id: flow.alignment.id,
    max_attempts: 2,
    concurrency: 1,
    batch_size: 4,
    max_words_per_batch: 0,
    batch_wait_ms: 100,
  })
  const { project, resource } = await makeRubyProject(backend, 'P2 eight paragraphs', 8)
  backend.holds.add('ruby-product-align')
  const job = await backend.startJob(project, stored)
  const ready = await backend.until(
    () => backend.getJob(job.id),
    (current) =>
      current.progress.stages.main_requests === 0 &&
      current.progress.stages.alignment_requests === 1 &&
      current.progress.stages.pending_alignment === 8,
    'eight durable candidates and one blocked four-member alignment request',
  )
  const alignmentCalls = () => backend.calls.filter((call) => call.model === 'ruby-product-align')
  const mainCalls = () => backend.calls.filter((call) => call.model === 'ruby-product-main')
  assert.equal(mainCalls().length, 2)
  assert.deepEqual(
    mainCalls().map((call) => call.memberCount),
    [4, 4],
  )
  assert.equal(alignmentCalls().length, 1)
  assert.equal(alignmentCalls()[0].memberCount, 4)
  assert.equal(ready.progress.progress_completed, 0)
  const firstMembers = alignmentCalls()[0].members.map((member) => member.workId)
  assert.equal(new Set(firstMembers).size, 4)
  const drawer = await openDetail(page, product, { job, project })
  const stages = drawer.getByRole('region', { name: '阶段详情', exact: true })
  const stageValue = (label) =>
    stages
      .locator('dt')
      .filter({ hasText: new RegExp(`^${label}$`) })
      .locator('+ dd')
  await expect(stageValue('注音请求')).toHaveText(/^1\s*个请求$/)
  await expect(stageValue('待完成注音候选')).toHaveText(/^8\s*个候选段落$/)
  await expect(stageValue('已确认工作量')).toHaveText(/^0\s*段落 × 轮$/)
  await drawer.getByRole('button', { name: '暂停', exact: true }).click()
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('暂停中')
  await expect(drawer.getByRole('button', { name: '恢复', exact: true })).toBeHidden()
  const pausing = await backend.getJob(job.id)
  assert.equal(pausing.status, 'pausing')
  assert.equal(pausing.progress.stages.draining_requests, 1)
  assert.equal(alignmentCalls().length, 1, 'pause cannot dispatch queued candidates')
  await drawer.screenshot({ path: path.join(backend.runDir, 'p2-batch-pausing.png') })
  backend.release('ruby-product-align')
  const paused = await backend.until(
    () => backend.getJob(job.id),
    (current) => current.status === 'paused',
    'the complete received batch drains before pause confirmation',
  )
  assert.equal(paused.progress.stages.draining_requests, 0)
  assert.equal(paused.progress.stages.pending_alignment, 4)
  assert.equal(paused.progress.stages.confirmed_work, 4)
  assert.equal(paused.progress.progress_completed, 4)
  assert.equal(alignmentCalls().length, 1)
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('已暂停')
  await expect(stageValue('待完成注音候选')).toHaveText(/^4\s*个候选段落$/)
  await expect(stageValue('已确认工作量')).toHaveText(/^4\s*段落 × 轮$/)
  await drawer.screenshot({ path: path.join(backend.runDir, 'p2-batch-paused.png') })
  await drawer.getByRole('button', { name: '恢复', exact: true }).click()
  const completed = await backend.terminalJob(job.id)
  assert.equal(completed.status, 'completed', completed.error_message)
  assert.equal(completed.progress.progress_completed, 8)
  assert.equal(completed.progress.stages.confirmed_work, 8)
  assert.equal(mainCalls().length, 2, 'resume reuses all durable main candidates')
  assert.deepEqual(
    alignmentCalls().map((call) => call.memberCount),
    [4, 4],
  )
  const resumedMembers = alignmentCalls()[1].members.map((member) => member.workId)
  assert.equal(new Set([...firstMembers, ...resumedMembers]).size, 8)
  assert.ok(resumedMembers.every((member) => !firstMembers.includes(member)))
  await expect(drawer.locator('.n-drawer-header .n-tag')).toHaveText('已完成')
  await expect(stageValue('已确认工作量')).toHaveText(/^8\s*段落 × 轮$/)
  await drawer.screenshot({ path: path.join(backend.runDir, 'p2-batch-completed.png') })
  const verified = await assertRubyResults(backend, project, resource, 8, 4)
  for (const action of ['pause', 'resume'])
    assert.equal(
      product.requests.filter(
        (request) =>
          request.method === 'POST' && request.path === `/api/v1/jobs/${job.id}/${action}`,
      ).length,
      1,
      `${action} is initiated exactly once through the product`,
    )
  backend.recordObservation('P2 product acceptance', {
    configurationEditedThroughUI: true,
    configurationReadBackAfterReload: stored.ruby_retry,
    mainBatchSize: 4,
    mainConcurrency: 1,
    alignmentConcurrency: 1,
    mainRequests: mainCalls().length,
    actualAlignmentBatchSizes: alignmentCalls().map((call) => call.memberCount),
    readyStages: ready.progress.stages,
    pausingStages: pausing.progress.stages,
    pausedStages: paused.progress.stages,
    completedStages: completed.progress.stages,
    noConfirmedMemberResent: true,
    ...verified,
  })
})
