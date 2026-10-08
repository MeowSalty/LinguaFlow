import { expect, test, type Page, type Route } from '@playwright/test'
import {
  createProfileConfig,
  QA_CHECKS,
  type ProfileConfigInput,
} from '../../src/utils/execution-profile-config'
import type { ExecutionRound } from '../../src/utils/execution-plan-config'
import { json, mockApp } from './fixtures'

type Write = { path: string; body: { config: ProfileConfigInput; rounds: ExecutionRound[] } }
async function setup(
  page: Page,
  config: unknown = createProfileConfig(),
  rounds: ExecutionRound[] = [],
) {
  await mockApp(page, { role: 'user' })
  const writes: Write[] = []
  const profile = {
    id: 1,
    name: '策略样本',
    description: 'profile',
    scope: 'user',
    owner_user_id: 1,
    config,
  }
  const plan = {
    id: 1,
    name: '计划样本',
    description: 'plan',
    scope: 'user',
    owner_user_id: 1,
    profile_id: 1,
    rounds,
  }
  let delayWrite: ((route: Route) => Promise<void>) | undefined
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
    const method = route.request().method()
    if (path === '/orgs')
      return json(route, {
        items: [
          {
            id: 7,
            name: '目标组织',
            slug: 'target',
            display_name: '目标组织',
            current_user_role: 'owner',
          },
        ],
      })
    if (method === 'POST' || method === 'PUT') {
      writes.push({ path, body: route.request().postDataJSON() })
      if (delayWrite) return delayWrite(route)
      return json(route, path.startsWith('/execution-profiles') ? profile : plan)
    }
    if (path === '/execution-profiles') return json(route, { items: [profile] })
    if (path === '/execution-plan-templates') return json(route, { items: [plan] })
    if (path === '/backends')
      return json(route, {
        items: [
          {
            id: 1,
            name: '模型样本',
            scope: 'user',
            owner_user_id: 1,
            type: 'openai',
            options: { type: 'openai', model: 'test-model' },
            has_secret: true,
            credential: { id: 1, version: 1 },
          },
        ],
      })
    if (path === '/translation-prompt-templates')
      return json(route, {
        items: [
          {
            id: 1,
            name: '提示词样本',
            scope: 'user',
            owner_user_id: 1,
            system_prompt_content: 'test',
          },
        ],
      })
    return route.fallback()
  })
  return {
    writes,
    profile,
    delay: (handler: (route: Route) => Promise<void>) => {
      delayWrite = handler
    },
  }
}

async function openProfile(page: Page) {
  await page.goto('/execution-profiles')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  return page.locator('.n-drawer')
}

test('new profile uses the new defaults and keeps QA checks omitted', async ({ page }) => {
  const { writes } = await setup(page)
  await page.goto('/execution-profiles')
  await page.getByRole('button', { name: '新建策略', exact: true }).click()
  const drawer = page.locator('.n-drawer')
  await drawer.getByRole('textbox').first().fill('新策略')
  await expect(drawer.getByRole('switch', { name: '启用处理', exact: true })).toBeChecked()
  await expect(drawer.getByRole('checkbox', { name: '创意', exact: true })).toBeChecked()
  await expect(drawer.getByRole('checkbox', { name: '音注', exact: true })).not.toBeChecked()
  await drawer.getByRole('button', { name: '创建策略', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual(createProfileConfig())
})

test('editing an optional sparse response does not synthesize config fields', async ({ page }) => {
  const config = createProfileConfig()
  delete config.ruby
  delete config.qa
  delete config.protect.rules
  const { writes } = await setup(page, config)
  const drawer = await openProfile(page)
  await expect(drawer.getByText('服务端未返回此配置', { exact: true })).toHaveCount(2)
  await expect(drawer.getByText('未指定', { exact: true })).toBeVisible()
  await drawer.getByRole('switch', { name: '启用上下文窗口', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({ schema_version: 1, context: { enabled: false } })
})

test('optional group configuration is an explicit action using current defaults', async ({
  page,
}) => {
  const config = createProfileConfig()
  delete config.ruby
  const { writes } = await setup(page, config)
  const drawer = await openProfile(page)
  await drawer.getByRole('button', { name: '配置此项', exact: true }).click()
  await expect(drawer.getByRole('checkbox', { name: '创意', exact: true })).toBeChecked()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({
    schema_version: 1,
    ruby: { enabled: true, preserve_kinds: ['creative'] },
  })
})

test('display defaults for sparse Ruby and QA fields do not become update values', async ({
  page,
}) => {
  const config = createProfileConfig()
  config.ruby = { enabled: true }
  config.qa = { enabled: true }
  const { writes } = await setup(page, config)
  const drawer = await openProfile(page)
  await expect(drawer.getByRole('checkbox', { name: '创意', exact: true })).toBeChecked()
  await expect(drawer.getByRole('radio', { name: '启用全部检查项', exact: true })).toBeChecked()
  await drawer.getByRole('textbox').first().fill('只改名称')
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({ schema_version: 1 })
})

test('explicit QA checks can be empty or all current checks, with no false inheritance reset', async ({
  page,
}) => {
  const config = createProfileConfig()
  config.qa!.enabled = true
  config.qa!.checks = ['untranslated']
  const { writes } = await setup(page, config)
  let drawer = await openProfile(page)
  await expect(drawer.getByRole('radio', { name: '启用全部检查项', exact: true })).toHaveCount(0)
  await drawer.getByRole('button', { name: '清空检查项', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({ schema_version: 1, qa: { checks: [] } })
  await expect(page.locator('.n-drawer')).toHaveCount(0)
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  drawer = page.locator('.n-drawer')
  await drawer.getByRole('button', { name: '全选当前检查项', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1]!.body.config.qa!.checks).toEqual(QA_CHECKS)
})

for (const invalid of ['missing-version', 'unknown-version', 'missing-required', 'null-field']) {
  test(`incompatible response ${invalid} prevents saving`, async ({ page }) => {
    const config = createProfileConfig() as unknown as Record<string, unknown>
    if (invalid === 'missing-version') delete config.schema_version
    if (invalid === 'unknown-version') config.schema_version = 2
    if (invalid === 'missing-required') delete (config.context as Record<string, unknown>).before
    if (invalid === 'null-field') config.ruby = null
    const { writes } = await setup(page, config)
    const drawer = await openProfile(page)
    await expect(
      drawer.getByText('此策略配置与当前版本不兼容，无法编辑或复制。', { exact: true }),
    ).toBeVisible()
    await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
    expect(writes).toHaveLength(0)
  })
}

test('late profile write success cannot close a new draft or show its success toast', async ({
  page,
}) => {
  const app = await setup(page)
  let pending: Route | undefined
  let release!: () => void
  const waiting = new Promise<void>((resolve) => {
    release = resolve
  })
  app.delay(async (route) => {
    pending = route
    await waiting
    await json(route, app.profile)
  })
  const drawer = await openProfile(page)
  await drawer.getByRole('textbox').first().fill('旧写入')
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => Boolean(pending)).toBe(true)
  await drawer.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '新建策略', exact: true }).click()
  await page.locator('.n-drawer').getByRole('textbox').first().fill('新草稿')
  const response = page.waitForResponse(
    (r) => r.url().endsWith('/execution-profiles/1') && r.request().method() === 'PUT',
  )
  release()
  await response
  await expect(page.locator('.n-drawer').getByRole('textbox').first()).toHaveValue('新草稿')
  await expect(page.locator('.n-message')).toHaveCount(0)
})

test('copying an incompatible profile never creates an editable default draft', async ({
  page,
}) => {
  const config = { ...createProfileConfig(), schema_version: 2 }
  const { writes } = await setup(page, config)
  await page.goto('/execution-profiles')
  await page.getByRole('button', { name: '复制到组织', exact: true }).click()
  const modal = page.locator('.n-modal:visible')
  await modal.locator('.n-select').click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('目标组织', { exact: true })
    .click()
  await modal.getByRole('button', { name: '继续编辑', exact: true }).click()
  await expect(
    page.getByText('此策略配置与当前版本不兼容，无法编辑或复制。', { exact: true }),
  ).toBeVisible()
  await expect(page.locator('.n-drawer')).toHaveCount(0)
  expect(writes).toHaveLength(0)
})

test('unsaved default QA selection remembers an explicit empty selection and toggling QA preserves it', async ({
  page,
}) => {
  const config = createProfileConfig()
  config.qa!.enabled = true
  const { writes } = await setup(page, config)
  const drawer = await openProfile(page)
  await drawer.getByText('自定义选择', { exact: true }).click()
  await expect(drawer.getByRole('checkbox', { name: '未翻译', exact: true })).not.toBeChecked()
  await drawer.getByText('启用全部检查项', { exact: true }).click()
  await drawer.getByText('自定义选择', { exact: true }).click()
  await drawer.getByRole('switch', { name: '启用质量检测', exact: true }).click()
  await drawer.getByRole('switch', { name: '启用质量检测', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({ schema_version: 1, qa: { checks: [] } })
})

test('unchanged plan arrays and absent scopes round-trip through the page', async ({ page }) => {
  const rounds: ExecutionRound[] = [
    {
      mode: 'adjudicate',
      backend_id: 1,
      concurrency: 1,
      adjudicate: { batch_size: 10, adjudicate_codes: [] },
    },
    {
      mode: 'semantic_qa',
      backend_id: 1,
      concurrency: 1,
      semantic_qa: { batch_size: 10, issue_codes: ['source_residual'] },
    },
    { mode: 'revise', backend_id: 1, concurrency: 1, revise: { batch_size: 10, issue_codes: [] } },
    {
      mode: 'translate',
      backend_id: 1,
      concurrency: 1,
      translate: {
        prompt_template_id: 1,
        batch_size: 10,
        fallback_shrink: 1,
        segment_filter: { status_filter: 'skip_approved' },
      },
    },
  ]
  const { writes } = await setup(page, createProfileConfig(), rounds)
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  await page.locator('.n-drawer').getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  const saved = writes[0]!.body.rounds
  expect(saved[0].adjudicate.adjudicate_codes).toEqual([])
  expect(saved[1].semantic_qa.issue_codes).toEqual(['source_residual'])
  expect(saved[1].semantic_qa).not.toHaveProperty('segment_scope')
  expect(saved[2].revise.issue_codes).toEqual([])
  expect(saved[2].revise).not.toHaveProperty('segment_scope')
  expect(saved[3].translate.segment_filter).toEqual({ status_filter: 'skip_approved' })
})

test('plan scope and default controls preserve selected drafts and never insert implicit codes', async ({
  page,
}) => {
  const rounds: ExecutionRound[] = [
    { mode: 'revise', backend_id: 1, concurrency: 1, revise: { batch_size: 10 } },
  ]
  const { writes } = await setup(page, createProfileConfig(), rounds)
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  const drawer = page.locator('.n-drawer')
  const round = drawer.getByTestId('execution-round')
  await round.getByLabel('段落扫描范围', { exact: true }).click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('按问题代码筛选', { exact: true })
    .click()
  await expect(round.getByText('此范围要求至少选择一个问题代码。', { exact: true })).toBeVisible()
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
  await round.getByLabel('问题代码筛选', { exact: true }).click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('语法', { exact: true })
    .click()
  await round.getByLabel('问题代码筛选', { exact: true }).press('Escape')
  await round.getByText('默认', { exact: true }).click()
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
  await round.getByText('指定', { exact: true }).click()
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeEnabled()
  await round.getByLabel('段落扫描范围', { exact: true }).click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('存在未决语义问题的段落', { exact: true })
    .click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds[0].revise).toMatchObject({
    segment_scope: 'with_issues',
    issue_codes: ['grammar'],
  })
})

test('semantic QA keeps dormant codes when changing scope and restores them when filtering again', async ({
  page,
}) => {
  const rounds: ExecutionRound[] = [
    {
      mode: 'semantic_qa',
      backend_id: 1,
      concurrency: 1,
      semantic_qa: { batch_size: 10, issue_codes: ['source_residual'] },
    },
  ]
  const { writes } = await setup(page, createProfileConfig(), rounds)
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  const drawer = page.locator('.n-drawer')
  const round = drawer.getByTestId('execution-round')
  await expect(
    round.getByText('此范围不按问题代码筛选；已保存的选择会保留。', { exact: true }),
  ).toBeVisible()
  const scope = round.getByLabel('段落扫描范围', { exact: true })
  await scope.click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('按问题代码筛选', { exact: true })
    .click()
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeEnabled()
  await scope.click()
  await page
    .locator('.n-base-select-menu:visible')
    .last()
    .getByText('仅有问题的段落', { exact: true })
    .click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds[0].semantic_qa).toMatchObject({
    segment_scope: 'with_issues',
    issue_codes: ['source_residual'],
  })
})

for (const kind of ['profile', 'plan'] as const) {
  test(`late ${kind} write failure cannot toast into a replacement draft`, async ({ page }) => {
    const rounds: ExecutionRound[] = [
      { mode: 'revise', backend_id: 1, concurrency: 1, revise: { batch_size: 10 } },
    ]
    const app = await setup(page, createProfileConfig(), rounds)
    let pending = false
    let release!: () => void
    const waiting = new Promise<void>((resolve) => {
      release = resolve
    })
    app.delay(async (route) => {
      pending = true
      await waiting
      await json(route, { title: 'Old write failed', status: 500 }, 500)
    })
    const path = kind === 'profile' ? '/execution-profiles' : '/execution-plan-templates'
    await page.goto(path)
    await page.getByRole('button', { name: '编辑', exact: true }).click()
    const drawer = page.locator('.n-drawer')
    await drawer.getByRole('textbox').first().fill('旧写入')
    await drawer.getByRole('button', { name: '保存', exact: true }).click()
    await expect.poll(() => pending).toBe(true)
    await drawer.getByRole('button', { name: '取消', exact: true }).click()
    await page
      .getByRole('button', { name: kind === 'profile' ? '新建策略' : '新建计划', exact: true })
      .click()
    await page.locator('.n-drawer').getByRole('textbox').first().fill('新草稿')
    const response = page.waitForResponse(
      (r) => r.url().endsWith(`${path}/1`) && r.request().method() === 'PUT',
    )
    release()
    await (await response).finished()
    await page.evaluate(
      () =>
        new Promise<void>((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
        ),
    )
    await expect(page.locator('.n-drawer').getByRole('textbox').first()).toHaveValue('新草稿')
    await expect(page.locator('.n-message')).toHaveCount(0)
  })

  test(`current ${kind} write failure keeps its draft and displays one error`, async ({ page }) => {
    const rounds: ExecutionRound[] = [
      { mode: 'revise', backend_id: 1, concurrency: 1, revise: { batch_size: 10 } },
    ]
    const app = await setup(page, createProfileConfig(), rounds)
    app.delay((route) => json(route, { title: 'Write failed', status: 500 }, 500))
    await page.goto(kind === 'profile' ? '/execution-profiles' : '/execution-plan-templates')
    await page.getByRole('button', { name: '编辑', exact: true }).click()
    const drawer = page.locator('.n-drawer')
    await drawer.getByRole('textbox').first().fill('保留草稿')
    await drawer.getByRole('button', { name: '保存', exact: true }).click()
    await expect(page.locator('.n-message')).toHaveCount(1)
    await expect(drawer.getByRole('textbox').first()).toHaveValue('保留草稿')
    await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeEnabled()
  })
}

test('round reordering preserves a temporarily defaulted specified-code draft', async ({
  page,
}) => {
  const rounds: ExecutionRound[] = [
    {
      mode: 'revise',
      backend_id: 1,
      concurrency: 1,
      revise: { batch_size: 10, issue_codes: ['grammar'] },
    },
    { mode: 'adjudicate', backend_id: 1, concurrency: 1, adjudicate: { batch_size: 10 } },
  ]
  const { writes } = await setup(page, createProfileConfig(), rounds)
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  const drawer = page.locator('.n-drawer')
  const round = drawer.getByTestId('execution-round')
  await round.first().getByText('默认', { exact: true }).click()
  await round.first().getByRole('button', { name: '下移', exact: true }).click()
  await round.nth(1).getByText('指定', { exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds[1].revise?.issue_codes).toEqual(['grammar'])
  expect(writes[0]!.body.rounds[0].adjudicate).not.toHaveProperty('adjudicate_codes')
})
