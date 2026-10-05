import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'

const timestamp = '2026-10-04T00:00:00Z'
const project = {
  id: 7,
  name: 'Source updates',
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
const preview = {
  task_id: 42,
  source_generation: 0,
  translation_generation: 9,
  stats: { added: 2, updated: 1, deleted: 4, unchanged: 3 },
  expires_at: null,
}
const committed = {
  id: 42,
  operation_id: 'original-operation',
  kind: 'source_update',
  project_id: 7,
  resource_id: 71,
  status: 'completed',
  phase: 'committed',
  cleanup_status: 'done',
  allowed_actions: [],
  result_resource_id: 71,
  result_revision_id: 2,
  expires_at: null,
  created_at: timestamp,
  updated_at: timestamp,
}
type State = {
  commits: unknown[]
  files: string[]
  keys: string[]
  generation: number
  loseCommit?: boolean
  rejectSize?: boolean
}
async function setup(page: Page, state: State) {
  await mockApp(page, { role: 'user' })
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace('/api/v1', '')
    if (path === '/projects') return json(route, { items: [project] })
    if (path === '/projects/7') return json(route, project)
    if (path === '/projects/7/resources/tree')
      return json(route, {
        root: {
          type: 'directory',
          name: '',
          path: '',
          children: [
            {
              type: 'resource',
              name: resource.name,
              path: resource.path,
              resource: { ...resource, source_generation: state.generation },
            },
          ],
        },
      })
    if (path === '/projects/7/resources')
      return json(route, { items: [{ ...resource, source_generation: state.generation }] })
    if (path === '/projects/7/storage')
      return json(route, {
        project_id: 7,
        storage_generation: 0,
        storage_state: 'active',
        migration_task_id: null,
        binding: null,
        reason_codes: [],
      })
    if (path.endsWith('/source-preview')) {
      state.files.push(request.postDataBuffer()?.toString() ?? '')
      state.keys.push(request.headers()['idempotency-key'] ?? '')
      if (state.rejectSize)
        return json(
          route,
          { title: 'Too large', status: 413, error_code: 'storage_payload_too_large' },
          413,
        )
      return json(route, { ...preview, source_generation: state.generation })
    }
    if (path.endsWith('/source-commit')) {
      const body = request.postDataJSON()
      state.commits.push(body)
      if (body.expected_source_generation !== state.generation)
        return json(
          route,
          { title: 'Conflict', status: 409, error_code: 'source_revision_conflict' },
          409,
        )
      state.generation++
      if (state.loseCommit) return route.abort('connectionreset')
      return json(route, committed)
    }
    if (path === '/projects/7/storage/tasks/42') return json(route, committed)
    return route.fallback()
  })
}
async function openCandidate(page: Page) {
  await page.goto('/projects/7')
  await page
    .getByRole('button', { name: '查看段落：welcome.txt', exact: true })
    .locator('button')
    .click()
  await page.getByText('更新源文件', { exact: true }).click()
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: '选择源文件', exact: true }).click()
  await (
    await chooser
  ).setFiles({
    name: 'welcome.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('new source bytes'),
  })
  await page.getByRole('button', { name: '预览变化', exact: true }).click()
}

test('ordinary owner previews four counts and publishes the frozen generations using a real File', async ({
  page,
}) => {
  const state: State = { commits: [], files: [], keys: [], generation: 0 }
  await setup(page, state)
  await openCandidate(page)
  for (const label of ['新增段落', '原文变更段落', '删除段落', '未变化段落', '未提供到期时间'])
    await expect(page.getByText(label, { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '确认更新', exact: true }).click()
  await expect(page.getByText('源文件已更新，视图已刷新。', { exact: true })).toBeVisible()
  expect(state.files).toEqual(['new source bytes'])
  expect(state.commits).toEqual([
    { task_id: 42, expected_source_generation: 0, expected_translation_generation: 9 },
  ])
})
test('lost commit response recovers original task without resubmitting the commit', async ({
  page,
}) => {
  const state: State = { commits: [], files: [], keys: [], generation: 0, loseCommit: true }
  await setup(page, state)
  await openCandidate(page)
  await page.getByRole('button', { name: '确认更新', exact: true }).click()
  await expect(
    page.getByText('更新结果待确认，请查看原任务；不要重复创建更新。', { exact: true }),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: '选择源文件', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '恢复原任务结果', exact: true }).click()
  await expect(page.getByText('源文件已更新，视图已刷新。', { exact: true })).toBeVisible()
  expect(state.commits).toHaveLength(1)
})
test('another tab publishing invalidates old confirmation and a new preview receives a fresh key', async ({
  page,
  context,
}) => {
  const state: State = { commits: [], files: [], keys: [], generation: 0 }
  const second = await context.newPage()
  await setup(page, state)
  await setup(second, state)
  await openCandidate(page)
  await openCandidate(second)
  await second.getByRole('button', { name: '确认更新', exact: true }).click()
  await expect(second.getByText('源文件已更新，视图已刷新。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '确认更新', exact: true }).click()
  await expect(page.getByText('预览确认已失效，请重新预览文件。', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '创建新预览', exact: true }).click()
  await expect(page.getByText('预览候选已准备，尚未发布。', { exact: true })).toBeVisible()
  expect(state.keys[2]).not.toBe(state.keys[0])
  await second.close()
})
test('413 File rejection does not offer confirmation or report publication', async ({ page }) => {
  const state: State = { commits: [], files: [], keys: [], generation: 0, rejectSize: true }
  await setup(page, state)
  await openCandidate(page)
  await expect(page.getByText('操作失败，请读取原任务状态。', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '确认更新', exact: true })).toBeDisabled()
  expect(state.files).toEqual(['new source bytes'])
  expect(state.commits).toEqual([])
})
