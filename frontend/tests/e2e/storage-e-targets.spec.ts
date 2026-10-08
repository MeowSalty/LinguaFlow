import { expect, test, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client-core'
import { json, mockApp } from './fixtures'

type Options = ApiSchemas['StorageOptions']
type Reason = ApiSchemas['StorageOption']['reason_codes'][number]
const policies = [
  ['site_only', 'site', '当前政策仅允许站点存储。'],
  ['both', 'site', '当前政策允许站点存储和所属个人或组织的存储。'],
  ['both', 'user', '当前政策允许站点存储和所属个人或组织的存储。'],
  ['user_required', 'user', '当前政策要求使用所属个人或组织的存储。'],
] as const
const reasonText: Partial<Record<Reason, string>> = {
  storage_maintenance: '存储服务正在维护',
  storage_deployment_disabled: '站点尚未启用此操作所需的存储能力，请联系管理员',
  policy_disallowed: '当前政策不接受此空间',
  selection_required: '请明确选择存储空间，系统不会自动选择第一项。',
}

/** These are metadata-only responses: drivers/connection locations are never sent to the UI. */
function discovery(
  mode: Options['policy']['mode'],
  defaultChoice: Options['policy']['default_choice'],
  enabled: boolean,
  maintenance: boolean,
  scope: Options['scope'] = 'user',
): Options {
  const candidate = (
    space_id: number,
    name: string,
    owner: ApiSchemas['StorageOption']['scope'],
    local: boolean,
  ): ApiSchemas['StorageOption'] => {
    const reason_codes: Reason[] = []
    if (maintenance) reason_codes.push('storage_maintenance')
    if (!enabled && !local) reason_codes.push('storage_deployment_disabled')
    if (owner === 'site' ? mode === 'user_required' : mode === 'site_only')
      reason_codes.push('policy_disallowed')
    return { space_id, name, scope: owner, selectable: reason_codes.length === 0, reason_codes }
  }
  const items = [
    candidate(11, 'Local 站点空间', 'site', true),
    candidate(12, 'S3 站点空间', 'site', false),
    candidate(13, scope === 'org' ? '组织自有空间' : '个人自有空间', scope, false),
  ]
  const defaultLocal = defaultChoice === 'site' && items[0]!.selectable
  return {
    runtime: { deployment_enabled: enabled, maintenance },
    scope,
    owner_id: scope === 'org' ? 7 : 1,
    policy: { mode, default_choice: defaultChoice, generation: 6 },
    default_space_id: defaultLocal ? 11 : null,
    default_unavailable_reason:
      defaultChoice === 'user' ? 'selection_required' : (items[0]!.reason_codes[0] ?? null),
    items,
  }
}

async function setup(page: Page, options: Options) {
  await mockApp(page, { role: 'user' })
  const reads: string[] = []
  const writes: { path: string; body: Record<string, unknown> }[] = []
  const organization = {
    id: 7,
    name: 'Target Team',
    slug: 'targets',
    current_user_role: 'owner',
    created_at: '2026-10-05T00:00:00Z',
  }
  await page.route('**/api/v1/**', (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname.replace('/api/v1', '')
    if (request.method() === 'GET') reads.push(path + url.search)
    else writes.push({ path, body: request.postDataJSON() })
    if (path === '/orgs')
      return json(route, { items: options.scope === 'org' ? [organization] : [] })
    if (path === '/orgs/7') return json(route, organization)
    if (path === '/storage/options') return json(route, options)
    if (path === '/projects' || path === '/orgs/7/projects') {
      if (request.method() === 'GET') return json(route, { items: [] })
      return json(
        route,
        {
          id: 7,
          ...request.postDataJSON(),
          owner_user_id: options.scope === 'user' ? 1 : null,
          owner_org_id: options.scope === 'org' ? 7 : null,
          storage_generation: 0,
          output_generation: 0,
          storage_state: 'active',
          created_at: '2026-10-05T00:00:00Z',
          updated_at: '2026-10-05T00:00:00Z',
        },
        201,
      )
    }
    return route.fallback()
  })
  await page.goto(options.scope === 'org' ? '/projects?org_id=7' : '/projects')
  await page.getByRole('button', { name: '新建项目', exact: true }).click()
  await page.getByPlaceholder('例如：LinguaFlow 本地化').fill('E target selection')
  await expect(page.getByLabel('选择存储空间', { exact: true })).toBeVisible()
  return { reads, writes }
}

const target = (page: Page) => page.getByLabel('选择存储空间', { exact: true })
const create = (page: Page) => page.getByRole('button', { name: '创建项目', exact: true })
const option = (page: Page, name: string) =>
  page.locator('.n-base-select-option').filter({ hasText: name })

for (const [mode, defaultChoice, policyText] of policies) {
  for (const enabled of [true, false]) {
    for (const maintenance of [false, true]) {
      test(`E-T02/E-T05 targets ${mode}/${defaultChoice} enabled=${enabled} maintenance=${maintenance}`, async ({
        page,
      }) => {
        const options = discovery(mode, defaultChoice, enabled, maintenance)
        const { reads, writes } = await setup(page, options)
        await expect(page.getByText(policyText, { exact: true })).toBeVisible()
        const hasDefault = defaultChoice === 'site' && !maintenance
        if (hasDefault) {
          await expect(target(page)).toContainText('Local 站点空间 · 站点')
          await expect(create(page)).toBeEnabled()
        } else {
          await expect(target(page)).toContainText('选择存储空间')
          await expect(create(page)).toBeDisabled()
        }
        if (defaultChoice === 'user')
          await expect(
            page.getByText(reasonText.selection_required!, { exact: true }),
          ).toBeVisible()
        await target(page).click()
        for (const item of options.items) {
          const row = option(page, item.name)
          await expect(row).toBeVisible()
          for (const reason of item.reason_codes)
            await expect(row).toContainText(reasonText[reason]!)
          if (item.selectable) await expect(row).not.toHaveClass(/--disabled/)
          else {
            await expect(row).toHaveClass(/--disabled/)
            // A visible refused option must not replace the default or manufacture a choice.
            await row.click()
            await expect(target(page)).toContainText(hasDefault ? 'Local 站点空间' : '选择存储空间')
          }
        }
        expect(writes).toHaveLength(0)
        const chosen = options.items.find((item) => item.selectable)
        if (chosen) {
          await option(page, chosen.name).click()
          await expect(create(page)).toBeEnabled()
          expect(writes).toHaveLength(0)
          await create(page).click()
          await expect.poll(() => writes.length).toBe(1)
          expect(writes[0]).toMatchObject({
            path: '/projects',
            body: { storage_space_id: chosen.space_id },
          })
        } else {
          await page.keyboard.press('Escape')
          await expect(create(page)).toBeDisabled()
          expect(writes).toHaveLength(0)
        }
        expect(reads.filter((path) => path.startsWith('/storage/options'))).toEqual([
          '/storage/options?scope=user',
        ])
        expect(
          reads.some((path) => path.includes('/connections') || path.startsWith('/admin')),
        ).toBe(false)
      })
    }
  }
}

for (const scenario of ['unregistered', 'unregistered-disabled', 'registered-disabled'] as const) {
  test(`E-T05 ${scenario} default S3 never falls back to a selectable Local target`, async ({
    page,
  }) => {
    const enabled = scenario === 'unregistered'
    const options = discovery('both', 'site', enabled, false)
    options.default_space_id = null
    options.default_unavailable_reason = enabled
      ? 'selection_required'
      : 'storage_deployment_disabled'
    if (scenario !== 'registered-disabled')
      options.items = options.items.filter((item) => item.space_id !== 12)
    const { writes } = await setup(page, options)
    await expect(target(page)).toContainText('选择存储空间')
    await expect(create(page)).toBeDisabled()
    await expect(
      page.getByText(reasonText[options.default_unavailable_reason]!, { exact: true }),
    ).toBeVisible()
    await page.getByRole('button', { name: '刷新可用空间', exact: true }).click()
    await expect(create(page)).toBeDisabled()
    await expect(target(page)).toContainText('选择存储空间')
    expect(writes).toHaveLength(0)
    await target(page).click()
    await option(page, 'Local 站点空间').click()
    await expect(create(page)).toBeEnabled()
    await create(page).click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]!.body.storage_space_id).toBe(11)
  })
}

for (const enabled of [true, false]) {
  test(`E-T05 organization BYOS enabled=${enabled} requires an explicit scoped choice`, async ({
    page,
  }) => {
    const options = discovery('both', 'user', enabled, false, 'org')
    const { reads, writes } = await setup(page, options)
    await expect(create(page)).toBeDisabled()
    await expect(page.getByText(reasonText.selection_required!, { exact: true })).toBeVisible()
    await target(page).click()
    const byos = option(page, '组织自有空间')
    await expect(byos).toContainText('组织自有空间 · 组织')
    await expect(option(page, '个人自有空间')).toHaveCount(0)
    if (enabled) await byos.click()
    else {
      await expect(byos).toHaveClass(/--disabled/)
      await expect(byos).toContainText(reasonText.storage_deployment_disabled!)
      await byos.click()
      await expect(create(page)).toBeDisabled()
      await option(page, 'Local 站点空间').click()
    }
    await expect(create(page)).toBeEnabled()
    expect(writes).toHaveLength(0)
    await create(page).click()
    await expect.poll(() => writes.length).toBe(1)
    expect(writes[0]).toMatchObject({
      path: '/orgs/7/projects',
      body: { storage_space_id: enabled ? 13 : 11 },
    })
    expect(reads.filter((path) => path.startsWith('/storage/options'))).toEqual([
      '/storage/options?scope=org&organization_id=7',
    ])
    expect(reads.some((path) => path.includes('/connections') || path.startsWith('/admin'))).toBe(
      false,
    )
  })
}
