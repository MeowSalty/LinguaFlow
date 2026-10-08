import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'
import { storageRuntime } from '../storage-fixtures'

const timestamp = '2026-10-04T00:00:00Z'
const project = {
  id: 7,
  name: 'Fixed exports',
  owner_user_id: 1,
  owner_org_id: null,
  source_lang: 'en',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
  storage_space_id: 1,
  storage_generation: 0,
  output_generation: 0,
  storage_state: 'active',
  created_at: timestamp,
  updated_at: timestamp,
}
const resource = {
  id: 71,
  name: 'welcome.txt',
  path: 'welcome.txt',
  directory: '',
  format: 'txt',
  total_segments: 1,
  translated_segments: 1,
  approved_segments: 0,
  current_source_revision_id: 1,
  source_generation: 0,
  translation_generation: 9,
  created_at: timestamp,
  updated_at: timestamp,
}
const artifact = (id: number) => ({
  id,
  source_revision_id: 1,
  status: 'ready',
  rebuildable: true,
  renderer_version: '1',
  filename: `snapshot-${id}.txt`,
  deletion_task_id: null as number | null,
})
const task = {
  id: 42,
  operation_id: 'fixed-export',
  kind: 'export',
  project_id: 7,
  resource_id: 71,
  status: 'completed',
  phase: 'committed',
  cleanup_status: 'done',
  allowed_actions: [],
  result_artifact_id: 6,
  expires_at: null,
  created_at: timestamp,
  updated_at: timestamp,
}

async function setup(page: Page) {
  await mockApp(page, { role: 'user' })
  const state = {
    keys: [] as string[],
    posts: [] as string[],
    items: [artifact(5)],
    deletes: 0,
    deletedReads: 0,
    projectReads: 0,
    storageState: 'active',
  }
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname.replace('/api/v1', '')
    if (path === '/projects') return json(route, { items: [project] })
    if (path === '/projects/7') {
      state.projectReads++
      return json(route, { ...project, storage_state: state.storageState })
    }
    if (path === '/projects/7/resources/tree')
      return json(route, {
        root: {
          type: 'directory',
          name: '',
          path: '',
          children: [{ type: 'resource', name: resource.name, path: resource.path, resource }],
        },
      })
    if (path === '/projects/7/resources') return json(route, { items: [resource] })
    if (path === '/projects/7/storage')
      return json(route, {
        runtime: storageRuntime(),
        project_id: 7,
        storage_generation: 0,
        storage_state: 'active',
        migration_task_id: null,
        binding: null,
        reason_codes: [],
      })
    if (path.endsWith('/versions')) return json(route, { items: [] })
    if (path === '/projects/7/resources/71/exports' && request.method() === 'GET') {
      if (url.searchParams.get('include_deleted') === 'true') state.deletedReads++
      return json(route, { items: state.items })
    }
    if (
      (path === '/projects/7/resources/71/exports' || path.endsWith('/rebuild')) &&
      request.method() === 'POST'
    ) {
      state.keys.push(request.headers()['idempotency-key'] ?? '')
      state.posts.push(path)
      if (state.keys.length === 1) return route.abort('connectionreset')
      state.items = [artifact(5), artifact(6)]
      return json(route, task)
    }
    if (path === '/projects/7/storage/tasks/42') return json(route, task)
    if (path === '/projects/7/exports/5' && request.method() === 'DELETE') {
      state.deletes++
      state.items = [{ ...artifact(5), status: 'deleted', deletion_task_id: 43 }]
      return route.fulfill({ status: 204 })
    }
    if (path === '/projects/7/storage/tasks/43')
      return json(route, {
        ...task,
        id: 43,
        kind: 'export_delete',
        result_artifact_id: 5,
        cleanup_status: 'blocked',
      })
    return route.fallback()
  })
  return state
}

async function openExports(page: Page) {
  await page.goto('/projects/7')
  await page
    .getByRole('button', { name: '查看段落：welcome.txt', exact: true })
    .locator('button')
    .click()
  await page.getByText('源文件与固定交付', { exact: true }).click()
  await page.locator('.n-drawer:visible').getByText('固定交付', { exact: true }).click()
}

for (const kind of ['create', 'rebuild'] as const) {
  test(`ordinary owner recovers lost ${kind} response through the original key and confirms the new ready artifact`, async ({
    page,
  }) => {
    const state = await setup(page)
    await openExports(page)
    const drawer = page.locator('.n-drawer:visible')
    if (kind === 'create')
      await drawer.getByRole('button', { name: '保存当前译文为交付', exact: true }).click()
    else {
      await drawer.getByRole('button', { name: '使用旧快照重建', exact: true }).click()
      await page
        .locator('.n-dialog:visible')
        .getByRole('button', { name: '创建', exact: true })
        .click()
    }
    await expect(drawer.getByRole('button', { name: '恢复原操作', exact: true })).toBeVisible()
    await expect(
      drawer.getByRole('button', { name: '保存当前译文为交付', exact: true }),
    ).toBeDisabled()
    await drawer.getByRole('button', { name: '恢复原操作', exact: true }).click()
    await expect(drawer.getByText('固定交付已发布', { exact: true })).toBeVisible()
    await expect(drawer.getByText('snapshot-6.txt', { exact: true })).toBeVisible()
    expect(state.keys).toHaveLength(2)
    expect(state.keys[0]).toBeTruthy()
    expect(state.keys[1]).toBe(state.keys[0])
    expect(state.posts[1]).toBe(state.posts[0])
  })
}

test('accepted export deletion discovers its tombstone and reports blocked cleanup separately', async ({
  page,
}) => {
  const state = await setup(page)
  await openExports(page)
  const drawer = page.locator('.n-drawer:visible')
  await drawer.getByRole('button', { name: '删除', exact: true }).click()
  await page.locator('.n-dialog:visible').getByRole('button', { name: '删除', exact: true }).click()
  await expect(drawer.getByText('清理受阻，仍计入占用', { exact: true })).toBeVisible()
  await expect(drawer.getByRole('button', { name: '查看任务 #43', exact: true })).toBeVisible()
  await expect(drawer.getByRole('button', { name: '下载', exact: true })).toBeDisabled()
  expect(state.deletes).toBe(1)
  expect(state.deletedReads).toBeGreaterThan(1)
})

test('a delete confirmation opened before maintenance cannot submit a stale mutation', async ({
  page,
}) => {
  const state = await setup(page)
  await openExports(page)
  const drawer = page.locator('.n-drawer:visible')
  await drawer.getByRole('button', { name: '删除', exact: true }).click()
  state.storageState = 'migrating'
  const previousReads = state.projectReads
  await page.locator('.n-dialog:visible').getByRole('button', { name: '删除', exact: true }).click()
  await expect.poll(() => state.projectReads).toBeGreaterThan(previousReads)
  await expect(
    drawer.getByRole('button', { name: '保存当前译文为交付', exact: true }),
  ).toBeDisabled()
  expect(state.deletes).toBe(0)
})
