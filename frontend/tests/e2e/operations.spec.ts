import { expect, test, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json, mockApp, summaryFixture } from './fixtures'

function operation(
  task_id: string,
  task_type: 'translation' | 'glossary_sync',
  name: string = task_type,
): ApiSchemas['OperationSummary'] {
  const base = {
    task_id,
    project_id: 7,
    project_name: name,
    status: 'running' as const,
    created_at: '2026-09-30T00:00:00Z',
    updated_at: '2026-09-30T00:01:00Z',
    started_at: null,
    supported_actions: ['view', 'cancel'] as ('view' | 'cancel')[],
  }
  return task_type === 'translation'
    ? {
        ...base,
        task_type,
        trigger_type: 'manual',
        progress: {
          total_resources: 2,
          completed_resources: 1,
          failed_resources: 0,
          progress_total: 100,
          progress_completed: 25,
          queue_position: null,
          queue_size: null,
        },
      }
    : { ...base, task_type, progress: { processed_segments: 15, total_segments: 80 } }
}

async function mockSync(page: Page, orgRole?: 'owner' | 'admin' | 'member') {
  await mockApp(page)
  const state = {
    status: 'running' as ApiSchemas['GlossarySyncTaskStatusResponse']['status'],
    forbidden: false,
    cancelCount: 0,
    gets: 0,
  }
  await page.route('**/api/v1/projects/7', (route) =>
    json(route, {
      id: 7,
      name: 'Sync project',
      source_lang: 'en',
      target_lang: 'zh',
      glossary_enabled: true,
      ...(orgRole ? { owner_org_id: 7 } : { owner_user_id: 1 }),
    }),
  )
  if (orgRole)
    await page.route('**/api/v1/orgs', (route) =>
      json(route, { items: [{ id: 7, name: 'team', slug: 'team', current_user_role: orgRole }] }),
    )
  await page.route('**/api/v1/projects/7/sync-tasks/42', (route) => {
    state.gets++
    return state.forbidden
      ? json(route, { title: 'forbidden', status: 403 }, 403)
      : json(route, { task_id: '42', status: state.status, processed: 15, total: 80 })
  })
  await page.route('**/api/v1/projects/7/sync-tasks/42/cancel', (route) => {
    state.cancelCount++
    state.status = 'completed'
    return json(route, { title: 'task already finished', status: 409 }, 409)
  })
  return state
}

test('a fresh browser discovers both task types across every active page', async ({ page }) => {
  await mockApp(page)
  const cursors: (string | null)[] = []
  await page.route('**/api/v1/operations?**', (route) => {
    const url = new URL(route.request().url())
    if (url.searchParams.get('state') !== 'active') return json(route, { items: [] })
    cursors.push(url.searchParams.get('cursor'))
    return url.searchParams.has('cursor')
      ? json(route, { items: [operation('42', 'glossary_sync', 'Discovered sync')] })
      : json(route, {
          items: [operation('42', 'translation', 'Discovered translation')],
          next_cursor: 'next-active-page',
        })
  })
  await page.goto('/settings/preferences')
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await expect(page.getByRole('button', { name: /Discovered translation/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /Discovered sync/ })).toBeVisible()
  expect(cursors).toEqual([null, 'next-active-page'])
  await expect(page.getByRole('button', { name: '查看全部', exact: true })).toBeVisible()
})

test('task center preserves colliding task identifiers and explicitly loads more', async ({
  page,
}, testInfo) => {
  await mockApp(page)
  await page.route('**/api/v1/operations?**', (route) => {
    const url = new URL(route.request().url())
    if (url.searchParams.get('state') === 'terminal') return json(route, { items: [] })
    if (url.searchParams.get('limit') === '100')
      return json(route, {
        items: [operation('42', 'translation'), operation('42', 'glossary_sync')],
      })
    return json(
      route,
      url.searchParams.has('cursor')
        ? { items: [operation('43', 'translation', 'Second page')] }
        : {
            items: [
              operation('42', 'translation', 'Translation project'),
              operation('42', 'glossary_sync', 'Glossary project'),
            ],
            next_cursor: 'list-page-two',
          },
    )
  })
  await page.goto('/operations')
  await expect(page.getByText('Translation project', { exact: true })).toBeVisible()
  await expect(page.getByText('Glossary project', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '查看详情', exact: true })).toHaveCount(2)
  await page.getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(page.getByText('Second page', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '查看详情', exact: true })).toHaveCount(3)
  await page.screenshot({
    path: `tests/artifacts/operations-${testInfo.project.name}.png`,
    fullPage: true,
  })
})

test('sync deep link loads directly and closing keeps list filters', async ({ page }) => {
  const state = await mockSync(page)
  await page.goto('/operations?task_type=glossary_sync&task_id=42&project_id=7&state=all')
  await expect(page.getByText('术语同步任务 #42', { exact: true })).toBeVisible()
  await expect(page.getByText('已处理 15 / 80 段落', { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByText('已处理 15 / 80 段落', { exact: true })).toBeVisible()
  expect(state.gets).toBeGreaterThanOrEqual(2)
  await page.locator('.n-drawer').getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page).not.toHaveURL(/task_id=/)
  const url = new URL(page.url())
  expect(url.searchParams.get('task_type')).toBe('glossary_sync')
  expect(url.searchParams.get('state')).toBe('all')
  expect(url.searchParams.get('project_id')).toBe('7')
})

test('sync member detail has no cancel and loses sensitive content after 403', async ({ page }) => {
  const state = await mockSync(page, 'member')
  await page.goto('/operations?task_type=glossary_sync&task_id=42&project_id=7')
  await expect(page.getByText('已处理 15 / 80 段落', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '取消任务', exact: true })).toHaveCount(0)
  state.forbidden = true
  await page.locator('.n-drawer').getByRole('button', { name: '刷新', exact: true }).click()
  await expect(page.getByText('任务或项目已不可访问', { exact: true })).toBeVisible()
  await expect(page.getByText('已处理 15 / 80 段落', { exact: true })).toHaveCount(0)
})

test('sync cancel conflict refreshes status without replaying the mutation', async ({ page }) => {
  const state = await mockSync(page)
  await page.goto('/operations?task_type=glossary_sync&task_id=42&project_id=7')
  await page.getByRole('button', { name: '取消任务', exact: true }).click()
  await expect(page.getByText('任务状态已改变，已刷新最新状态。', { exact: true })).toBeVisible()
  await expect(page.locator('.n-drawer').getByText('已完成', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '取消任务', exact: true })).toHaveCount(0)
  expect(state.cancelCount).toBe(1)
})

test('legacy translation deep link queries detail independently of the list', async ({ page }) => {
  await mockApp(page)
  let queried = 0
  await page.route('**/api/v1/jobs/42', (route) => {
    queried++
    return json(route, {
      id: 42,
      project_id: 7,
      execution_plan_id: 1,
      status: 'completed',
      trigger_type: 'manual',
      progress: {
        total_resources: 2,
        completed_resources: 2,
        failed_resources: 0,
        progress_total: 100,
        progress_completed: 100,
      },
      job_resources: [],
      created_at: '2026-09-30T00:00:00Z',
      updated_at: '2026-09-30T00:01:00Z',
    })
  })
  await page.goto('/operations?job_id=42&state=terminal')
  await expect(page.locator('.n-drawer')).toBeVisible()
  await expect(page.locator('.n-drawer').getByText('已完成', { exact: true }).first()).toBeVisible()
  expect(queried).toBeGreaterThan(0)
  await page.locator('.n-drawer').getByRole('button', { name: '关闭', exact: true }).click()
  await expect(page).not.toHaveURL(/job_id=/)
  expect(new URL(page.url()).searchParams.get('state')).toBe('terminal')
})

test('invalid URL filters are rejected; summaries receive only supported dimensions', async ({
  page,
}) => {
  await mockApp(page)
  await page.goto('/operations?state=active&status=failed')
  await expect(
    page.getByText('链接参数无效或不完整，请检查任务类型、项目和任务编号。'),
  ).toBeVisible()
  let captured: URL | null = null
  await page.route('**/api/v1/operations/summary?**', (route) => {
    captured = new URL(route.request().url())
    return json(route, summaryFixture)
  })
  await page.goto(
    '/operations?task_type=translation&project_id=7&trigger_type=manual&status=failed&updated_from=2026-09-29T00:00:00.123456789Z',
  )
  await expect.poll(() => captured?.searchParams.get('project_id')).toBe('7')
  expect([...captured!.searchParams.keys()].sort()).toEqual([
    'project_id',
    'task_type',
    'trigger_type',
  ])
})

test('a temporary list error keeps the prior successful page visibly stale', async ({ page }) => {
  await mockApp(page)
  let fail = false
  await page.route('**/api/v1/operations?**', (route) => {
    const url = new URL(route.request().url())
    if (url.searchParams.get('limit') !== '50') return json(route, { items: [] })
    return fail
      ? json(route, { title: 'temporary list failure', status: 503 }, 503)
      : json(route, { items: [operation('42', 'translation', 'Saved page')] })
  })
  await page.goto('/operations')
  await expect(page.getByText('Saved page', { exact: true })).toBeVisible()
  fail = true
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(page.getByText(/未更新，显示上次成功快照 · temporary list failure/)).toBeVisible()
  await expect(page.getByText('Saved page', { exact: true })).toBeVisible()
})
