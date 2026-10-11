import { chromium, expect, test, type Locator, type Page, type Route } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import {
  createProfileConfig,
  QA_CHECKS,
  type ProfileConfigInput,
} from '../../src/utils/execution-profile-config'
import type { ExecutionRound, ExecutionPlanRubyRetry } from '../../src/utils/execution-plan-config'
import { json, mockApp } from './fixtures'

type Write = {
  path: string
  body: {
    config: ProfileConfigInput
    rounds: ExecutionRound[]
    ruby_retry?: ExecutionPlanRubyRetry
    org_id?: number
  }
}
async function setup(
  page: Page,
  config: unknown = createProfileConfig(),
  rounds: ExecutionRound[] = [],
  rubyRetry?: ExecutionPlanRubyRetry,
  theme: 'light' | 'dark' = 'light',
) {
  await mockApp(page, { role: 'user', theme })
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
    ...(rubyRetry === undefined ? {} : { ruby_retry: rubyRetry }),
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
        items: [1, 2].map((id) => ({
          id,
          name: id === 1 ? '模型样本' : '第二模型',
          scope: 'user',
          owner_user_id: 1,
          type: 'openai',
          options: { type: 'openai', model: 'test-model' },
          has_secret: true,
          credential: { id: 1, version: 1 },
        })),
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
    if (path === '/bootstrap-prompt-templates')
      return json(route, {
        items: [
          { id: 1, name: '术语提示词样本', scope: 'user', owner_user_id: 1, content: 'test' },
        ],
      })
    return route.fallback()
  })
  return {
    writes,
    profile,
    plan,
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

type InlineConfig = NonNullable<NonNullable<ExecutionRound['translate']>['inline_term_extraction']>
const inlineDefaults: InlineConfig = {
  enabled: false,
  max_terms_per_1000_words: 3,
  min_source_len: 2,
  conflict_strategy: 'rewrite-local',
}
function translateRound(inline?: InlineConfig): ExecutionRound {
  return {
    mode: 'translate',
    backend_id: 1,
    concurrency: 2,
    translate: {
      prompt_template_id: 1,
      batch_size: 7,
      max_words_per_batch: 200,
      fallback_shrink: 0.5,
      segment_filter: { status_filter: 'skip_approved' },
      retry: { max_attempts: 2, backoff_ms: 100, jitter: false },
      ...(inline === undefined ? {} : { inline_term_extraction: { ...inline } }),
    },
  }
}
async function openPlan(page: Page) {
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  return page.locator('.n-drawer:visible')
}
async function showAdvanced(round: Locator) {
  if (!(await round.getByLabel('最大重试次数', { exact: true }).isVisible()))
    await round.getByText('高级配置', { exact: true }).click()
  await expect(round.getByLabel('最大重试次数', { exact: true })).toBeVisible()
}
async function chooseOption(page: Page, select: Locator, label: string) {
  await select.click()
  await page.locator('.n-base-select-menu:visible').last().getByText(label, { exact: true }).click()
}
async function fillNumber(field: Locator, value: string) {
  await field.fill(value)
  await field.press('Tab')
}
async function expectRubySettingsHidden(drawer: Locator) {
  await expect(drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true })).toBeVisible()
  await expect(drawer.getByText('注音对齐后端', { exact: true })).toBeHidden()
  for (const label of [
    '重试轮数',
    '注音请求并发上限',
    ...rubyBatchFields.map((field) => field.label),
  ])
    await expect(drawer.getByLabel(label, { exact: true })).toBeHidden()
  await expect(drawer.getByRole('button', { name: '合批设置', exact: true })).toBeHidden()
  await expect(
    drawer.getByText('注音对齐已关闭，以下请求设置暂不生效。', { exact: true }),
  ).toBeHidden()
}
async function chooseRoundMode(round: Locator, label: string) {
  const radio = round.getByRole('radio', { name: label, exact: true })
  await expect(radio).toBeEnabled()
  await round
    .locator('.n-radio__label')
    .filter({ hasText: new RegExp(`^${label}$`) })
    .click()
  await expect(radio).toBeChecked()
}

for (const concurrency of [undefined, 4]) {
  test(`ruby concurrency ${concurrency ?? 'default'} survives new plan creation`, async ({
    page,
  }) => {
    const { writes } = await setup(page)
    await page.goto('/execution-plan-templates')
    await page.getByRole('button', { name: '新建计划', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await drawer.getByRole('textbox').first().fill('注音并发计划')
    await chooseOption(
      page,
      drawer.locator('.n-select').filter({ hasText: '选择计划引用的执行策略' }),
      '策略样本',
    )
    await chooseRoundMode(drawer.getByTestId('execution-round'), '本地改写')
    await expectRubySettingsHidden(drawer)
    await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
    const field = drawer.getByLabel('注音请求并发上限', { exact: true })
    await expect(field).toHaveValue('')
    await expect(field).toHaveAttribute('placeholder', '默认（1）')
    if (concurrency !== undefined) await fillNumber(field, String(concurrency))
    await drawer.getByRole('button', { name: '创建计划', exact: true }).click()
    await expect.poll(() => writes.length).toBe(1)
    if (concurrency === undefined)
      expect(writes[0]!.body.ruby_retry).not.toHaveProperty('concurrency')
    else expect(writes[0]!.body.ruby_retry?.concurrency).toBe(concurrency)
  })
}

test('ruby concurrency omission survives editing a legacy plan', async ({ page }) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()])
  const drawer = await openPlan(page)
  await expectRubySettingsHidden(drawer)
  await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
  await expect(drawer.getByLabel('注音请求并发上限', { exact: true })).toHaveValue('')
  await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('concurrency')
  expect(writes[0]!.body.rounds[0]!.concurrency).toBe(2)
})

test('ruby concurrency stays independent when the main round and retry switch change', async ({
  page,
}) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()], {
    enabled: true,
    backend_id: 2,
    max_attempts: 3,
    concurrency: 7,
  })
  const drawer = await openPlan(page)
  await expect(drawer.getByLabel('注音请求并发上限', { exact: true })).toHaveValue('7')
  await fillNumber(
    drawer.getByTestId('execution-round').getByLabel('并发数', { exact: true }),
    '11',
  )
  const retrySwitch = drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true })
  await retrySwitch.click()
  await expectRubySettingsHidden(drawer)
  await retrySwitch.click()
  await expect(drawer.locator('.n-select').filter({ hasText: '第二模型' })).toBeVisible()
  await expect(drawer.getByLabel('重试轮数', { exact: true })).toHaveValue('3')
  await expect(drawer.getByLabel('注音请求并发上限', { exact: true })).toHaveValue('7')
  await retrySwitch.click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry).toEqual({
    enabled: false,
    backend_id: 2,
    max_attempts: 3,
    concurrency: 7,
  })
  expect(writes[0]!.body.rounds[0]!.concurrency).toBe(11)
})

test('ruby concurrency clearing removes an existing explicit value', async ({ page }) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()], {
    enabled: true,
    concurrency: 3,
  })
  const drawer = await openPlan(page)
  const field = drawer.getByLabel('注音请求并发上限', { exact: true })
  await expect(field).toHaveValue('3')
  await fillNumber(field, '')
  await expect(field).toHaveAttribute('placeholder', '默认（1）')
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('concurrency')
})

for (const concurrency of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
  test(`ruby imported invalid concurrency ${concurrency} remains correctable after enabling retry`, async ({
    page,
  }) => {
    const { writes } = await setup(page, createProfileConfig(), [translateRound()], {
      enabled: false,
      concurrency,
    })
    const drawer = await openPlan(page)
    const field = drawer.getByLabel('注音请求并发上限', { exact: true })
    const save = drawer.getByRole('button', { name: '保存', exact: true })
    await expectRubySettingsHidden(drawer)
    await expect(save).toBeDisabled()
    await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
    await expect(field).toHaveValue(String(concurrency))
    await expect(field).toHaveAttribute('aria-invalid', 'true')
    await expect(drawer.getByRole('alert')).toHaveText(
      '注音并发必须为可安全表示的正整数，或留空使用默认值。',
    )
    await expect(save).toBeDisabled()
    expect(writes).toHaveLength(0)
    await fillNumber(field, '101')
    await expect(save).toBeEnabled()
    await save.click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]!.body.ruby_retry?.concurrency).toBe(101)
  })
}

for (const concurrency of [undefined, 8]) {
  test(`ruby concurrency ${concurrency ?? 'default'} survives copying a personal plan to an organization`, async ({
    page,
  }) => {
    const { profile, writes } = await setup(
      page,
      createProfileConfig(),
      [
        {
          mode: 'correct',
          concurrency: 1,
          correct: { rules: [{ name: 'width_mix_normalize', enabled: true }] },
        },
      ],
      { enabled: true, backend_id: 2, ...(concurrency === undefined ? {} : { concurrency }) },
    )
    profile.scope = 'system'
    await page.goto('/execution-plan-templates')
    await page.getByRole('button', { name: '复制到组织', exact: true }).click()
    const modal = page.locator('.n-modal:visible')
    await chooseOption(page, modal.locator('.n-select'), '目标组织')
    await modal.getByRole('button', { name: '继续编辑', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await expect(drawer.getByLabel('注音请求并发上限', { exact: true })).toHaveValue(
      concurrency === undefined ? '' : String(concurrency),
    )
    await expect(drawer.getByRole('button', { name: '创建计划', exact: true })).toBeEnabled()
    await drawer.getByRole('button', { name: '创建计划', exact: true }).click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]!.body.org_id).toBe(7)
    expect(writes[0]!.body.ruby_retry).not.toHaveProperty('backend_id')
    if (concurrency === undefined)
      expect(writes[0]!.body.ruby_retry).not.toHaveProperty('concurrency')
    else expect(writes[0]!.body.ruby_retry?.concurrency).toBe(concurrency)
  })
}

const rubyBatchFields = [
  { key: 'batch_size', label: '每次注音最多段数', placeholder: '默认（1）' },
  { key: 'max_words_per_batch', label: '每次注音最多字词数', placeholder: '默认（0，不限）' },
  { key: 'batch_wait_ms', label: '最大凑批等待时间（毫秒）', placeholder: '默认（25）' },
] as const
const explicitRubyBatch = {
  enabled: true,
  concurrency: 7,
  batch_size: 4,
  max_words_per_batch: 800,
  batch_wait_ms: 25,
}

for (const settings of [
  undefined,
  { batch_size: 4, max_words_per_batch: 0, batch_wait_ms: 25 },
  { batch_size: 0, max_words_per_batch: 800, batch_wait_ms: 0 },
]) {
  test(`P2 ruby new plan retains ${settings ? JSON.stringify(settings) : 'omitted defaults'}`, async ({
    page,
  }) => {
    const { writes } = await setup(page)
    await page.goto('/execution-plan-templates')
    await page.getByRole('button', { name: '新建计划', exact: true }).click()
    const drawer = page.locator('.n-drawer:visible')
    await drawer.getByRole('textbox').first().fill('P2 注音合批计划')
    await chooseOption(
      page,
      drawer.locator('.n-select').filter({ hasText: '选择计划引用的执行策略' }),
      '策略样本',
    )
    await chooseRoundMode(drawer.getByTestId('execution-round'), '本地改写')
    await expectRubySettingsHidden(drawer)
    await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
    const toggle = drawer.getByRole('button', { name: '合批设置', exact: true })
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await expect(drawer.getByLabel(rubyBatchFields[0].label, { exact: true })).toBeHidden()
    await toggle.focus()
    await toggle.press('Enter')
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    for (const field of rubyBatchFields) {
      const input = drawer.getByLabel(field.label, { exact: true })
      await expect(input).toHaveValue('')
      await expect(input).toHaveAttribute('placeholder', field.placeholder)
      if (settings) await fillNumber(input, String(settings[field.key]))
    }
    await drawer.getByRole('button', { name: '创建计划', exact: true }).click()
    await expect.poll(() => writes.length).toBe(1)
    for (const field of rubyBatchFields) {
      if (settings) expect(writes[0]!.body.ruby_retry?.[field.key]).toBe(settings[field.key])
      else expect(writes[0]!.body.ruby_retry).not.toHaveProperty(field.key)
    }
  })
}

test('P2 ruby legacy edit does not materialize displayed defaults', async ({ page }) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()])
  const drawer = await openPlan(page)
  await expectRubySettingsHidden(drawer)
  await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
  await expect(drawer.getByRole('button', { name: '合批设置', exact: true })).toHaveAttribute(
    'aria-expanded',
    'false',
  )
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  for (const field of rubyBatchFields)
    expect(writes[0]!.body.ruby_retry).not.toHaveProperty(field.key)
})

for (const field of rubyBatchFields) {
  test(`P2 ruby ${field.key} alone updates without losing other values or reopening collapsed drafts`, async ({
    page,
  }) => {
    const { writes } = await setup(
      page,
      createProfileConfig(),
      [translateRound()],
      explicitRubyBatch,
    )
    const drawer = await openPlan(page)
    const toggle = drawer.getByRole('button', { name: '合批设置', exact: true })
    const retrySwitch = drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true })
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await fillNumber(drawer.getByLabel(field.label, { exact: true }), '0')
    await retrySwitch.click()
    await expectRubySettingsHidden(drawer)
    await retrySwitch.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    await expect(drawer.getByLabel(field.label, { exact: true })).toHaveValue('0')
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await fillNumber(drawer.getByLabel('注音请求并发上限', { exact: true }), '8')
    await retrySwitch.click()
    await expectRubySettingsHidden(drawer)
    await retrySwitch.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
    await toggle.click()
    await expect(drawer.getByLabel(field.label, { exact: true })).toHaveValue('0')
    await retrySwitch.click()
    await drawer.getByRole('button', { name: '保存', exact: true }).click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]!.body.ruby_retry).toEqual({
      ...explicitRubyBatch,
      max_attempts: 1,
      enabled: false,
      concurrency: 8,
      [field.key]: 0,
    })
    expect(writes[0]!.body.rounds[0]!.concurrency).toBe(2)
  })

  test(`P2 ruby clearing ${field.key} restores omission`, async ({ page }) => {
    const { writes } = await setup(
      page,
      createProfileConfig(),
      [translateRound()],
      explicitRubyBatch,
    )
    const drawer = await openPlan(page)
    await fillNumber(drawer.getByLabel(field.label, { exact: true }), '')
    await drawer.getByRole('button', { name: '保存', exact: true }).click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]!.body.ruby_retry).not.toHaveProperty(field.key)
    for (const other of rubyBatchFields.filter((item) => item !== field))
      expect(writes[0]!.body.ruby_retry?.[other.key]).toBe(explicitRubyBatch[other.key])
  })
}

test('P2 ruby combined limits remain validated and correctable after enabling retry', async ({
  page,
}) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()], {
    enabled: false,
    batch_size: 0,
  })
  const drawer = await openPlan(page)
  const save = drawer.getByRole('button', { name: '保存', exact: true })
  await expectRubySettingsHidden(drawer)
  await expect(save).toBeDisabled()
  await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
  await expect(drawer.getByRole('button', { name: '合批设置', exact: true })).toHaveAttribute(
    'aria-expanded',
    'true',
  )
  await expect(drawer.getByRole('alert')).toHaveText(
    '段数上限和字词数上限不能同时为 0；留空分别使用默认值 1 和 0。',
  )
  await expect(save).toBeDisabled()
  for (const field of rubyBatchFields.slice(0, 2))
    await expect(drawer.getByLabel(field.label, { exact: true })).toHaveAttribute(
      'aria-invalid',
      'true',
    )
  await fillNumber(drawer.getByLabel(rubyBatchFields[0].label, { exact: true }), '')
  await expect(save).toBeEnabled()
  await fillNumber(drawer.getByLabel(rubyBatchFields[0].label, { exact: true }), '0')
  await fillNumber(drawer.getByLabel(rubyBatchFields[1].label, { exact: true }), '0')
  await expect(save).toBeDisabled()
  await expect(drawer.getByRole('alert')).toHaveText(
    '段数上限和字词数上限不能同时为 0；留空分别使用默认值 1 和 0。',
  )
  expect(writes).toHaveLength(0)
  await fillNumber(drawer.getByLabel(rubyBatchFields[1].label, { exact: true }), '800')
  await expect(save).toBeEnabled()
  await save.click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry).toMatchObject({ batch_size: 0, max_words_per_batch: 800 })
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('batch_wait_ms')
})

test('P2 ruby imported invalid batch settings remain correctable after enabling retry', async ({
  page,
}) => {
  const invalidBatch = { batch_size: -1, max_words_per_batch: 1.5, batch_wait_ms: 9223372036855 }
  const { writes } = await setup(page, createProfileConfig(), [translateRound()], {
    enabled: false,
    ...invalidBatch,
  })
  const drawer = await openPlan(page)
  const save = drawer.getByRole('button', { name: '保存', exact: true })
  await expectRubySettingsHidden(drawer)
  await expect(save).toBeDisabled()
  await drawer.getByRole('switch', { name: '启用注音对齐重试', exact: true }).click()
  await expect(drawer.getByRole('button', { name: '合批设置', exact: true })).toHaveAttribute(
    'aria-expanded',
    'true',
  )
  for (const field of rubyBatchFields) {
    const input = drawer.getByLabel(field.label, { exact: true })
    await expect(input).toHaveValue(String(invalidBatch[field.key]))
    await expect(input).toHaveAttribute('aria-invalid', 'true')
    const ids = (await input.getAttribute('aria-describedby'))!.split(' ')
    for (const id of ids) await expect(drawer.locator(`[id="${id}"]`)).toBeVisible()
  }
  await expect(save).toBeDisabled()
  expect(writes).toHaveLength(0)
  for (const field of rubyBatchFields)
    await fillNumber(drawer.getByLabel(field.label, { exact: true }), '')
  await fillNumber(drawer.getByLabel(rubyBatchFields[2].label, { exact: true }), '9223372036854')
  await expect(save).toBeEnabled()
  await save.click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry?.batch_wait_ms).toBe(9223372036854)
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('batch_size')
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('max_words_per_batch')
})

test('ruby number inputs clamp entered values and keep keyboard and stepper changes within bounds', async ({
  page,
}) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()], explicitRubyBatch)
  const drawer = await openPlan(page)
  const fields = [
    { label: '重试轮数', min: 1, max: 10 },
    { label: '注音请求并发上限', min: 1, max: Number.MAX_SAFE_INTEGER },
    ...rubyBatchFields.map((field) => ({
      label: field.label,
      min: 0,
      max: field.key === 'batch_wait_ms' ? 9223372036854 : Number.MAX_SAFE_INTEGER,
    })),
  ]
  for (const field of fields) {
    const input = drawer.getByLabel(field.label, { exact: true })
    const buttons = drawer
      .locator('.n-input-number')
      .filter({ has: page.getByLabel(field.label, { exact: true }) })
      .getByRole('button')
    await fillNumber(input, '-2')
    await expect(input).toHaveValue(String(field.min))
    await expect(buttons.first()).toBeDisabled()
    await input.press('ArrowDown')
    await expect(input).toHaveValue(String(field.min))
    await input.press('ArrowUp')
    await expect(input).toHaveValue(String(field.min + 1))
    await buttons.first().click()
    await expect(input).toHaveValue(String(field.min))
    await expect(buttons.first()).toBeDisabled()
    await fillNumber(input, String(field.max + 1))
    await expect(input).toHaveValue(String(field.max))
    await expect(buttons.last()).toBeDisabled()
    await input.press('ArrowUp')
    await expect(input).toHaveValue(String(field.max))
    await fillNumber(input, '1.5')
    await expect(input).toHaveValue('2')
  }
  await fillNumber(drawer.getByLabel('重试轮数', { exact: true }), '')
  await expect(drawer.getByLabel('重试轮数', { exact: true })).toHaveValue('1')
  await expect(drawer.getByRole('alert')).toHaveCount(0)
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.ruby_retry).toEqual({
    enabled: true,
    max_attempts: 1,
    concurrency: 2,
    batch_size: 2,
    max_words_per_batch: 2,
    batch_wait_ms: 2,
  })
})

test('P2 ruby organization copy retains zero and batching settings after clearing private dependencies', async ({
  page,
}) => {
  const retry = {
    enabled: true,
    backend_id: 2,
    batch_size: 0,
    max_words_per_batch: 800,
    batch_wait_ms: 0,
  }
  const { profile, writes, plan } = await setup(
    page,
    createProfileConfig(),
    [
      {
        mode: 'correct',
        concurrency: 1,
        correct: { rules: [{ name: 'width_mix_normalize', enabled: true }] },
      },
    ],
    retry,
  )
  profile.scope = 'system'
  await page.goto('/execution-plan-templates')
  await page.getByRole('button', { name: '复制到组织', exact: true }).click()
  const modal = page.locator('.n-modal:visible')
  await chooseOption(page, modal.locator('.n-select'), '目标组织')
  await modal.getByRole('button', { name: '继续编辑', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  await expect(drawer.getByRole('button', { name: '合批设置', exact: true })).toHaveAttribute(
    'aria-expanded',
    'true',
  )
  for (const field of rubyBatchFields)
    await expect(drawer.getByLabel(field.label, { exact: true })).toHaveValue(
      String(retry[field.key]),
    )
  await drawer.getByRole('button', { name: '创建计划', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.org_id).toBe(7)
  expect(writes[0]!.body.ruby_retry).not.toHaveProperty('backend_id')
  for (const field of rubyBatchFields)
    expect(writes[0]!.body.ruby_retry?.[field.key]).toBe(retry[field.key])
  expect(plan.ruby_retry).toEqual(retry)
})

async function rubyBatchLayout(drawer: Locator) {
  const toggle = drawer.getByRole('button', { name: '合批设置', exact: true })
  await toggle.focus()
  await toggle.press('Enter')
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await toggle.press('Space')
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  const first = drawer.getByLabel(rubyBatchFields[0].label, { exact: true })
  await first.focus()
  await first.press('Tab')
  await expect(drawer.getByLabel(rubyBatchFields[1].label, { exact: true })).toBeFocused()
  for (const field of rubyBatchFields) {
    const input = drawer.getByLabel(field.label, { exact: true })
    await input.scrollIntoViewIfNeeded()
    await expect(input).toBeVisible()
    const control = (await input.boundingBox())!
    const bounds = (await drawer.boundingBox())!
    const footer = (await drawer.locator('.n-drawer-footer').boundingBox())!
    expect(control.x).toBeGreaterThanOrEqual(bounds.x - 1)
    expect(control.x + control.width).toBeLessThanOrEqual(bounds.x + bounds.width + 1)
    expect(control.y + control.height).toBeLessThanOrEqual(footer.y + 1)
  }
  const panelId = await toggle.getAttribute('aria-controls')
  const panel = drawer.locator(`[id="${panelId}"]`)
  expect(await panel.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(
    true,
  )
  return panel
}

for (const theme of ['light', 'dark'] as const) {
  test(`P2 ruby ${theme} configuration reflows with keyboard access`, async ({
    page,
  }, testInfo) => {
    await setup(page, createProfileConfig(), [translateRound()], explicitRubyBatch, theme)
    const drawer = await openPlan(page)
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    const panel = await rubyBatchLayout(drawer)
    await mkdir('tests/artifacts/p2-ruby', { recursive: true })
    await panel.screenshot({
      path: `tests/artifacts/p2-ruby/config-${theme}-${testInfo.project.name}.png`,
    })
  })
}

test('P2 ruby configuration remains operable at real 200 percent browser zoom', async ({
  browserName,
}, testInfo) => {
  test.skip(
    testInfo.project.name !== 'chromium' || browserName !== 'chromium',
    'Actual Chromium zoom is exercised once',
  )
  const extension = testInfo.outputPath('ruby-zoom-extension')
  await mkdir(extension, { recursive: true })
  await writeFile(
    `${extension}/manifest.json`,
    JSON.stringify({
      manifest_version: 3,
      name: 'Ruby configuration zoom verification',
      version: '1.0',
      permissions: ['tabs'],
      background: { service_worker: 'worker.js' },
    }),
  )
  await writeFile(`${extension}/worker.js`, 'chrome.runtime.onInstalled.addListener(() => {});')
  const context = await chromium.launchPersistentContext('', {
    channel: 'chromium',
    executablePath: process.env.LINGUAFLOW_TEST_CHROMIUM,
    headless: true,
    baseURL: 'http://127.0.0.1:4173',
    viewport: { width: 1440, height: 1100 },
    deviceScaleFactor: 1,
    args: [`--disable-extensions-except=${extension}`, `--load-extension=${extension}`],
  })
  try {
    const worker = context.serviceWorkers()[0] ?? (await context.waitForEvent('serviceworker'))
    const page = await context.newPage()
    await setup(page, createProfileConfig(), [translateRound()], explicitRubyBatch, 'dark')
    const drawer = await openPlan(page)
    const zoom = await worker.evaluate(async () => {
      const api = (
        globalThis as unknown as {
          chrome: {
            tabs: {
              query(query: { url: string }): Promise<{ id: number }[]>
              setZoom(id: number, factor: number): Promise<void>
              getZoom(id: number): Promise<number>
            }
          }
        }
      ).chrome
      const [tab] = await api.tabs.query({ url: 'http://127.0.0.1:4173/*' })
      await api.tabs.setZoom(tab!.id, 2)
      return api.tabs.getZoom(tab!.id)
    })
    expect(zoom).toBe(2)
    await page.waitForFunction(() => innerWidth === 720 && devicePixelRatio === 2)
    const panel = await rubyBatchLayout(drawer)
    await mkdir('tests/artifacts/p2-ruby', { recursive: true })
    await panel.scrollIntoViewIfNeeded()
    // Element clipping uses unzoomed coordinates in Chromium; capture the actual zoomed viewport.
    await page.screenshot({ path: 'tests/artifacts/p2-ruby/config-dark-200pct.png' })
  } finally {
    await context.close()
  }
})

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
  expect(writes[0]!.body.config).not.toHaveProperty('glossary')
})

test('legacy profile glossary is ignored during editing and never submitted', async ({ page }) => {
  const config = {
    ...createProfileConfig(),
    glossary: { bootstrap: { enabled: true, max_terms_per_1000_chars: 8 } },
  }
  const { writes } = await setup(page, config)
  const drawer = await openProfile(page)
  await expect(drawer.getByRole('switch', { name: '启用内联自举', exact: true })).toHaveCount(0)
  await drawer.getByRole('switch', { name: '启用上下文窗口', exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.config).toEqual({ schema_version: 1, context: { enabled: false } })
  expect(config.glossary.bootstrap.enabled).toBe(true)
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

test('opening advanced settings does not create an omitted inline extraction object', async ({
  page,
}) => {
  const { writes } = await setup(page, createProfileConfig(), [translateRound()])
  const drawer = await openPlan(page)
  const round = drawer.getByTestId('execution-round')
  await showAdvanced(round)
  await expect(round.getByRole('switch', { name: '翻译时提取术语', exact: true })).not.toBeChecked()
  await expect(round.getByLabel('每千源文字词最大术语数', { exact: true })).toHaveCount(0)
  await round.getByText('高级配置', { exact: true }).click()
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds[0]!.translate).not.toHaveProperty('inline_term_extraction')
})

test('partial inline objects receive defaults while explicit disabled parameters round-trip', async ({
  page,
}) => {
  const disabled: InlineConfig = {
    enabled: false,
    max_terms_per_1000_words: 0.123456789,
    min_source_len: 4,
    conflict_strategy: 'off',
  }
  const { writes } = await setup(page, createProfileConfig(), [
    translateRound({}),
    translateRound({ enabled: true }),
    translateRound(disabled),
  ])
  const drawer = await openPlan(page)
  const rounds = drawer.getByTestId('execution-round')
  for (const round of await rounds.all()) await showAdvanced(round)
  await expect(
    rounds.nth(0).getByRole('switch', { name: '翻译时提取术语', exact: true }),
  ).not.toBeChecked()
  await expect(
    rounds.nth(1).getByRole('switch', { name: '翻译时提取术语', exact: true }),
  ).toBeChecked()
  await expect(rounds.nth(2).getByLabel('每千源文字词最大术语数', { exact: true })).toHaveValue(
    '0.123456789',
  )
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds.map((round) => round.translate?.inline_term_extraction)).toEqual([
    inlineDefaults,
    { ...inlineDefaults, enabled: true },
    disabled,
  ])
})

test('inline extraction toggles retain parameters and invalid disabled values block saving', async ({
  page,
}, testInfo) => {
  const other = { ...inlineDefaults, enabled: true, max_terms_per_1000_words: 2.25 }
  const { writes } = await setup(page, createProfileConfig(), [
    translateRound(),
    translateRound(other),
  ])
  const drawer = await openPlan(page)
  const round = drawer.getByTestId('execution-round').first()
  await showAdvanced(round)
  const toggle = round.getByRole('switch', { name: '翻译时提取术语', exact: true })
  await toggle.click()
  const density = round.getByLabel('每千源文字词最大术语数', { exact: true })
  const length = round.getByLabel('最短源术语长度', { exact: true })
  await expect(density).toHaveValue('3')
  await expect(length).toHaveValue('2')
  await fillNumber(density, '7.5')
  await fillNumber(length, '4')
  await chooseOption(page, round.getByLabel('冲突处理', { exact: true }), '保留本批译文')
  await toggle.click()
  await expect(density).toHaveValue('7.5')
  await expect(density).toBeEnabled()
  await fillNumber(density, '0')
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
  await fillNumber(density, '7.5')
  await fillNumber(length, '1.5')
  await expect(drawer.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
  expect(writes).toHaveLength(0)
  await fillNumber(length, '4')
  await toggle.click()
  await expect(density).toHaveValue('7.5')
  await expect(length).toHaveValue('4')
  await expect(round.getByLabel('冲突处理', { exact: true })).toContainText('保留本批译文')
  await expect(round.getByTestId('inline-term-extraction-badge')).toBeVisible()
  await expect(
    round.locator('.fade-down-transition-enter-active, .fade-down-transition-leave-active'),
  ).toHaveCount(0)
  await round.getByTestId('inline-term-extraction').screenshot({
    animations: 'disabled',
    path: testInfo.outputPath('inline-term-extraction.png'),
  })
  await toggle.click()
  await expect(round.getByTestId('inline-term-extraction-badge')).toHaveCount(0)
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds.map((item) => item.translate?.inline_term_extraction)).toEqual([
    { enabled: false, max_terms_per_1000_words: 7.5, min_source_len: 4, conflict_strategy: 'off' },
    other,
  ])
})

test('switching round modes restores complete drafts and submits only the active mode', async ({
  page,
}) => {
  const original = translateRound({
    enabled: true,
    max_terms_per_1000_words: 7.5,
    min_source_len: 4,
    conflict_strategy: 'off',
  })
  const { writes } = await setup(page, createProfileConfig(), [original])
  const drawer = await openPlan(page)
  const round = drawer.getByTestId('execution-round')
  await chooseRoundMode(round, '术语抽取')
  await chooseOption(page, round.getByLabel('AI 后端', { exact: true }), '第二模型')
  await fillNumber(round.getByLabel('并发数', { exact: true }), '5')
  await chooseOption(page, round.getByLabel('术语抽取模板', { exact: true }), '术语提示词样本')
  await fillNumber(round.getByLabel('术语最短源文长度', { exact: true }), '3')
  await fillNumber(round.getByLabel('段落数上限', { exact: true }), '4')
  await fillNumber(round.getByLabel('字词数上限', { exact: true }), '40')
  await fillNumber(round.getByLabel('每千字术语抽取系数', { exact: true }), '13')
  await showAdvanced(round)
  await fillNumber(round.getByLabel('最大重试次数', { exact: true }), '4')
  await fillNumber(round.getByLabel('重试退避间隔（毫秒）', { exact: true }), '400')
  await expect(round.getByTestId('inline-term-extraction')).toHaveCount(0)

  await chooseRoundMode(round, '翻译')
  await showAdvanced(round)
  await expect(round.getByLabel('AI 后端', { exact: true })).toContainText('模型样本')
  await expect(round.getByLabel('并发数', { exact: true })).toHaveValue('2')
  await expect(round.getByLabel('批次大小', { exact: true })).toHaveValue('7')
  await expect(round.getByLabel('每千源文字词最大术语数', { exact: true })).toHaveValue('7.5')
  await expect(round.getByLabel('最大重试次数', { exact: true })).toHaveValue('2')
  await expect(round.getByLabel('重试退避间隔（毫秒）', { exact: true })).toHaveValue('100')
  await expect(round.getByRole('switch', { name: '启用抖动', exact: true })).not.toBeChecked()

  await chooseRoundMode(round, '术语抽取')
  await showAdvanced(round)
  await expect(round.getByLabel('AI 后端', { exact: true })).toContainText('第二模型')
  for (const [label, value] of [
    ['并发数', '5'],
    ['术语最短源文长度', '3'],
    ['段落数上限', '4'],
    ['字词数上限', '40'],
    ['每千字术语抽取系数', '13'],
    ['最大重试次数', '4'],
    ['重试退避间隔（毫秒）', '400'],
  ] as const)
    await expect(round.getByLabel(label, { exact: true })).toHaveValue(value)
  await expect(round.getByLabel('术语抽取模板', { exact: true })).toContainText('术语提示词样本')
  await chooseRoundMode(round, '翻译')
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds).toEqual([original])
  expect(writes[0]!.body.rounds[0]).not.toHaveProperty('extract')
})

test('reordering rounds keeps each dormant translate draft attached to its own round', async ({
  page,
}) => {
  const first = translateRound({ ...inlineDefaults, enabled: true, max_terms_per_1000_words: 7.5 })
  const second = translateRound({
    ...inlineDefaults,
    enabled: false,
    max_terms_per_1000_words: 2.25,
  })
  const { writes } = await setup(page, createProfileConfig(), [first, second])
  const drawer = await openPlan(page)
  const rounds = drawer.getByTestId('execution-round')
  await chooseRoundMode(rounds.nth(0), '术语抽取')
  await chooseRoundMode(rounds.nth(1), '质量裁决')
  await rounds.nth(0).getByRole('button', { name: '下移', exact: true }).click()
  for (const round of await rounds.all()) {
    await chooseRoundMode(round, '翻译')
    await showAdvanced(round)
  }
  await expect(rounds.nth(0).getByLabel('每千源文字词最大术语数', { exact: true })).toHaveValue(
    '2.25',
  )
  await expect(rounds.nth(1).getByLabel('每千源文字词最大术语数', { exact: true })).toHaveValue(
    '7.5',
  )
  await drawer.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => writes.length).toBe(1)
  expect(writes[0]!.body.rounds).toEqual([second, first])
})

test('closing a plan drawer discards unsaved active and dormant mode drafts', async ({ page }) => {
  const original = translateRound({
    ...inlineDefaults,
    enabled: true,
    max_terms_per_1000_words: 7.5,
  })
  const { writes, plan } = await setup(page, createProfileConfig(), [original])
  let drawer = await openPlan(page)
  let round = drawer.getByTestId('execution-round')
  await showAdvanced(round)
  await fillNumber(round.getByLabel('每千源文字词最大术语数', { exact: true }), '99')
  await chooseRoundMode(round, '术语抽取')
  await fillNumber(round.getByLabel('术语最短源文长度', { exact: true }), '8')
  await drawer.getByRole('button', { name: '取消', exact: true }).click()
  await expect(page.locator('.n-drawer:visible')).toHaveCount(0)
  await page.getByRole('button', { name: '编辑', exact: true }).click()
  drawer = page.locator('.n-drawer:visible')
  round = drawer.getByTestId('execution-round')
  await showAdvanced(round)
  await expect(round.getByLabel('每千源文字词最大术语数', { exact: true })).toHaveValue('7.5')
  await chooseRoundMode(round, '术语抽取')
  await expect(round.getByLabel('术语最短源文长度', { exact: true })).toHaveValue('2')
  expect(plan.rounds).toEqual([original])
  expect(writes).toHaveLength(0)
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
