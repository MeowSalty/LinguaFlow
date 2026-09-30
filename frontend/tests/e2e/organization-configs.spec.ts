import { expect, test, type Locator, type Page } from '@playwright/test'
import { json, mockApp } from './fixtures'

type Kind = 'prompt' | 'bootstrap' | 'prune' | 'profile' | 'plan'
type Entity = {
  id: number
  name: string
  scope: 'user' | 'org' | 'system'
  owner_org_id?: number
  owner_user_id?: number
  [key: string]: unknown
}
type Write = { method: string; path: string; body: Record<string, unknown> | null }
const specs = [
  {
    kind: 'prompt',
    path: '/prompt-templates',
    api: '/translation-prompt-templates',
    create: '新建模板',
    submit: '创建模板',
    field: 'system_prompt_content',
  },
  {
    kind: 'bootstrap',
    path: '/bootstrap-prompt-templates',
    api: '/bootstrap-prompt-templates',
    create: '新建模板',
    submit: '创建模板',
    field: 'content',
  },
  {
    kind: 'prune',
    path: '/prune-prompt-templates',
    api: '/prune-prompt-templates',
    create: '新建模板',
    submit: '创建模板',
    field: 'content',
  },
  {
    kind: 'profile',
    path: '/execution-profiles',
    api: '/execution-profiles',
    create: '新建策略',
    submit: '创建策略',
    field: 'config',
  },
  {
    kind: 'plan',
    path: '/execution-plan-templates',
    api: '/execution-plan-templates',
    create: '新建计划',
    submit: '创建计划',
    field: 'rounds',
  },
] as const
const profileConfig = {
  protect: { enabled: true, rules: ['code'] },
  postprocess: { enabled: true, trim_spaces: true },
  repair: { enabled: false },
  glossary: { bootstrap: { enabled: false } },
  context: { enabled: false },
}
const display = (kind: string, scope: 'user' | 'org' | 'system', orgId = 7) =>
  `${scope === 'user' ? '私人' : scope === 'system' ? '系统' : orgId === 7 ? '目标组织' : '其他组织'}-${kind}`
function entity(kind: Kind | 'backend', scope: 'user' | 'org' | 'system', orgId = 7): Entity {
  const id = scope === 'system' ? -1 : scope === 'user' ? 101 : orgId === 7 ? 701 : 801
  const common = {
    id,
    name: display(kind, scope, orgId),
    scope,
    ...(scope === 'org' ? { owner_org_id: orgId } : scope === 'user' ? { owner_user_id: 1 } : {}),
    description: 'original description',
    created_at: '2026-09-30T00:00:00Z',
    updated_at: '2026-09-30T00:00:00Z',
  }
  if (kind === 'profile') return { ...common, config: structuredClone(profileConfig) }
  if (kind === 'plan')
    return {
      ...common,
      profile_id: id,
      ruby_retry: { enabled: false, backend_id: id, max_attempts: 1 },
      rounds: [
        {
          mode: 'translate',
          backend_id: id,
          concurrency: 3,
          translate: {
            prompt_template_id: id,
            batch_size: 10,
            max_words_per_batch: 0,
            fallback_shrink: 1,
          },
        },
      ],
    }
  if (kind === 'backend')
    return {
      ...common,
      type: 'openai',
      options: { api_key: 'fixture-key', model: 'fixture-model' },
      rate_limit_per_minute: 0,
    }
  return {
    ...common,
    [kind === 'prompt' ? 'system_prompt_content' : 'content']:
      'Preserved source instructions {{.SourceLang}}',
  }
}

async function configApp(page: Page, role: 'owner' | 'member' = 'owner') {
  await mockApp(page, { role: 'user' })
  const data = Object.fromEntries(
    specs.map((spec) => [
      spec.kind,
      [
        entity(spec.kind, 'system'),
        entity(spec.kind, 'user'),
        entity(spec.kind, 'org', 7),
        entity(spec.kind, 'org', 8),
      ],
    ]),
  ) as Record<Kind, Entity[]>
  const backends = [
    entity('backend', 'user'),
    entity('backend', 'org', 7),
    entity('backend', 'org', 8),
  ]
  const writes: Write[] = []
  let nextId = 1000
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      url = new URL(request.url()),
      path = url.pathname.replace('/api/v1', '')
    if (path === '/orgs')
      return json(route, {
        items: [7, 8].map((id) => ({
          id,
          name: `Organization ${id}`,
          slug: `org-${id}`,
          display_name: `Organization ${id}`,
          current_user_role: role,
        })),
      })
    const backend = /^\/orgs\/(7|8)\/backends(?:\/(\d+))?$/.exec(path)
    if (path === '/backends' || backend) {
      const orgId = backend ? Number(backend[1]) : null,
        id = backend?.[2] ? Number(backend[2]) : null
      if (request.method() === 'GET')
        return json(route, {
          items: backends.filter((item) =>
            orgId == null ? item.scope === 'user' : item.owner_org_id === orgId,
          ),
        })
      const body = request.postData() ? (request.postDataJSON() as Record<string, unknown>) : null
      writes.push({ method: request.method(), path, body })
      if (request.method() === 'POST') {
        const item = { ...entity('backend', 'org', orgId!), ...body, id: nextId++ } as Entity
        backends.push(item)
        return json(route, item, 201)
      }
      const index = backends.findIndex((item) => item.id === id)
      if (request.method() === 'PUT') {
        backends[index] = { ...backends[index]!, ...body }
        return json(route, backends[index])
      }
      if (request.method() === 'DELETE') {
        backends.splice(index, 1)
        return route.fulfill({ status: 204 })
      }
    }
    const spec = specs.find((value) => path === value.api || path.startsWith(`${value.api}/`))
    if (!spec) return route.fallback()
    const collection = data[spec.kind],
      id = path === spec.api ? null : Number(path.slice(spec.api.length + 1))
    if (request.method() === 'GET') {
      const scope = url.searchParams.get('org_id')
      return id == null
        ? json(route, {
            items: scope
              ? collection.filter((item) => item.owner_org_id === Number(scope))
              : collection,
          })
        : json(
            route,
            collection.find((item) => item.id === id),
          )
    }
    const body = request.postData() ? (request.postDataJSON() as Record<string, unknown>) : null
    writes.push({ method: request.method(), path, body })
    if (request.method() === 'POST') {
      const item = {
        ...entity(spec.kind, body?.org_id ? 'org' : 'user', Number(body?.org_id ?? 7)),
        ...body,
        id: nextId++,
      } as Entity
      delete item.org_id
      collection.push(item)
      return json(route, item, 201)
    }
    const index = collection.findIndex((item) => item.id === id)
    if (request.method() === 'PUT') {
      collection[index] = { ...collection[index]!, ...body }
      return json(route, collection[index])
    }
    if (request.method() === 'DELETE') {
      collection.splice(index, 1)
      return route.fulfill({ status: 204 })
    }
    return route.fallback()
  })
  return { data, backends, writes }
}

const drawer = (page: Page) => page.locator('.n-drawer:visible')
const card = (page: Page, name: string) =>
  page
    .locator('.lf-interactive-card')
    .filter({ has: page.getByRole('heading', { name, exact: true }) })
async function selectOption(page: Page, select: Locator, value: string, forbidden: string[] = []) {
  await select.click()
  const menu = page.locator('.n-base-select-menu:visible').last()
  for (const label of forbidden) await expect(menu.getByText(label, { exact: true })).toHaveCount(0)
  await menu.getByText(value, { exact: true }).click()
}
async function planDependencies(page: Page) {
  const selects = drawer(page).locator('.n-select:visible')
  await selectOption(page, selects.nth(0), display('profile', 'system'), [
    display('profile', 'user'),
    display('profile', 'org', 8),
  ])
  await selectOption(page, selects.nth(1), display('backend', 'org'), [
    display('backend', 'user'),
    display('backend', 'org', 8),
  ])
  await selectOption(page, selects.nth(2), display('prompt', 'system'), [
    display('prompt', 'user'),
    display('prompt', 'org', 8),
  ])
}
function expectOwnership(body: Record<string, unknown> | null, create: boolean) {
  expect(body).not.toBeNull()
  expect(body).not.toHaveProperty('scope')
  expect(body).not.toHaveProperty('owner_org_id')
  expect(body).not.toHaveProperty('owner_user_id')
  if (create) expect(body).toHaveProperty('org_id', 7)
  else expect(body).not.toHaveProperty('org_id')
}

for (const spec of specs) {
  test(`${spec.kind}: organization create, update and delete preserve ownership`, async ({
    page,
  }) => {
    const state = await configApp(page)
    await page.goto(`${spec.path}?org_id=7`)
    await page.getByRole('button', { name: spec.create, exact: true }).click()
    await drawer(page).locator('input').first().fill(`Created ${spec.kind}`)
    if (['prompt', 'bootstrap', 'prune'].includes(spec.kind))
      await drawer(page).locator('textarea').last().fill('Created instructions {{.SourceLang}}')
    if (spec.kind === 'plan') await planDependencies(page)
    await drawer(page).getByRole('button', { name: spec.submit, exact: true }).click()
    await expect.poll(() => state.writes.filter((write) => write.method === 'POST').length).toBe(1)
    const createdWrite = state.writes.find((write) => write.method === 'POST')!
    expectOwnership(createdWrite.body, true)
    if (spec.kind === 'plan') {
      expect(createdWrite.body).toHaveProperty('profile_id', -1)
      expect(createdWrite.body?.rounds).toMatchObject([
        { backend_id: 701, translate: { prompt_template_id: -1 } },
      ])
    }
    await expect(drawer(page)).toHaveCount(0)
    await card(page, `Created ${spec.kind}`)
      .getByRole('button', { name: '编辑', exact: true })
      .click()
    await drawer(page).locator('input').first().fill(`Updated ${spec.kind}`)
    await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
    await expect.poll(() => state.writes.filter((write) => write.method === 'PUT').length).toBe(1)
    expectOwnership(state.writes.find((write) => write.method === 'PUT')!.body, false)
    await expect(drawer(page)).toHaveCount(0)
    const updated = state.data[spec.kind].find((item) => item.name === `Updated ${spec.kind}`)!
    expect(updated.owner_org_id).toBe(7)
    await card(page, updated.name).getByRole('button', { name: '删除', exact: true }).click()
    await page.getByRole('dialog').getByRole('button', { name: '删除', exact: true }).click()
    await expect
      .poll(() => state.writes.filter((write) => write.method === 'DELETE').length)
      .toBe(1)
    await expect(card(page, updated.name)).toHaveCount(0)
    expect(state.data[spec.kind].some((item) => item.id === updated.id)).toBe(false)
  })

  test(`${spec.kind}: copying to an organization prefills the editor and keeps the original`, async ({
    page,
  }) => {
    const state = await configApp(page),
      original = structuredClone(state.data[spec.kind].find((item) => item.scope === 'user')!)
    await page.goto(spec.path)
    await card(page, original.name).getByRole('button', { name: '复制到组织', exact: true }).click()
    const modal = page.locator('.n-modal:visible')
    await selectOption(page, modal.locator('.n-select'), 'Organization 7')
    await modal.getByRole('button', { name: '继续编辑', exact: true }).click()
    await expect(page).toHaveURL(/org_id=7/)
    await expect(drawer(page).locator('input').first()).toHaveValue(original.name)
    expect(state.writes).toHaveLength(0)
    if (spec.kind === 'plan') {
      await expect(page.getByText('已清除目标组织不可用的依赖，请重新选择后保存。')).toBeVisible()
      await drawer(page).getByRole('button', { name: spec.submit, exact: true }).click()
      await expect(page.getByText('请重新选择当前组织可用的依赖')).toBeVisible()
      expect(state.writes).toHaveLength(0)
      await planDependencies(page)
    } else if (spec.field !== 'config')
      await expect(drawer(page).locator('textarea').last()).toHaveValue(
        String(original[spec.field]),
      )
    await drawer(page).locator('input').first().fill(`Copied ${spec.kind}`)
    await drawer(page).getByRole('button', { name: spec.submit, exact: true }).click()
    await expect.poll(() => state.writes.length).toBe(1)
    expect(state.writes[0]!.method).toBe('POST')
    expectOwnership(state.writes[0]!.body, true)
    expect(state.data[spec.kind].find((item) => item.id === original.id)).toEqual(original)
    const copied = state.data[spec.kind].find((item) => item.name === `Copied ${spec.kind}`)!
    expect(copied.owner_org_id).toBe(7)
    expect(copied.id).not.toBe(original.id)
    if (spec.kind === 'plan') expect(copied.ruby_retry).toMatchObject({ backend_id: 0 })
    await page.goto(spec.path)
    await expect(card(page, original.name)).toBeVisible()
    await expect(card(page, copied.name)).toBeVisible()
  })

  test(`${spec.kind}: members can view organization configuration without write controls`, async ({
    page,
  }) => {
    const state = await configApp(page, 'member')
    await page.goto(`${spec.path}?org_id=7`)
    await expect(page.getByRole('button', { name: spec.create, exact: true })).toHaveCount(0)
    const row = card(page, display(spec.kind, 'org'))
    await expect(row.getByRole('button', { name: '编辑', exact: true })).toHaveCount(0)
    await expect(row.getByRole('button', { name: '删除', exact: true })).toHaveCount(0)
    await row.getByRole('button', { name: '查看', exact: true }).click()
    await expect(drawer(page).locator('input').first()).toBeDisabled()
    await expect(drawer(page).getByRole('button', { name: '保存', exact: true })).toHaveCount(0)
    expect(state.writes).toHaveLength(0)
  })
}

test('organization backend CRUD uses organization paths and keeps ownership out of request bodies', async ({
  page,
}) => {
  const state = await configApp(page)
  await page.goto('/backends?org_id=7')
  await page.getByRole('button', { name: '添加 AI 后端', exact: true }).click()
  await drawer(page).getByPlaceholder('例如：My OpenAI').fill('Created backend')
  await selectOption(page, drawer(page).locator('.n-select').first(), 'OpenAI')
  await drawer(page).getByPlaceholder('sk-…').fill('synthetic-test-key')
  const model = drawer(page).locator('.n-select').nth(1).locator('input')
  await model.fill('test-model')
  await model.press('Enter')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({ method: 'POST', path: '/orgs/7/backends' })
  expectOwnership(state.writes[0]!.body, false)
  await expect(drawer(page)).toHaveCount(0)
  await card(page, 'Created backend').getByRole('button', { name: '编辑', exact: true }).click()
  await drawer(page).getByPlaceholder('例如：My OpenAI').fill('Updated backend')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(2)
  expect(state.writes[1]).toMatchObject({ method: 'PUT', path: '/orgs/7/backends/1000' })
  expectOwnership(state.writes[1]!.body, false)
  await expect(drawer(page)).toHaveCount(0)
  const row = card(page, 'Updated backend')
  await row.getByRole('button', { name: '更多', exact: true }).click()
  await page.locator('.n-dropdown-menu:visible').getByText('删除', { exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '删除', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(3)
  expect(state.writes[2]).toMatchObject({ method: 'DELETE', path: '/orgs/7/backends/1000' })
  expect(state.backends.some((item) => item.id === 1000)).toBe(false)
})

test('organization backend is read-only for a member', async ({ page }) => {
  const state = await configApp(page, 'member')
  await page.goto('/backends?org_id=7')
  await expect(page.getByRole('button', { name: '添加 AI 后端', exact: true })).toHaveCount(0)
  const row = card(page, display('backend', 'org'))
  await row.getByRole('button', { name: '查看', exact: true }).click()
  await expect(drawer(page).getByPlaceholder('例如：My OpenAI')).toBeDisabled()
  await expect(drawer(page).getByRole('button', { name: '保存', exact: true })).toHaveCount(0)
  expect(state.writes).toHaveLength(0)
})
