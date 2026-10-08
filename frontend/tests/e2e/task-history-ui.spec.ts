import { expect, test, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json, mockApp } from './fixtures'

type Kind = 'translation' | 'glossary_sync'
function operation(
  id: string,
  kind: Kind = 'translation',
  canDelete = true,
): ApiSchemas['OperationSummary'] {
  const base = {
    task_id: id,
    project_id: 7,
    project_name: `${kind} project ${id}`,
    status: 'completed' as const,
    can_delete: canDelete,
    finished_at: null,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:01:00Z',
    started_at: null,
    supported_actions: ['view'] as 'view'[],
  }
  return kind === 'glossary_sync'
    ? { ...base, task_type: kind, progress: { processed_segments: 2, total_segments: 2 } }
    : {
        ...base,
        task_type: kind,
        trigger_type: 'manual',
        progress: {
          total_resources: 1,
          completed_resources: 1,
          failed_resources: 0,
          progress_total: 2,
          progress_completed: 2,
          queue_position: null,
          queue_size: null,
        },
      }
}
function job(id: number, canDelete: boolean): ApiSchemas['Job'] {
  return {
    id,
    project_id: 7,
    status: 'completed',
    can_delete: canDelete,
    finished_at: null,
    trigger_type: 'manual',
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:01:00Z',
    execution_config: {},
    resources: [],
    progress: {
      total_resources: 1,
      completed_resources: 1,
      failed_resources: 0,
      progress_total: 2,
      progress_completed: 2,
    },
  }
}
async function historyApp(page: Page, records = [operation('42')]) {
  await mockApp(page)
  const state = {
    records,
    deleted: new Set<string>(),
    deleteCount: 0,
    denyDelete: false,
    missing: false,
  }
  await page.route('**/api/v1/operations?**', (route) => {
    const url = new URL(route.request().url())
    if (url.searchParams.get('state') === 'active') return json(route, { items: [] })
    const items = state.records.filter(
      (item) => !state.deleted.has(`${item.task_type}:${item.task_id}`),
    )
    const offset = Number(url.searchParams.get('cursor') ?? 0)
    const limit = Number(url.searchParams.get('limit') ?? 50)
    return json(route, {
      items: items.slice(offset, offset + limit),
      next_cursor: offset + limit < items.length ? String(offset + limit) : undefined,
    })
  })
  await page.route('**/api/v1/projects/7', (route) =>
    json(route, {
      id: 7,
      name: 'History project',
      source_lang: 'en',
      target_lang: 'zh',
      owner_user_id: 1,
    }),
  )
  await page.route('**/api/v1/jobs/*', (route) => {
    const id = new URL(route.request().url()).pathname.split('/').at(-1)!
    if (!/^\d+$/.test(id)) return json(route, { items: [] })
    if (route.request().method() === 'DELETE') {
      state.deleteCount++
      if (state.denyDelete) return json(route, { title: 'forbidden', status: 403 }, 403)
      state.deleted.add(`translation:${id}`)
      return route.fulfill({ status: 204 })
    }
    if (state.missing || state.deleted.has(`translation:${id}`))
      return json(route, { title: 'not_found', status: 404 }, 404)
    return json(route, job(Number(id), !state.denyDelete))
  })
  await page.route('**/api/v1/jobs/*/events?**', (route) => json(route, { items: [] }))
  await page.route('**/api/v1/projects/7/sync-tasks/*', (route) => {
    const id = new URL(route.request().url()).pathname.split('/').at(-1)!
    if (state.missing || state.deleted.has(`glossary_sync:${id}`))
      return json(route, { title: 'not_found', status: 404 }, 404)
    return json(route, {
      task_id: id,
      status: 'completed',
      processed: 2,
      total: 2,
      result: null,
      can_delete: true,
      finished_at: null,
    })
  })
  return state
}
const main = (page: Page) => page.locator('main')
const modal = (page: Page) => page.locator('.n-modal')
async function openSingleDelete(page: Page) {
  await main(page).getByRole('button', { name: '更多任务操作' }).first().click()
  await page.getByText('删除任务记录', { exact: true }).click()
  await expect(modal(page)).toBeVisible()
}

test('delete confirmation starts on cancel and cancelling sends no deletion', async ({ page }) => {
  const state = await historyApp(page)
  await page.goto('/operations?state=terminal')
  const trigger = main(page).getByRole('button', { name: '更多任务操作' }).first()
  await trigger.focus()
  await trigger.press('Enter')
  await expect(page.getByText('删除任务记录', { exact: true })).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(modal(page)).toBeVisible()
  await expect(modal(page).getByRole('button', { name: '取消', exact: true })).toBeFocused()
  await expect(modal(page).getByText(/项目原文和译文会保留/)).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(modal(page)).not.toBeVisible()
  await expect(main(page).getByRole('button', { name: '更多任务操作' }).first()).toBeFocused()
  expect(state.deleteCount).toBe(0)
  await expect(main(page).getByText('translation project 42', { exact: true })).toBeVisible()
})

test('single 204 deletes the record and keeps the operations filter', async ({ page }) => {
  const state = await historyApp(page)
  await page.goto('/operations?state=terminal')
  await openSingleDelete(page)
  await modal(page).getByRole('button', { name: '删除 1 条记录', exact: true }).click()
  await expect(modal(page).getByText('已删除 1 条，0 条已不存在，0 条未完成')).toBeVisible()
  expect(state.deleteCount).toBe(1)
  await modal(page).getByRole('button', { name: '关闭', exact: true }).click()
  await expect(main(page).getByText('translation project 42', { exact: true })).not.toBeVisible()
  await expect(page).toHaveURL(/state=terminal/)
  await expect(main(page).getByText('已删除 1 条，0 条已不存在，0 条未完成')).toBeVisible()
})

test('mixed batch results match types with the same ID and remain reviewable', async ({ page }) => {
  const state = await historyApp(page, [
    operation('42'),
    operation('42', 'glossary_sync'),
    operation('43'),
  ])
  let body: { items: ApiSchemas['TaskHistoryTarget'][] } | undefined
  await page.route('**/api/v1/operations/batch-delete', (route) => {
    body = route.request().postDataJSON()
    state.deleted.add('translation:42')
    state.deleted.add('glossary_sync:42')
    return json(route, {
      items: [
        { kind: 'translation', id: '43', project_id: 7, status: 'busy' },
        { kind: 'glossary_sync', id: '42', project_id: 7, status: 'not_found' },
        { kind: 'translation', id: '42', project_id: 7, status: 'deleted' },
      ],
    })
  })
  await page.goto('/operations?state=terminal')
  await main(page).getByRole('button', { name: '选择记录', exact: true }).click()
  await main(page).getByRole('checkbox', { name: '选择已加载的可删除记录', exact: true }).check()
  await expect(main(page).getByText('已选择 3 / 100 条')).toBeVisible()
  await main(page).getByRole('button', { name: '删除所选记录', exact: true }).click()
  await modal(page).getByRole('button', { name: '删除 3 条记录', exact: true }).click()
  await expect(modal(page).getByText('已删除 1 条，1 条已不存在，1 条未完成')).toBeVisible()
  await expect(modal(page).getByText('任务仍在收尾，请稍后刷新')).toBeVisible()
  expect(body?.items).toHaveLength(3)
  expect(
    body?.items
      .filter((item) => item.id === '42')
      .map((item) => item.kind)
      .sort(),
  ).toEqual(['glossary_sync', 'translation'])
  await modal(page).getByRole('button', { name: '关闭', exact: true }).click()
  await expect(main(page).getByText('translation project 43', { exact: true })).toBeVisible()
  await expect(main(page).getByText('已删除 1 条，1 条已不存在，1 条未完成')).toBeVisible()
})

test('selection spans loaded pages, stops at 100 and clears on refresh', async ({ page }) => {
  await historyApp(
    page,
    Array.from({ length: 101 }, (_, index) => operation(String(index + 1))),
  )
  await page.goto('/operations?state=terminal')
  await main(page).getByRole('button', { name: '选择记录', exact: true }).click()
  await main(page).getByRole('checkbox', { name: '选择已加载的可删除记录', exact: true }).check()
  await expect(main(page).getByText('已选择 50 / 100 条')).toBeVisible()
  await main(page).getByRole('button', { name: '加载更多', exact: true }).click()
  await main(page).getByRole('checkbox', { name: '选择已加载的可删除记录', exact: true }).check()
  await expect(main(page).getByText('已选择 100 / 100 条')).toBeVisible()
  await main(page).getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(main(page).getByRole('checkbox', { name: /#101$/ })).toBeDisabled()
  await main(page).getByRole('button', { name: '刷新', exact: true }).click()
  await expect(main(page).getByText('已选择 0 / 100 条')).toBeVisible()
})

test('unknown finish time is explicit and deletion clears the matching URL only', async ({
  page,
}) => {
  await historyApp(page)
  await page.goto('/operations?state=terminal&task_type=translation&task_id=42&project_id=7')
  await expect(page.getByText('结束时间未知', { exact: true })).toBeVisible()
  await page.locator('.n-drawer').getByRole('button', { name: '更多任务操作' }).click()
  await page.getByText('删除任务记录', { exact: true }).click()
  await modal(page).getByRole('button', { name: '删除 1 条记录', exact: true }).click()
  await expect(modal(page).getByText('已删除 1 条，0 条已不存在，0 条未完成')).toBeVisible()
  await expect(page).not.toHaveURL(/task_id|job_id/)
  await expect(page).toHaveURL(/state=terminal/)
  await expect(page).toHaveURL(/project_id=7/)
})

test('external sync disappearance preserves a missing-record state and project link', async ({
  page,
}) => {
  const state = await historyApp(page, [operation('42', 'glossary_sync')])
  state.missing = true
  await page.goto('/operations?state=terminal&task_type=glossary_sync&task_id=42&project_id=7')
  await expect(page.locator('.n-drawer').getByText('记录不存在，可能已被删除')).toBeVisible()
  await expect(page.locator('.n-drawer').getByRole('button', { name: '返回项目' })).toBeVisible()
  await expect(page.locator('.n-drawer').getByRole('button', { name: '更多任务操作' })).toHaveCount(
    0,
  )
})

test('delete forbidden keeps readable detail and removes its stale delete capability', async ({
  page,
}) => {
  const state = await historyApp(page)
  await page.route('**/api/v1/jobs/42', (route) => {
    if (route.request().method() === 'DELETE') {
      state.deleteCount++
      state.denyDelete = true
      return json(route, { title: 'forbidden', status: 403 }, 403)
    }
    return json(route, job(42, !state.denyDelete))
  })
  await page.goto('/operations?state=terminal&task_type=translation&task_id=42&project_id=7')
  await expect(page.getByText('结束时间未知', { exact: true })).toBeVisible()
  await page.locator('.n-drawer').getByRole('button', { name: '更多任务操作' }).click()
  await page.getByText('删除任务记录', { exact: true }).click()
  await modal(page).getByRole('button', { name: '删除 1 条记录', exact: true }).click()
  await expect(modal(page).getByText('无删除权限', { exact: true })).toBeVisible()
  await modal(page).getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page.getByText('结束时间未知', { exact: true })).toBeVisible()
  await page.locator('.n-drawer').getByRole('button', { name: '更多任务操作' }).click()
  await expect(
    page.getByText('删除任务记录 · 当前不可删除，可能受权限或任务状态限制', { exact: true }),
  ).toBeVisible()
  expect(state.deleteCount).toBe(1)
  await expect(page).toHaveURL(/task_id=42/)
})

test('320px confirmation and selection have no horizontal overflow', async ({ page }) => {
  await page.setViewportSize({ width: 320, height: 740 })
  await historyApp(page)
  await page.goto('/operations?state=terminal')
  await openSingleDelete(page)
  await expect(
    modal(page).getByRole('button', { name: '删除 1 条记录', exact: true }),
  ).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
  const bounds = await modal(page).boundingBox()
  expect(bounds!.x).toBeGreaterThanOrEqual(0)
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(320)
})

test('a nondeletable record explains its restriction within a narrow viewport', async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 740 })
  const state = await historyApp(page, [operation('42', 'translation', false)])
  await page.goto('/operations?state=terminal')
  await main(page).getByRole('button', { name: '更多任务操作' }).click()
  const explanation = page.getByText('删除任务记录 · 当前不可删除，可能受权限或任务状态限制', {
    exact: true,
  })
  await expect(explanation).toBeVisible()
  await expect(explanation).toBeInViewport({ ratio: 1 })
  const bounds = await explanation.boundingBox()
  expect(bounds!.x).toBeGreaterThanOrEqual(0)
  expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(320)
  await explanation.click({ force: true })
  await expect(modal(page)).not.toBeVisible()
  expect(state.deleteCount).toBe(0)
})
