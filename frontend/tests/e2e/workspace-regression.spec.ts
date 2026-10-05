import { expect, test } from '@playwright/test'
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

      // The app document also scrolls independently of the workspace pane.
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight))
      expect(await page.evaluate(() => scrollY)).toBeGreaterThan(0)
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
