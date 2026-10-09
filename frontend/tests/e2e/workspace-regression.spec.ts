import { expect, test, type Locator, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json } from './fixtures'
import { regressionApp } from './workspace-regression-fixtures'
import {
  assertTaskHeader,
  assertUnobscured,
  mockTaskHeader,
  openAndCheckTaskPanel,
  scrollWorkspacePane,
  selectWorkspaceItem,
  selectionClear,
} from './task-header-visual-helpers'

const extractionDisabledHint = '项目未启用术语表，本次不提取术语'
const skippedExtractionHint = '已跳过：项目术语表已关闭'
const quickGlossaryDisabledHint =
  '项目未启用术语表，本次不使用已有或临时术语，也不提取新术语。临时输入仍然保留。'

function extractionRounds(
  kind: 'inline' | 'extract' | 'none',
): ApiSchemas['ExecutionRoundConfig'][] {
  const translate: ApiSchemas['ExecutionRoundConfig'] = {
    mode: 'translate',
    backend_id: 101,
    concurrency: 1,
    translate: {
      prompt_template_id: -1,
      batch_size: 10,
      fallback_shrink: 1,
      ...(kind === 'inline' ? { inline_term_extraction: { enabled: true } } : {}),
    },
  }
  return kind === 'extract'
    ? [
        {
          mode: 'extract',
          backend_id: 101,
          concurrency: 1,
          extract: { template_id: -1, batch_size: 10 },
        },
        translate,
      ]
    : [translate]
}

async function chooseRegressionPlan(page: Page, drawer: Locator) {
  await drawer.locator('.n-select').first().click()
  await page
    .locator('.n-base-select-menu:visible .n-base-select-option')
    .filter({ hasText: 'Regression plan' })
    .click()
}

for (const scenario of [
  { kind: 'inline', glossaryEnabled: false, showHint: true },
  { kind: 'extract', glossaryEnabled: false, showHint: true },
  { kind: 'none', glossaryEnabled: false, showHint: false },
  { kind: 'inline', glossaryEnabled: true, showHint: false },
  { kind: 'inline', glossaryEnabled: undefined, showHint: false },
] as const) {
  test(`new task and translation preview explain extraction for ${scenario.kind} with glossary ${scenario.glossaryEnabled}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 960 })
    const state = await regressionApp(page, {
      rounds: extractionRounds(scenario.kind),
      glossaryEnabled: scenario.glossaryEnabled,
    })
    if (scenario.glossaryEnabled === undefined) {
      await page.route('**/api/v1/projects/7', (route) =>
        json(route, { ...state.project, glossary_enabled: undefined }),
      )
    }
    await page.goto('/projects/7')
    await selectWorkspaceItem(page, false)
    await page.getByRole('button', { name: '处理选中', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await chooseRegressionPlan(page, drawer)
    await expect(drawer.getByText(extractionDisabledHint, { exact: true })).toHaveCount(
      scenario.showHint ? 1 : 0,
    )
    await expect(drawer.getByRole('button', { name: '创建任务', exact: true })).toBeEnabled()
    await drawer.getByRole('button', { name: '取消', exact: true }).click()
    await expect(drawer).toHaveCount(0)

    await page.goto('/projects/7?edit=71')
    await page.getByRole('button', { name: '单段试译', exact: true }).click()
    await chooseRegressionPlan(page, drawer)
    await expect(drawer.getByText(extractionDisabledHint, { exact: true })).toHaveCount(
      scenario.showHint ? 1 : 0,
    )
    await expect(drawer.getByRole('button', { name: '开始试译', exact: true })).toBeEnabled()
    expect(state.errors).toEqual([])
  })
}

test('quick translation keeps temporary terms and skipped extraction follows the returned round', async ({
  page,
}) => {
  const state = await regressionApp(page, {
    rounds: [...extractionRounds('extract'), ...extractionRounds('none')],
    quickRoundSummary: [
      { index: 0, mode: 'extract', status: 'skipped', duration_ms: 0 },
      { index: 1, mode: 'translate', status: 'success', duration_ms: 12 },
      { index: 2, mode: 'translate', status: 'skipped', duration_ms: 0 },
    ],
  })
  await page.goto('/')
  await page.getByRole('button', { name: '高级选项', exact: true }).click()
  await page.getByRole('button', { name: '添加术语', exact: true }).click()
  await page.getByPlaceholder('源术语', { exact: true }).fill('world')
  await page.getByPlaceholder('目标术语', { exact: true }).fill('世界')
  await expect(page.getByText(quickGlossaryDisabledHint, { exact: true })).toHaveCount(0)
  const projectSelect = page.getByLabel('项目（可选）', { exact: true })
  await projectSelect.click()
  await page
    .locator('.n-base-select-menu:visible .n-base-select-option')
    .filter({ hasText: 'Regression workspace' })
    .click()
  await expect(page.getByText(quickGlossaryDisabledHint, { exact: true })).toBeVisible()
  await page.getByPlaceholder('粘贴需要翻译的文本…').fill('Hello, world!')
  await page.getByRole('button', { name: '翻译', exact: true }).click()
  await expect(page.getByText(skippedExtractionHint, { exact: true })).toBeVisible()
  await expect(page.getByText('已跳过', { exact: true })).toBeVisible()
  await expect(page.getByText('你好，世界！', { exact: true })).toBeVisible()
  expect(state.quickRequests[0]).toMatchObject({
    project_id: 7,
    glossary: [{ source: 'world', target: '世界' }],
  })

  await projectSelect.hover()
  await projectSelect.locator('.n-base-clear').click()
  await expect(page.getByText(quickGlossaryDisabledHint, { exact: true })).toHaveCount(0)
  await expect(page.getByText(skippedExtractionHint, { exact: true })).toBeVisible()
  await expect(page.getByPlaceholder('源术语', { exact: true })).toHaveValue('world')
  await expect(page.getByPlaceholder('目标术语', { exact: true })).toHaveValue('世界')
  expect(state.errors).toEqual([])
})

test('quick translation without a project can submit temporary terms and an extraction plan', async ({
  page,
}) => {
  const state = await regressionApp(page, { rounds: extractionRounds('inline') })
  await page.goto('/')
  await page.getByRole('button', { name: '高级选项', exact: true }).click()
  await page.getByRole('button', { name: '添加术语', exact: true }).click()
  await page.getByPlaceholder('源术语', { exact: true }).fill('world')
  await page.getByPlaceholder('目标术语', { exact: true }).fill('世界')
  await page.getByPlaceholder('粘贴需要翻译的文本…').fill('Hello, world!')
  await expect(page.getByText(quickGlossaryDisabledHint, { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '翻译', exact: true }).click()
  await expect(page.getByText('你好，世界！', { exact: true })).toBeVisible()
  expect(state.quickRequests[0]).not.toHaveProperty('project_id')
  expect(state.quickRequests[0]).toMatchObject({
    execution_plan_id: 101,
    glossary: [{ source: 'world', target: '世界' }],
  })
  expect(state.errors).toEqual([])
})

for (const frozenGlossaryEnabled of [false, true, undefined]) {
  test(`historical skipped extraction uses frozen glossary ${frozenGlossaryEnabled} despite the current project`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1280, height: 960 })
    const state = await regressionApp(page, { glossaryEnabled: frozenGlossaryEnabled === false })
    const timestamp = '2026-10-01T00:00:00Z'
    const savedJob: ApiSchemas['Job'] = {
      id: 42,
      project_id: 7,
      execution_plan_id: 101,
      status: 'completed',
      trigger_type: 'manual',
      execution_config: { glossary_enabled: frozenGlossaryEnabled },
      progress: {
        total_resources: 1,
        completed_resources: 1,
        failed_resources: 0,
        progress_total: 1,
        progress_completed: 1,
      },
      job_resources: [
        {
          id: 421,
          resource_id: 71,
          status: 'completed',
          segment_count: 1,
          completed_segments: 1,
          skipped_segments: 0,
          work_weight: 1,
          rounds: [
            {
              round_index: 0,
              mode: 'extract',
              status: 'skipped',
              segment_total: 0,
              segment_completed: 0,
              finished_at: timestamp,
            },
            {
              round_index: 1,
              mode: 'translate',
              status: 'completed',
              segment_total: 1,
              segment_completed: 1,
              finished_at: timestamp,
            },
          ],
          created_at: timestamp,
          updated_at: timestamp,
        },
      ],
      can_delete: true,
      created_at: timestamp,
      updated_at: timestamp,
      finished_at: timestamp,
    }
    await page.route('**/api/v1/jobs/42', (route) => json(route, savedJob))
    await page.route('**/api/v1/projects/7/jobs*', (route) => json(route, { items: [savedJob] }))
    await page.goto('/projects/7?tab=jobs')
    await page.getByRole('button', { name: '详情', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await expect(drawer.getByText('资源执行明细', { exact: true })).toBeVisible()
    await drawer.getByRole('button', { name: /展开.*的轮次/ }).click()
    const extractionRound = drawer.locator('.round-item').filter({ hasText: '术语提取' })
    if (frozenGlossaryEnabled === false) {
      await expect(extractionRound).toContainText(skippedExtractionHint)
    } else {
      await expect(extractionRound).toContainText('已跳过')
      await expect(drawer.getByText(skippedExtractionHint, { exact: true })).toHaveCount(0)
    }
    await expect(drawer.getByText('完成 1 轮 · 跳过 1 轮', { exact: true })).toBeVisible()
    expect(state.errors).toEqual([])
  })
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

for (const editor of [false, true]) {
  for (const [width, height] of [
    [320, 720],
    [720, 480],
  ]) {
    test(`task header remains usable in ${editor ? 'segment editor' : 'resource browser'} with selection at ${width} CSS pixels`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: 1280, height: 960 })
      const state = await regressionApp(page, { populated: true })
      await mockTaskHeader(page)
      await page.goto(editor ? '/projects/7?edit=71' : '/projects/7')
      await selectWorkspaceItem(page, editor)
      await page.setViewportSize({ width: width!, height: height! })
      await assertTaskHeader(page)
      await scrollWorkspacePane(page, editor)
      await assertTaskHeader(page)

      // The workspace fits the viewport; only its content pane should scroll.
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
      expect(await page.evaluate(() => scrollY)).toBe(0)
      expect(await page.evaluate(() => document.documentElement.scrollHeight <= innerHeight)).toBe(
        true,
      )
      await assertTaskHeader(page)
      await openAndCheckTaskPanel(page)

      await page
        .getByTestId('global-job-tracker-panel')
        .getByRole('button', { name: '关闭', exact: true })
        .click()
      await expect(page.getByTestId('global-job-tracker-panel')).toBeHidden()
      await expect(page.getByTestId('global-job-tracker-trigger')).toBeFocused()
      const clear = selectionClear(page)
      await assertUnobscured(clear)
      await clear.click()
      await expect(clear).toBeHidden()
      expect(state.errors).toEqual([])
    })
  }
}

test('task panel stays above a retained upload result and leaves upload actions usable at 320 CSS pixels', async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 480 })
  const state = await regressionApp(page, { upload: true })
  await mockTaskHeader(page)
  await page.goto('/projects/7')
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: '上传资源', exact: true }).click()
  await (
    await chooser
  ).setFiles({
    name: 'header-upload.txt',
    mimeType: 'text/plain',
    buffer: Buffer.from('Task header upload regression'),
  })
  await expect(page.getByRole('button', { name: '1 个文件上传失败', exact: true })).toBeVisible()
  await expect(page.getByText('header-upload.txt', { exact: true })).toBeVisible()
  expect(state.uploads).toHaveLength(1)
  expect(state.uploads[0]).toContain('Task header upload regression')
  // The existing upload error toast is unrelated to the task entry; let it expire.
  await expect(page.locator('.n-message')).toHaveCount(0, { timeout: 10000 })
  await openAndCheckTaskPanel(page)
  await page
    .getByTestId('global-job-tracker-panel')
    .getByRole('button', { name: '关闭', exact: true })
    .click()
  const clearUploads = page.getByRole('button', { name: '清除全部', exact: true })
  await assertUnobscured(clearUploads)
  await clearUploads.click()
  await expect(page.getByText('header-upload.txt', { exact: true })).toBeHidden()
  expect(state.errors).toEqual([])
})
