import { expect, test, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { emptyCounts, json, mockApp, summaryFixture } from './fixtures'

const longProjectName = '跨页面任务与多语言交付项目-LongUnbrokenTranslationProjectName20261005'
const timestamp = '2026-10-05T00:00:00Z'

function task(
  id: number,
  status: ApiSchemas['StorageTask']['status'] = 'running',
): ApiSchemas['OperationSummary'] {
  return {
    task_id: String(id),
    task_type: 'storage',
    project_id: 7,
    project_name: `${longProjectName} ${id}`,
    status,
    storage_kind: 'source_update',
    phase: 'prepared',
    cleanup_status: 'cleanup_pending',
    error_code: '',
    next_retry_at: null,
    supported_actions: ['view'],
    created_at: timestamp,
    updated_at: timestamp,
    started_at: null,
  }
}

async function mockTasks(
  page: Page,
  options: {
    active?: ApiSchemas['OperationSummary'][]
    terminal?: ApiSchemas['OperationSummary'][]
    counts?: Partial<typeof emptyCounts>
    theme?: 'light' | 'dark'
    mode?: 'local' | 'server'
  } = {},
) {
  await mockApp(page, { theme: options.theme, mode: options.mode })
  const state = {
    active: options.active ?? [],
    terminal: options.terminal ?? [],
    counts: { ...emptyCounts, ...options.counts },
    summaryFailed: false,
    discoveryFailed: false,
    failureStatus: 503,
    summaryReads: 0,
  }
  await page.route('**/api/v1/operations/summary**', (route) => {
    state.summaryReads++
    return state.summaryFailed
      ? json(
          route,
          { title: 'Summary unavailable', status: state.failureStatus },
          state.failureStatus,
        )
      : json(route, {
          ...summaryFixture,
          total: state.counts,
          by_type: { ...summaryFixture.by_type, storage: state.counts },
        })
  })
  await page.route('**/api/v1/operations?**', (route) => {
    const terminal = new URL(route.request().url()).searchParams.get('state') === 'terminal'
    return state.discoveryFailed && !terminal
      ? json(
          route,
          { title: 'Tasks unavailable', status: state.failureStatus },
          state.failureStatus,
        )
      : json(route, { items: terminal ? state.terminal : state.active })
  })
  return state
}

async function seedPreferences(page: Page, preferences: Record<string, unknown>) {
  await page.addInitScript((value) => {
    const key = `linguaflow.preferences.v1:${encodeURIComponent(`${location.origin}/api/v1|1`)}`
    if (!localStorage.getItem(key))
      localStorage.setItem(key, JSON.stringify({ version: 1, ...value }))
  }, preferences)
}

async function expectPanelBounds(page: Page) {
  const trigger = await page.getByTestId('global-job-tracker-trigger').boundingBox()
  const panel = await page.getByTestId('global-job-tracker-panel').boundingBox()
  const viewport = await page.evaluate(() => ({ width: innerWidth, height: innerHeight }))
  expect(panel!.width).toBeLessThanOrEqual(336)
  // Popover transforms may round to fractions of a CSS pixel.
  expect(panel!.x).toBeGreaterThanOrEqual(15.5)
  expect(panel!.x + panel!.width).toBeLessThanOrEqual(viewport.width - 15.5)
  expect(panel!.y).toBeGreaterThanOrEqual(trigger!.y + trigger!.height)
  expect(panel!.y + panel!.height).toBeLessThanOrEqual(viewport.height - 16)
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(viewport.width)
}

test('every public layout has one reachable header entry and browser navigation closes its panel', async ({
  page,
}) => {
  await mockTasks(page)
  for (const path of ['/', '/operations', '/settings/preferences', '/settings/team', '/admin']) {
    await page.goto(path)
    await expect(page.getByTestId('global-job-tracker')).toHaveCount(1)
    const trigger = page.locator('header').getByTestId('global-job-tracker-trigger')
    await expect(trigger).toHaveAccessibleName('当前任务 0')
    await expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
    await expect(trigger).toBeInViewport()
    await trigger.click()
    await expect(page.getByRole('dialog', { name: '当前任务', exact: true })).toBeVisible()
    await page
      .getByTestId('global-job-tracker-panel')
      .getByRole('button', { name: '关闭', exact: true })
      .click()
  }
  await page.goto('/settings/preferences')
  await page.getByTestId('global-job-tracker-trigger').click()
  await page
    .getByTestId('global-job-tracker-panel')
    .getByRole('button', { name: '查看全部', exact: true })
    .click()
  await expect(page).toHaveURL(/\/operations$/)
  await page.getByTestId('global-job-tracker-trigger').click()
  await expect(page.getByTestId('global-job-tracker-panel')).toBeVisible()
  await page.goBack()
  await expect(page).toHaveURL(/\/settings\/preferences$/)
  await expect(page.getByTestId('global-job-tracker-panel')).toBeHidden()
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAttribute(
    'aria-expanded',
    'false',
  )
})

test('keyboard, close, repeated trigger and outside clicks can dismiss the panel', async ({
  page,
}) => {
  await mockTasks(page)
  await page.goto('/settings/preferences')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  const panel = page.getByTestId('global-job-tracker-panel')
  await trigger.focus()
  await page.keyboard.press('Enter')
  await expect(panel).toBeVisible()
  if (
    !(await panel
      .getByRole('button', { name: '关闭', exact: true })
      .evaluate((element) => element === document.activeElement))
  )
    await page.keyboard.press('Tab')
  await expect(panel.getByRole('button', { name: '关闭', exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(trigger).toBeFocused()
  await trigger.click()
  await panel.getByRole('button', { name: '关闭', exact: true }).click()
  await expect(panel).toBeHidden()
  await trigger.click()
  await trigger.click()
  await expect(panel).toBeHidden()
  await trigger.click()
  await page.mouse.click(8, 300)
  await expect(panel).toBeHidden()
})

test('reload ignores legacy expanded state while keeping task range and terminal preferences', async ({
  page,
}) => {
  await mockTasks(page, { terminal: [task(91, 'failed')], counts: { recent_failed: 1 } })
  await seedPreferences(page, {
    trackerExpanded: true,
    defaultTaskState: 'all',
    retainTerminal: false,
    quickTranslatePlanId: 101,
    hiddenTerminalKeys: ['storage:90'],
    selectedOrgId: 9,
  })
  await page.goto('/settings/preferences')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('switch', { name: '保留终态提醒', exact: true })).not.toBeChecked()
  await expect(page.locator('.n-base-selection').filter({ hasText: '全部任务' })).toBeVisible()
  await expect(page.getByTestId('global-job-tracker')).toHaveAttribute('data-attention', 'normal')
  await trigger.click()
  await expect(page.getByTestId('global-job-tracker-list').getByRole('button')).toHaveCount(0)
  await page.reload()
  await expect(trigger).toHaveAttribute('aria-expanded', 'false')
  await page.getByRole('switch', { name: '保留终态提醒', exact: true }).click()
  const stored = await page.evaluate(() => {
    const key = Object.keys(localStorage).find((item) =>
      item.startsWith('linguaflow.preferences.v1:'),
    )!
    return JSON.parse(localStorage.getItem(key)!)
  })
  expect(stored).toMatchObject({
    defaultTaskState: 'all',
    retainTerminal: true,
    quickTranslatePlanId: 101,
    hiddenTerminalKeys: ['storage:90'],
    selectedOrgId: 9,
  })
  expect(stored).not.toHaveProperty('trackerExpanded')
})

for (const theme of ['light', 'dark'] as const) {
  test(`320px ${theme} header caps the badge, preserves the full count and scrolls only the task list`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 320, height: 568 })
    await mockTasks(page, {
      theme,
      mode: 'local',
      active: Array.from({ length: 24 }, (_, index) => task(index + 1)),
      terminal: [task(91, 'failed')],
      counts: { running: 110, pending: 4, paused: 3, needs_action: 2, waiting_retry: 1 },
    })
    await page.goto('/settings/preferences')
    const trigger = page.getByTestId('global-job-tracker-trigger')
    await expect(trigger).toHaveAccessibleName(/^当前任务 120/)
    await expect(trigger).toContainText('99+')
    await expect(page.getByTestId('global-job-tracker')).toHaveAttribute('data-attention', 'failed')
    await trigger.click()
    const panel = page.getByTestId('global-job-tracker-panel')
    const list = page.getByTestId('global-job-tracker-list')
    await expect(list.getByRole('button')).toHaveCount(20)
    await expect(list).not.toContainText(`${longProjectName} 91`)
    await expectPanelBounds(page)
    expect(await list.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(true)
    await list.evaluate((element) => {
      element.scrollTop = element.scrollHeight
    })
    await expect(panel.getByRole('button', { name: '关闭', exact: true })).toBeInViewport()
    await expect(
      panel.getByRole('button', { name: '清除已结束任务', exact: true }),
    ).toBeInViewport()
    await panel.getByRole('button', { name: '清除已结束任务', exact: true }).click()
    await expect(page.getByTestId('global-job-tracker')).toHaveAttribute(
      'data-attention',
      'needs_action',
    )
    await expect(trigger).toHaveAccessibleName(/^当前任务 120/)
    await page.screenshot({
      path: `tests/artifacts/task-header-320-${theme}-${testInfo.project.name}.png`,
    })
    await panel.getByRole('button', { name: '查看全部', exact: true }).click()
    await expect(page).toHaveURL(/\/operations$/)
    await expect(panel).toBeHidden()
  })
}

test('failure reminders take priority and hiding or disabling retained terminals preserves active counts', async ({
  page,
}) => {
  await mockTasks(page, {
    active: [task(1, 'needs_action'), task(2, 'waiting_retry')],
    terminal: [task(91, 'failed'), task(92, 'failed')],
    counts: { needs_action: 3, waiting_retry: 2, recent_failed: 99 },
  })
  await page.goto('/settings/preferences')
  const tracker = page.getByTestId('global-job-tracker')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  const panel = page.getByTestId('global-job-tracker-panel')
  await expect(tracker).toHaveAttribute('data-attention', 'failed')
  await expect(trigger).toHaveAccessibleName(/^当前任务 5/)
  await trigger.click()
  const status = page.getByTestId('global-job-tracker-status')
  await expect(status).toContainText('失败提醒 2')
  await expect(status).toContainText('需要处理 3')
  await expect(status).toContainText('等待自动重试 2')
  await panel.getByRole('button', { name: '隐藏此提醒', exact: true }).first().click()
  await expect(status).toContainText('失败提醒 1')
  await panel.getByRole('button', { name: '清除已结束任务', exact: true }).click()
  await expect(tracker).toHaveAttribute('data-attention', 'needs_action')
  await expect(trigger).toHaveAccessibleName(/^当前任务 5/)
  await expect(status).not.toContainText('失败提醒')
  await panel.getByRole('button', { name: '关闭', exact: true }).click()
  await page.getByRole('button', { name: '恢复已隐藏提醒', exact: true }).click()
  await expect(tracker).toHaveAttribute('data-attention', 'failed')
  await page.getByRole('switch', { name: '保留终态提醒', exact: true }).click()
  await expect(tracker).toHaveAttribute('data-attention', 'needs_action')
  await expect(trigger).toHaveAccessibleName(/^当前任务 5/)
})

for (const attention of ['normal', 'waiting_retry'] as const) {
  test(`${attention} entry ignores recent failure statistics without retained failure reminders`, async ({
    page,
  }) => {
    await mockTasks(page, {
      active: attention === 'waiting_retry' ? [task(1, 'waiting_retry')] : [],
      counts: { waiting_retry: attention === 'waiting_retry' ? 8 : 0, recent_failed: 99 },
    })
    await page.goto('/settings/preferences')
    await expect(page.getByTestId('global-job-tracker')).toHaveAttribute(
      'data-attention',
      attention,
    )
    await page.getByTestId('global-job-tracker-trigger').click()
    if (attention === 'waiting_retry')
      await expect(page.getByTestId('global-job-tracker-status')).toContainText('等待自动重试 8')
    await expect(page.getByTestId('global-job-tracker-panel')).not.toContainText('失败提醒')
  })
}

test('an unresolved summary displays unknown rather than zero', async ({ page }) => {
  await mockTasks(page)
  let release!: () => void
  const pending = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route('**/api/v1/operations/summary**', async (route) => {
    await pending
    return json(route, summaryFixture)
  })
  try {
    await page.goto('/settings/preferences')
    await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName(/^当前任务 —/)
    await page.getByTestId('global-job-tracker-trigger').click()
    await expect(page.getByTestId('global-job-tracker-panel')).not.toContainText('上次成功快照')
  } finally {
    release()
  }
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAccessibleName('当前任务 0')
})

test('first load failures remain unknown and do not claim a prior snapshot', async ({ page }) => {
  const state = await mockTasks(page)
  state.summaryFailed = true
  state.discoveryFailed = true
  await page.goto('/settings/preferences')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  await expect(trigger).toHaveAccessibleName('当前任务 —，任务信息更新失败')
  await expect(page.getByTestId('global-job-tracker')).toHaveAttribute('data-read-state', 'error')
  await trigger.click()
  const panel = page.getByTestId('global-job-tracker-panel')
  await expect(panel).toContainText('暂时无法加载任务，请重试。')
  await expect(panel).not.toContainText('上次成功快照')
})

test('a later polling failure preserves the prior count and explicitly identifies a stale snapshot', async ({
  page,
}) => {
  await page.clock.install()
  const state = await mockTasks(page, { active: [task(1)], counts: { running: 7 } })
  await page.goto('/settings/preferences')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  await expect(trigger).toHaveAccessibleName('当前任务 7')
  await trigger.click()
  await expect(page.getByTestId('global-job-tracker-list')).toContainText(longProjectName)
  state.summaryFailed = true
  state.discoveryFailed = true
  await page.clock.fastForward(12_000)
  await expect(trigger).toHaveAccessibleName('当前任务 7，任务信息更新失败')
  await expect(page.getByTestId('global-job-tracker')).toHaveAttribute('data-read-state', 'stale')
  await expect(page.getByTestId('global-job-tracker-panel')).toContainText(
    '未更新，显示上次成功快照',
  )
  await expect(page.getByTestId('global-job-tracker-list')).toContainText(longProjectName)
  expect(state.summaryReads).toBeGreaterThan(1)
})

test('access denial clears prior tasks and counts without claiming a retained snapshot', async ({
  page,
}) => {
  await page.clock.install()
  const state = await mockTasks(page, { active: [task(1)], counts: { running: 7 } })
  await page.goto('/settings/preferences')
  const tracker = page.getByTestId('global-job-tracker')
  const trigger = page.getByTestId('global-job-tracker-trigger')
  await expect(trigger).toHaveAccessibleName('当前任务 7')
  await trigger.click()
  await expect(page.getByTestId('global-job-tracker-list')).toContainText(longProjectName)
  state.failureStatus = 403
  state.summaryFailed = true
  state.discoveryFailed = true
  await page.clock.fastForward(12_000)
  await expect(trigger).toHaveAccessibleName('当前任务 —，任务信息更新失败')
  await expect(tracker).toHaveAttribute('data-read-state', 'error')
  const panel = page.getByTestId('global-job-tracker-panel')
  await expect(panel).toContainText('暂时无法加载任务，请重试。')
  await expect(panel).not.toContainText(longProjectName)
  await expect(panel).not.toContainText('上次成功快照')
})
