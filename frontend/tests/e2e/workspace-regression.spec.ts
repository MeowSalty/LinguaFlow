import { expect, test, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'

const timestamp = '2026-09-30T00:00:00Z'
const project = {
  id: 7,
  name: 'Regression workspace',
  owner_user_id: 1,
  source_lang: 'en',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
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
  created_at: timestamp,
  updated_at: timestamp,
}
const plan = {
  id: 101,
  name: 'Regression plan',
  scope: 'user',
  owner_user_id: 1,
  profile_id: -1,
  rounds: [
    {
      mode: 'translate',
      backend_id: 101,
      concurrency: 3,
      translate: {
        prompt_template_id: -1,
        batch_size: 10,
        max_words_per_batch: 0,
        fallback_shrink: 1,
      },
    },
  ],
}

async function regressionApp(page: Page) {
  await mockApp(page, { role: 'user' })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  const quickRequests: Record<string, unknown>[] = [],
    segmentWrites: Record<string, unknown>[] = []
  let segment = {
    id: 711,
    sub_job_id: 1,
    segment_index: 0,
    source_text: 'Welcome to the workspace.',
    target_text: '欢迎进入工作区。',
    status: 'translated',
    quality_issues: [],
    created_at: timestamp,
    updated_at: timestamp,
  }
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname.replace('/api/v1', '')
    if (path === '/execution-plan-templates') return json(route, { items: [plan] })
    if (path === '/projects') return json(route, { items: [project] })
    if (path === '/projects/7') return json(route, project)
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
    if (path === '/projects/7/resources/71/segments/groups')
      return json(route, {
        items: [
          {
            group_key: '',
            group_title: '',
            segment_count: 1,
            translated_count: 1,
            approved_count: 0,
          },
        ],
      })
    if (path === '/projects/7/resources/71/segments')
      return json(route, { items: [segment], total: 1 })
    if (path === '/projects/7/resources/71/segments/711' && request.method() === 'PATCH') {
      const body = request.postDataJSON() as Record<string, unknown>
      segmentWrites.push(body)
      segment = { ...segment, target_text: String(body.target_text), status: 'edited' }
      return json(route, segment)
    }
    if (path === '/quick-translate' && request.method() === 'POST') {
      const body = request.postDataJSON() as Record<string, unknown>
      quickRequests.push(body)
      return json(route, {
        status: 'success',
        source_text: body.source_text,
        target_text: '你好，世界！',
        source_lang: 'en',
        target_lang: 'zh-Hans',
        quality_issues: [],
        usage: { api_calls: 1, input_tokens: 8, output_tokens: 6 },
      })
    }
    return route.fallback()
  })
  return { quickRequests, segmentWrites, errors }
}

test('quick translation submits the selected plan and renders the result', async ({ page }) => {
  const state = await regressionApp(page)
  await page.goto('/')
  await page.getByPlaceholder('粘贴需要翻译的文本…').fill('Hello, world!')
  await page.getByRole('button', { name: '翻译', exact: true }).click()
  await expect(page.getByText('你好，世界！', { exact: true })).toBeVisible()
  expect(state.quickRequests).toEqual([
    {
      source_text: 'Hello, world!',
      source_lang: 'auto',
      target_lang: 'zh-Hans',
      execution_plan_id: 101,
    },
  ])
  await expect(page.getByRole('button', { name: '复制译文', exact: true })).toBeVisible()
  expect(state.errors).toEqual([])
})

test('workspace resource editing saves a segment and records a successful recent visit', async ({
  page,
  isMobile,
}) => {
  const state = await regressionApp(page)
  await page.goto('/projects/7')
  await page.getByRole('button', { name: '查看段落：welcome.txt', exact: true }).click()
  await expect(page).toHaveURL(/\/projects\/7\?edit=71$/)
  await expect(
    page.getByText('Welcome to the workspace.', { exact: true }).filter({ visible: true }),
  ).toBeVisible()
  if (isMobile) await page.getByRole('button', { name: '编辑', exact: true }).click()
  else {
    await page.getByRole('cell', { name: 'Welcome to the workspace.', exact: true }).click()
    await page.keyboard.press('Enter')
  }
  const translation = page.getByPlaceholder('译文', { exact: true }).filter({ visible: true })
  await translation.fill('欢迎使用翻译工作区。')
  if (isMobile) await page.getByRole('button', { name: '保存', exact: true }).click()
  else await translation.press('Control+Enter')
  await expect(
    page.getByText('欢迎使用翻译工作区。', { exact: true }).filter({ visible: true }),
  ).toBeVisible()
  expect(state.segmentWrites).toEqual([{ target_text: '欢迎使用翻译工作区。' }])
  await expect
    .poll(() =>
      page.evaluate(() =>
        Object.keys(localStorage)
          .filter((key) => key.startsWith('linguaflow.preferences.v1:'))
          .flatMap((key) => JSON.parse(localStorage.getItem(key)!).recentProjects ?? [])
          .map((visit: { project_id: number }) => visit.project_id),
      ),
    )
    .toEqual([7])
  await page.goto('/')
  await expect(
    page.getByText('Regression workspace', { exact: true }).filter({ visible: true }),
  ).toBeVisible()
  expect(state.errors).toEqual([])
})

test('workspace navigation protects drafts and save failure keeps the editor open', async ({
  page,
  isMobile,
}) => {
  const state = await regressionApp(page)
  await page.goto('/projects/7?edit=71')
  if (isMobile) await page.getByRole('button', { name: '编辑', exact: true }).click()
  else {
    await page.getByRole('cell', { name: 'Welcome to the workspace.', exact: true }).click()
    await page.keyboard.press('Enter')
  }
  const translation = page.getByPlaceholder('译文', { exact: true }).filter({ visible: true })
  await translation.fill('未保存的本地草稿')
  await page.getByTitle('返回工作台', { exact: true }).click()
  await expect(page.getByText('有未保存的译文或备注', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '取消', exact: true }).last().click()
  await expect(page).toHaveURL(/edit=71/)
  await expect(translation).toHaveValue('未保存的本地草稿')
  await page.route('**/api/v1/projects/7/resources/71/segments/711', (route) =>
    json(route, { title: 'Unavailable', status: 503 }, 503),
  )
  await page.getByTitle('返回工作台', { exact: true }).click()
  await page.getByRole('button', { name: '保存并继续', exact: true }).click()
  await expect(page.getByText('有未保存的译文或备注', { exact: true })).toBeHidden()
  await expect(page).toHaveURL(/edit=71/)
  await expect(translation).toHaveValue('未保存的本地草稿')
  expect(state.segmentWrites).toEqual([])
  await page.getByTitle('返回工作台', { exact: true }).click()
  await page.getByRole('button', { name: '放弃并继续', exact: true }).click()
  await expect(page).not.toHaveURL(/edit=71/)
  expect(state.errors).toEqual([])
})

test('unknown storage state leaves DB editing accessible and blocks manifest uploads', async ({
  page,
}) => {
  const state = await regressionApp(page)
  await page.goto('/projects/7')
  await expect(
    page.getByText('当前存储状态尚未确认，暂不能上传、更新或删除文件；仍可查看资源和编辑译文。', {
      exact: true,
    }),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: '上传资源', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '查看段落：welcome.txt', exact: true }).click()
  await expect(page).toHaveURL(/edit=71/)
  await expect(
    page.getByText('Welcome to the workspace.', { exact: true }).filter({ visible: true }),
  ).toBeVisible()
  expect(state.errors).toEqual([])
})
