import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'

const project = {
  id: 7,
  name: 'Storage project',
  owner_user_id: 1,
  owner_org_id: null as number | null,
  source_lang: 'en',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
  storage_space_id: 1,
  storage_generation: 0,
  output_generation: 0,
  storage_state: 'active',
  created_at: '2026-10-04T00:00:00Z',
  updated_at: '2026-10-04T00:00:00Z',
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
}
const version = {
  id: 3,
  current: false,
  verification_state: 'verified',
  format: 'txt',
  parser_version: '1',
  location_generation: 0,
  health: 'missing',
  size: 3,
}
const options = {
  scope: 'user',
  owner_id: 1,
  policy: { mode: 'both', default_choice: 'user', generation: 0 },
  default_space_id: null,
  default_unavailable_reason: 'selection_required',
  storage_generation: 0,
  items: [{ space_id: 9, name: '合法目标', scope: 'user', selectable: true, reason_codes: [] }],
}
const task = {
  id: 42,
  operation_id: 'original',
  kind: 'repair',
  status: 'pending',
  phase: 'accepted',
  cleanup_status: 'done',
  allowed_actions: ['upload_content'],
  project_id: 7,
  resource_id: 71,
  source_revision_id: 3,
  target_space_id: 9,
  expected_storage_generation: 0,
  expected_location_generation: 0,
  input_size: 3,
  created_at: project.created_at,
  updated_at: project.updated_at,
  expires_at: null,
}
async function setup(page: Page, member = false) {
  await mockApp(page, { role: 'user' })
  const writes: { path: string; body: unknown }[] = [],
    reads: string[] = []
  const current = { ...project, owner_org_id: member ? 8 : null }
  let migrated = false,
    repaired = false
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname.replace('/api/v1', '')
    if (request.method() === 'GET') reads.push(path + url.search)
    else
      writes.push({
        path,
        body: request.headers()['content-type']?.includes('json')
          ? request.postDataJSON()
          : request.postDataBuffer()?.toString(),
      })
    if (path === '/orgs')
      return json(route, {
        items: member
          ? [{ id: 8, name: 'Member org', slug: 'member', current_user_role: 'member' }]
          : [],
      })
    if (path === '/projects' && request.method() === 'POST')
      return json(route, { ...current, ...request.postDataJSON() }, 201)
    if (path === '/projects' || path === '/orgs/8/projects')
      return json(route, { items: [current] })
    if (path === '/projects/7') return json(route, current)
    if (path.endsWith('/storage/options') || path === '/storage/options')
      return json(route, options)
    if (path === '/projects/7/storage')
      return json(route, {
        project_id: 7,
        storage_generation: 0,
        storage_state: migrated ? 'migrating' : 'active',
        migration_task_id: migrated ? 42 : null,
        binding: {
          space_id: 1,
          name: '只读旧空间',
          scope: member ? 'org' : 'user',
          historical: true,
        },
        reason_codes: [],
      })
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
    if (path.endsWith('/versions'))
      return json(route, { items: [{ ...version, health: repaired ? 'available' : 'missing' }] })
    if (path === '/projects/7/storage/tasks' && request.method() === 'POST')
      return json(route, task, 202)
    if (path === '/projects/7/storage/tasks/42/content') {
      repaired = true
      return json(route, {
        ...task,
        status: 'completed',
        phase: 'committed',
        result_resource_id: 71,
        result_revision_id: 3,
        allowed_actions: [],
      })
    }
    if (path === '/projects/7/storage/migrations') {
      migrated = true
      return json(route, { ...task, kind: 'migration', phase: 'copy' }, 202)
    }
    if (path === '/projects/7/storage/tasks/42')
      return json(
        route,
        migrated ? { ...task, kind: 'migration', phase: 'cutover', allowed_actions: [] } : task,
      )
    return route.fallback()
  })
  return { reads, writes }
}
async function chooseTarget(page: Page) {
  await page.getByLabel('选择存储空间', { exact: true }).click()
  await page.getByText('合法目标 · 个人', { exact: true }).click()
}
test('ordinary owner explicitly selects a discovered target when creating a project', async ({
  page,
}) => {
  const { reads, writes } = await setup(page)
  await page.goto('/projects')
  await page.getByRole('button', { name: '新建项目', exact: true }).click()
  await page.getByPlaceholder('例如：LinguaFlow 本地化').fill('New storage project')
  await expect(page.getByRole('button', { name: '创建项目', exact: true })).toBeDisabled()
  await chooseTarget(page)
  await page.getByRole('button', { name: '创建项目', exact: true }).click()
  await expect.poll(() => writes.filter((item) => item.path === '/projects').length).toBe(1)
  expect(writes.find((item) => item.path === '/projects')?.body).toMatchObject({
    storage_space_id: 9,
  })
  expect(reads).toContain('/storage/options?scope=user')
  expect(reads.some((path) => path.includes('/connections'))).toBe(false)
})
test('organization member sees safe project storage without discovery or management requests', async ({
  page,
}) => {
  const { reads, writes } = await setup(page, true)
  await page.goto('/projects/7')
  await page.getByText('项目存储', { exact: true }).click()
  await expect(page.getByText('只读旧空间', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '重新绑定空项目', exact: true })).toHaveCount(0)
  expect(
    reads.some((path) => path.includes('/storage/options') || path.includes('/connections')),
  ).toBe(false)
  expect(writes).toHaveLength(0)
})
test('historical repair uploads original bytes to its own task without changing the current source', async ({
  page,
}) => {
  const { writes } = await setup(page)
  await page.goto('/projects/7')
  await page
    .getByRole('button', { name: '查看段落：welcome.txt', exact: true })
    .locator('button')
    .click()
  await page.getByText('源文件与固定交付', { exact: true }).click()
  await page.getByRole('button', { name: '同字节修复', exact: true }).click()
  await expect(page.getByText('修复版本 #3', { exact: true })).toBeVisible()
  await chooseTarget(page)
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: '选择原始文件', exact: true }).click()
  await (
    await chooser
  ).setFiles({ name: 'original.txt', mimeType: 'text/plain', buffer: Buffer.from('abc') })
  await page.getByRole('button', { name: '创建修复任务', exact: true }).click()
  await page.getByRole('button', { name: '向原任务补传文件', exact: true }).click()
  await expect(page.getByText('原始文件修复完成。', { exact: true })).toBeVisible()
  expect(writes.find((item) => item.path === '/projects/7/storage/tasks')?.body).toMatchObject({
    kind: 'repair',
    source_revision_id: 3,
    location_generation: 0,
    target_space_id: 9,
    storage_generation: 0,
  })
  expect(writes.find((item) => item.path.endsWith('/content'))?.body).toBe('abc')
  expect(writes.some((item) => item.path.includes('source-commit'))).toBe(false)
})
test('migration from a read-only historical binding tracks the original cutover task', async ({
  page,
}) => {
  const { writes } = await setup(page)
  await page.goto('/projects/7')
  await page.getByText('项目存储', { exact: true }).click()
  await page.getByText('迁移已有数据', { exact: true }).click()
  await chooseTarget(page)
  await page.getByRole('button', { name: '迁移已有数据', exact: true }).click()
  await page.getByRole('button', { name: '确定', exact: true }).click()
  await expect(page.getByText('任务 #42 · cutover', { exact: true })).toBeVisible()
  expect(writes.filter((item) => item.path.endsWith('/migrations'))).toHaveLength(1)
  expect(writes.find((item) => item.path.endsWith('/migrations'))?.body).toEqual({
    space_id: 9,
    expected_generation: 0,
    idempotency_key: expect.any(String),
  })
})

test('maintenance beginning after confirmation opens prevents a binding write', async ({
  page,
}) => {
  const { writes } = await setup(page)
  let maintenance = false
  await page.route('**/api/v1/projects/7/storage', (route) =>
    maintenance
      ? json(route, {
          project_id: 7,
          storage_generation: 1,
          storage_state: 'draining',
          migration_task_id: null,
          binding: null,
          reason_codes: ['storage_maintenance'],
        })
      : route.fallback(),
  )
  await page.goto('/projects/7')
  await page.getByText('项目存储', { exact: true }).click()
  await chooseTarget(page)
  await page.getByRole('button', { name: '重新绑定空项目', exact: true }).click()
  await expect(page.getByText('确认将空项目绑定到所选空间？', { exact: true })).toBeVisible()
  maintenance = true
  await page.getByRole('button', { name: '确定', exact: true }).click()
  await expect(page.getByText('存储状态已变化，请刷新后重新确认。', { exact: true })).toBeVisible()
  expect(writes.filter((item) => item.path === '/projects/7/storage')).toHaveLength(0)
})
