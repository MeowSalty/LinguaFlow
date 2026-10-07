import { chromium, expect, test, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import type { ApiSchemas } from '../../src/api/client-core'
import { json, mockApp } from './fixtures'

type Policy = ApiSchemas['TaskRetentionPolicy']
type Preview = ApiSchemas['TaskRetentionPreview']
const initialPolicy = (): Policy => ({ enabled: false, retention_days: 30, revision: 1 })
const typePreview = (): ApiSchemas['RetentionTypePreview'] => ({
  active: 2,
  terminal: 10,
  missing_anchor: 1,
  not_expired: 2,
  expired: { blocked: 1, busy: 1, deletable: 5 },
  timing_sources: { finished_at: 8, legacy_anchor: 1, missing_anchor: 1 },
  terminal_age: { lt_1d: 1, days_1_6: 1, days_7_29: 7, days_30_89: 0, days_90_plus: 0, unknown: 1 },
  deletable_dependencies: {
    sse_events: 20,
    job_resources: 5,
    job_rounds: 5,
    job_round_segments: 10,
    credential_job_references: 1,
  },
})
const result = (days: number, revision: number, partial = false): Preview => ({
  policy_revision: revision,
  retention_days: days,
  as_of: '2026-10-06T01:00:00Z',
  cutoff: new Date(Date.parse('2026-10-06T01:00:00Z') - days * 86_400_000).toISOString(),
  partial,
  incomplete_reasons: partial ? ['evaluation_budget_exhausted', 'private_unknown_code'] : [],
  by_type: {
    translation: typePreview(),
    glossary_sync: {
      ...typePreview(),
      expired: { blocked: 1, busy: 1, deletable: partial ? null : 5 },
    },
  },
})

async function arrange(
  page: Page,
  options: { partial?: boolean; theme?: 'dark' | 'light'; enabled?: boolean } = {},
) {
  await mockApp(page, { theme: options.theme })
  const state = {
    policy: { ...initialPolicy(), enabled: options.enabled ?? false },
    registration: true,
    writes: [] as ApiSchemas['UpdateSystemSettingsRequest'][],
    previews: 0,
    reads: 0,
    failRead: false,
    failPreview: false,
    conflictOnce: false,
    statusReads: 0,
  }
  await page.route('**/api/v1/admin/settings', (route) => {
    if (route.request().method() === 'PATCH') {
      const body = route.request().postDataJSON() as ApiSchemas['UpdateSystemSettingsRequest']
      state.writes.push(body)
      if (state.conflictOnce) {
        state.conflictOnce = false
        state.policy = { enabled: true, retention_days: 14, revision: state.policy.revision + 1 }
        return json(
          route,
          { error_code: 'settings_conflict', status: 409, detail: 'private server content' },
          409,
        )
      }
      if (body.settings.registration_enabled !== undefined)
        state.registration = body.settings.registration_enabled
      if (body.settings.task_retention)
        state.policy = {
          enabled: body.settings.task_retention.enabled,
          retention_days: body.settings.task_retention.retention_days,
          revision: state.policy.revision + 1,
        }
    } else {
      state.reads++
      if (state.failRead) return json(route, { status: 503 }, 503)
    }
    return json(route, {
      settings: { registration_enabled: state.registration, task_retention: state.policy },
    })
  })
  await page.route('**/api/v1/admin/task-retention/status', (route) => {
    state.statusReads++
    return json(route, {
      task_retention: state.policy,
      policy_revision: state.policy.revision,
      state: state.policy.enabled ? 'idle' : 'disabled',
      reason_codes: [],
      last_scan: null,
      backlog: null,
    })
  })
  await page.route('**/api/v1/admin/task-retention/preview', (route) => {
    state.previews++
    if (state.failPreview)
      return json(route, { status: 503, detail: 'private server content' }, 503)
    return json(
      route,
      result(route.request().postDataJSON().retention_days, state.policy.revision, options.partial),
    )
  })
  await page.goto('/admin/settings')
  await expect(page.getByRole('spinbutton', { name: '保留天数' })).toHaveValue('30')
  return state
}
const days = (page: Page) => page.getByRole('spinbutton', { name: '保留天数' })
const retentionSwitch = (page: Page) => page.getByRole('switch', { name: '自动清理任务历史' })
const save = (page: Page) => page.getByRole('button', { name: '保存保留策略', exact: true })

test('settings cards submit separate patches and retain the other card draft', async ({ page }) => {
  const state = await arrange(page)
  await days(page).fill('7')
  await page.getByRole('switch', { name: '允许公开注册' }).click()
  await page.getByRole('button', { name: '保存注册设置' }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toEqual({ settings: { registration_enabled: false } })
  await expect(days(page)).toHaveValue('7')
  await page.getByRole('switch', { name: '允许公开注册' }).click()
  await save(page).click()
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1]).toEqual({
    settings: { task_retention: { enabled: false, retention_days: 7, expected_revision: 1 } },
  })
  await expect(page.getByRole('switch', { name: '允许公开注册' })).toBeChecked()
  await expect(page.getByRole('button', { name: '保存注册设置' })).toBeEnabled()
  expect(state.previews).toBe(0)
})

test('partial enablement requires a fresh preview and explicit unknown-impact acknowledgement', async ({
  page,
}) => {
  const state = await arrange(page, { partial: true })
  await days(page).fill('7')
  await page.getByRole('button', { name: '预览影响', exact: true }).click()
  await expect.poll(() => state.previews).toBe(1)
  await retentionSwitch(page).click()
  await save(page).click()
  const modal = page.getByRole('dialog')
  await expect(modal).toBeVisible()
  await expect(modal.getByRole('button', { name: '取消', exact: true })).toBeFocused()
  await expect.poll(() => state.previews).toBe(2)
  await expect(modal.getByRole('button', { name: '确认启用' })).toBeDisabled()
  await expect(modal.getByText('未知', { exact: true })).toBeVisible()
  await expect(page.getByText('private_unknown_code')).toHaveCount(0)
  await modal.getByRole('checkbox', { name: '我了解仍有未完成统计，实际影响可能更多' }).check()
  await modal.getByRole('button', { name: '确认启用' }).click()
  await expect(modal).toHaveCount(0)
  expect(state.writes).toEqual([
    { settings: { task_retention: { enabled: true, retention_days: 7, expected_revision: 1 } } },
  ])
  await expect(page.getByText('保留策略已保存', { exact: true })).toBeVisible()
  await expect(page.getByText('本进程暂无扫描记录', { exact: true })).toBeVisible()
})

test('conflicts retain the draft and require reviewed rebase before a new confirmation', async ({
  page,
}) => {
  const state = await arrange(page)
  state.conflictOnce = true
  await days(page).fill('7')
  await retentionSwitch(page).click()
  await save(page).click()
  await page.getByRole('dialog').getByRole('button', { name: '确认启用' }).click()
  await expect(page.getByText('设置已被其他管理员修改', { exact: true })).toBeVisible()
  await expect(days(page)).toHaveValue('7')
  await expect(save(page)).toBeDisabled()
  expect(state.writes).toHaveLength(1)
  await expect(page.getByText('private server content', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '基于最新设置继续' }).click()
  await save(page).click()
  const modal = page.getByRole('dialog')
  await expect(modal.getByText('14 天 → 7 天', { exact: true })).toBeVisible()
  await modal.getByRole('button', { name: '确认缩短保留期' }).click()
  await expect(modal).toHaveCount(0)
  expect(state.writes[1]?.settings.task_retention?.expected_revision).toBe(2)
})

test('invalid days disable actions, failed preview never enables, disabling needs no preview', async ({
  page,
}) => {
  const state = await arrange(page, { enabled: true })
  await days(page).fill('')
  await expect(save(page)).toBeDisabled()
  await expect(page.getByRole('button', { name: '预览影响', exact: true })).toBeDisabled()
  await expect(page.getByText('请输入 1–3650 的整数天数', { exact: true })).toBeVisible()
  await days(page).fill('7')
  state.failPreview = true
  await save(page).click()
  await expect(page.getByText(/无法获取影响预览/)).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(state.writes).toHaveLength(0)
  await retentionSwitch(page).click()
  await save(page).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]?.settings.task_retention?.enabled).toBe(false)
  expect(state.previews).toBe(1)
})

test('page refresh only discards both drafts after a successful read', async ({ page }) => {
  const state = await arrange(page)
  await days(page).fill('7')
  await page.getByRole('switch', { name: '允许公开注册' }).click()
  state.failRead = true
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '丢弃修改并刷新' }).click()
  await expect(page.getByText('刷新失败，已保留当前草稿和上次确认的政策。')).toBeVisible()
  await expect(days(page)).toHaveValue('7')
  await expect(page.getByRole('switch', { name: '允许公开注册' })).not.toBeChecked()
  state.failRead = false
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '丢弃修改并刷新' }).click()
  await expect(days(page)).toHaveValue('30')
  await expect(page.getByRole('switch', { name: '允许公开注册' })).toBeChecked()
})

test('leaving a dirty settings page requires an explicit discard and cancellation retains both drafts', async ({
  page,
}) => {
  await arrange(page)
  await days(page).fill('7')
  await page.getByRole('switch', { name: '允许公开注册' }).click()
  const menu = page.getByRole('button', { name: '菜单', exact: true })
  const navigate = async () => {
    if (await menu.isVisible()) {
      await menu.click()
      await page.getByRole('button', { name: '工作台', exact: true }).click()
    } else await page.getByRole('link', { name: '工作台', exact: true }).click()
  }
  await navigate()
  const modal = page.getByRole('dialog')
  await expect(modal.getByText('尚有未保存的修改。离开将丢弃这些草稿。')).toBeVisible()
  await modal.getByRole('button', { name: '取消', exact: true }).click()
  await expect(page).toHaveURL(/\/admin\/settings$/)
  await expect(days(page)).toHaveValue('7')
  await expect(page.getByRole('switch', { name: '允许公开注册' })).not.toBeChecked()
  await navigate()
  await modal.getByRole('button', { name: '丢弃修改并离开' }).click()
  await expect(page).toHaveURL(/\/$/)
})

for (const theme of ['light', 'dark'] as const) {
  test(`retention fits 320px in ${theme} theme and cancel restores keyboard focus`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 320, height: 800 })
    await arrange(page, { theme })
    await retentionSwitch(page).click()
    await save(page).click()
    const modal = page.getByRole('dialog')
    await expect(modal.getByRole('button', { name: '取消', exact: true })).toBeFocused()
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    await page.screenshot({
      path: testInfo.outputPath(`retention-confirm-320-${theme}.png`),
      fullPage: false,
    })
    await page.keyboard.press('Escape')
    await expect(modal).toHaveCount(0)
    await expect(save(page)).toBeFocused()
  })
}

test('retention confirmation fits real 200% browser zoom in both themes', async ({
  browserName,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium' || browserName !== 'chromium',
    'Real browser zoom is a desktop browser setting.',
  )
  const extension = testInfo.outputPath('zoom-extension')
  await mkdir(extension, { recursive: true })
  await writeFile(
    `${extension}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'Retention zoom evidence',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
  )
  await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
  for (const theme of ['light', 'dark'] as const) {
    const context = await chromium.launchPersistentContext('', {
      channel: 'chromium',
      headless: true,
      baseURL: 'http://127.0.0.1:4173',
      viewport: { width: 1440, height: 960 },
      deviceScaleFactor: 1,
      reducedMotion: 'reduce',
      args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
    })
    try {
      const worker = context.serviceWorkers()[0] ?? (await context.waitForEvent('serviceworker'))
      const page = await context.newPage()
      await arrange(page, { theme, partial: true })
      const original = await page.evaluate(() => ({ width: innerWidth, ratio: devicePixelRatio }))
      expect(original).toEqual({ width: 1440, ratio: 1 })
      const zoom = await worker.evaluate(async () => {
        const browserApi = (
          globalThis as unknown as {
            chrome: {
              tabs: {
                query(query: { url: string }): Promise<Array<{ id: number }>>
                setZoom(id: number, factor: number): Promise<void>
                getZoom(id: number): Promise<number>
              }
            }
          }
        ).chrome
        const [tab] = await browserApi.tabs.query({ url: 'http://127.0.0.1:4173/*' })
        if (!tab) throw new Error('Retention preview tab was not found')
        await browserApi.tabs.setZoom(tab.id, 2)
        return browserApi.tabs.getZoom(tab.id)
      })
      expect(zoom).toBe(2)
      await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
      await retentionSwitch(page).click()
      await save(page).click()
      const modal = page.getByRole('dialog')
      await expect(modal.getByRole('button', { name: '取消', exact: true })).toBeFocused()
      const bounds = await modal.boundingBox()
      expect(bounds).not.toBeNull()
      expect(bounds!.x).toBeGreaterThanOrEqual(0)
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(720)
      expect(bounds!.y).toBeGreaterThanOrEqual(0)
      expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(await page.evaluate(() => innerHeight))
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(
        true,
      )
      const cdp = await context.newCDPSession(page)
      try {
        const { data } = await cdp.send('Page.captureScreenshot', {
          format: 'png',
          fromSurface: true,
          captureBeyondViewport: false,
        })
        await writeFile(
          testInfo.outputPath(`retention-real-200pct-${theme}.png`),
          Buffer.from(data, 'base64'),
        )
      } finally {
        await cdp.detach()
      }
      await page.keyboard.press('Shift+Tab')
      const acknowledgement = modal.getByRole('checkbox', {
        name: '我了解仍有未完成统计，实际影响可能更多',
      })
      await expect(acknowledgement).toBeFocused()
      await page.keyboard.press('Space')
      await expect(modal.getByRole('button', { name: '确认启用' })).toBeEnabled()
      await modal.getByRole('button', { name: '取消', exact: true }).click()
      await expect(save(page)).toBeFocused()
    } finally {
      await context.close()
    }
  }
})
