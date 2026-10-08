import { expect, type Locator, type Page } from '@playwright/test'
import { json, summaryFixture } from './fixtures.ts'

/** A full panel with long names, pending work and retained failures. */
export async function mockTaskHeader(page: Page) {
  const active = Array.from({ length: 18 }, (_, index) => ({
    task_type: 'storage',
    task_id: String(900 + index),
    project_id: 7,
    project_name: `多语言项目任务 ${index + 1} · LongUnbrokenTranslationProjectName20261005`,
    status: index === 0 ? 'needs_action' : 'running',
    storage_kind: 'source_update',
    phase: 'prepared',
    cleanup_status: 'cleanup_pending',
    error_code: '',
    created_at: '2026-10-05T01:00:00Z',
    updated_at: '2026-10-05T02:00:00Z',
    started_at: null,
    next_retry_at: null,
    supported_actions: ['view'],
  }))
  const terminal = ['failed', 'completed'].map((status, index) => ({
    ...active[0],
    task_id: String(950 + index),
    project_name: status === 'failed' ? '保留的失败任务' : '保留的已完成任务',
    status,
  }))
  await page.route('**/api/v1/operations?**', (route) => {
    const state = new URL(route.request().url()).searchParams.get('state')
    return json(route, { items: state === 'terminal' ? terminal : active })
  })
  const counts = { ...summaryFixture.total, running: 17, needs_action: 1 }
  await page.route('**/api/v1/operations/summary**', (route) =>
    json(route, {
      ...summaryFixture,
      total: counts,
      by_type: { ...summaryFixture.by_type, storage: counts },
    }),
  )
}

export async function selectWorkspaceItem(page: Page, editor: boolean) {
  // Mobile cards currently have no selection control. Select at desktop width
  // before narrowing/zooming, as a user with an existing selection would do.
  const checkbox = editor
    ? page.locator('[data-segment-id="711"]').getByRole('checkbox')
    : page.getByRole('button', { name: '查看段落：welcome.txt', exact: true }).getByRole('checkbox')
  await checkbox.click()
  await expect(checkbox).toBeChecked()
  await expect(selectionClear(page)).toBeVisible()
}

export function selectionClear(page: Page) {
  return page.getByRole('button', { name: '取消选择', exact: true }).filter({ visible: true })
}

/** Scroll the actual document pane, rather than mistaking window scroll for it. */
export async function scrollWorkspacePane(page: Page, editor: boolean) {
  const anchor = editor
    ? page.locator('.lf-scroll').filter({ visible: true }).first()
    : page.getByRole('button', { name: '查看段落：welcome.txt', exact: true })
  const geometry = await anchor.evaluate((element) => {
    for (let node: Element | null = element; node; node = node.parentElement) {
      if (
        node instanceof HTMLElement &&
        /auto|scroll/.test(getComputedStyle(node).overflowY) &&
        node.scrollHeight > node.clientHeight
      ) {
        node.scrollTop = node.scrollHeight
        return { top: node.scrollTop, height: node.clientHeight, content: node.scrollHeight }
      }
    }
    return null
  })
  expect(geometry, 'fixture must exercise a real workspace scroll container').not.toBeNull()
  expect(geometry!.top).toBeGreaterThan(0)
  return geometry
}

export async function assertViewportBounds(locator: Locator) {
  await expect(locator).toBeVisible()
  const geometry = await locator.evaluate((element) => {
    const rect = element.getBoundingClientRect()
    return {
      x: rect.x,
      y: rect.y,
      right: rect.right,
      bottom: rect.bottom,
      width: rect.width,
      height: rect.height,
      viewportWidth: innerWidth,
      viewportHeight: innerHeight,
    }
  })
  expect(geometry.x).toBeGreaterThanOrEqual(0)
  expect(geometry.y).toBeGreaterThanOrEqual(0)
  expect(geometry.right).toBeLessThanOrEqual(geometry.viewportWidth)
  expect(geometry.bottom).toBeLessThanOrEqual(geometry.viewportHeight)
  return geometry
}

export async function assertUnobscured(locator: Locator) {
  await assertViewportBounds(locator)
  const hits = await locator.evaluate((element) => {
    const rect = element.getBoundingClientRect()
    return [
      [0.5, 0.5],
      [0.1, 0.1],
      [0.9, 0.1],
      [0.1, 0.9],
      [0.9, 0.9],
    ].map(([x, y]) => {
      const hit = document.elementFromPoint(rect.x + rect.width * x!, rect.y + rect.height * y!)
      return !!hit && element.contains(hit)
    })
  })
  expect(hits, 'visible control/panel must win browser hit testing').toEqual(Array(5).fill(true))
}

export async function assertTaskHeader(page: Page) {
  await expect(page.getByTestId('global-job-tracker')).toHaveCount(1)
  const trigger = page.getByTestId('global-job-tracker-trigger')
  await expect(page.locator('header').getByTestId('global-job-tracker-trigger')).toHaveCount(1)
  const geometry = await assertViewportBounds(trigger)
  expect(geometry.bottom).toBeLessThanOrEqual(64)
  await assertUnobscured(trigger)
  await trigger.click({ trial: true })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  return geometry
}

export async function openAndCheckTaskPanel(page: Page) {
  await assertTaskHeader(page)
  await page.getByTestId('global-job-tracker-trigger').click()
  const panel = page.getByTestId('global-job-tracker-panel')
  await expect(page.getByTestId('global-job-tracker-trigger')).toHaveAttribute(
    'aria-expanded',
    'true',
  )
  const list = page.getByTestId('global-job-tracker-list')
  await expect(list.getByRole('button').filter({ hasText: /多语言项目任务|保留的/ })).toHaveCount(
    20,
  )
  const geometry = await assertViewportBounds(panel)
  await assertUnobscured(panel)
  const scrolling = await list.evaluate((element) => {
    element.scrollTop = element.scrollHeight
    return { top: element.scrollTop, height: element.clientHeight, content: element.scrollHeight }
  })
  expect(scrolling.content).toBeGreaterThan(scrolling.height)
  expect(scrolling.top).toBeGreaterThan(0)
  await expect(list.getByRole('button', { name: /保留的已完成任务/ })).toBeInViewport()
  await panel.getByRole('button', { name: '查看全部', exact: true }).click({ trial: true })
  await assertUnobscured(panel.getByRole('button', { name: '隐藏已结束任务', exact: true }))
  await list.evaluate((element) => {
    element.scrollTop = 0
  })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  return geometry
}
