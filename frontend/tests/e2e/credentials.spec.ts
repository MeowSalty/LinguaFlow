import { expect, test, type Page, type Locator } from '@playwright/test'
import { json, mockApp } from './fixtures'

type Write = { path: string; method: string; body?: Record<string, unknown> }
const drawer = (page: Page) => page.locator('.n-drawer:visible')
const field = (page: Page, label: string) =>
  drawer(page)
    .locator('.n-form-item')
    .filter({ has: page.getByText(label, { exact: true }) })
async function choose(page: Page, locator: Locator, label: string) {
  await locator.locator('.n-base-selection').click()
  await page
    .locator('.n-base-select-menu:visible .n-base-select-option')
    .filter({ hasText: label })
    .first()
    .click()
}
async function setup(page: Page, role: 'owner' | 'admin' | 'member' = 'owner') {
  await mockApp(page, { role: 'user' })
  const state = {
    writes: [] as Write[],
    reads: [] as string[],
    failCredentials: false,
    failVersions: false,
    abortCreate: false,
    credentialVersion: 1,
    revoked: false,
  }
  const metadata = (org = false) => ({
    id: org ? 70 : 10,
    scope: org ? 'org' : 'user',
    owner_id: org ? 7 : 1,
    provider: 'openai',
    endpoint: 'https://api.openai.com/v1',
    current_version: state.credentialVersion,
  })
  const backends = [false, true].map((org) => ({
    id: org ? 7 : 1,
    name: org ? '组织 Backend' : '个人 Backend',
    scope: org ? 'org' : 'user',
    ...(org ? { owner_org_id: 7 } : { owner_user_id: 1 }),
    type: 'openai',
    options: { type: 'openai', model: 'fixture-model' },
    has_secret: true,
    credential: { id: org ? 70 : 10, version: 1 },
  }))
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname.replace('/api/v1', ''),
      method = request.method()
    if (path === '/orgs')
      return json(route, {
        items: [
          {
            id: 7,
            name: 'Organization 7',
            slug: 'org-7',
            display_name: 'Organization 7',
            current_user_role: role,
          },
        ],
      })
    if (path.includes('/credentials')) {
      if (method === 'GET') state.reads.push(path)
      else state.writes.push({ path, method, body: request.postDataJSON() ?? undefined })
      if (/\/versions\/\d+\/revoke$/.test(path)) {
        state.revoked = true
        return route.fulfill({ status: 204 })
      }
      if (path.endsWith('/collect')) return json(route, { deleted_versions: 0 })
      if (path.endsWith('/versions')) {
        if (method === 'POST') {
          ++state.credentialVersion
          return json(
            route,
            { id: path.includes('/70/') ? 70 : 10, version: state.credentialVersion },
            201,
          )
        }
        if (state.failVersions) return json(route, { title: 'unavailable' }, 503)
        return json(route, {
          items: [
            { version: 1, revoked: state.revoked, created_at: '2026-10-01T00:00:00Z' },
            ...(state.credentialVersion > 1
              ? [
                  {
                    version: state.credentialVersion,
                    revoked: false,
                    created_at: '2026-10-01T00:01:00Z',
                  },
                ]
              : []),
          ],
        })
      }
      if (method === 'POST') {
        if (state.abortCreate) return route.abort('failed')
        return json(route, metadata(path.startsWith('/orgs/')), 201)
      }
      if (state.failCredentials) return json(route, { title: 'unavailable' }, 503)
      return json(route, { items: [metadata(path.startsWith('/orgs/'))] })
    }
    if (path.includes('/backends')) {
      if (method === 'GET') {
        state.reads.push(path)
        return json(route, { items: path.startsWith('/orgs/') ? [backends[1]] : backends })
      }
      const body = request.postDataJSON() ?? {}
      state.writes.push({ path, method, body })
      if (path.endsWith('/models'))
        return json(route, { items: [{ id: 'probed-model', name: 'Probed model' }] })
      if (method === 'DELETE') return route.fulfill({ status: 204 })
      const target = path.startsWith('/orgs/') ? backends[1]! : backends[0]!
      const result = {
        ...target,
        name: body.name,
        type: body.type,
        options: body.options,
        credential: {
          id: body.credential_id ?? target.credential.id,
          version: state.credentialVersion,
        },
      }
      if (method === 'POST') {
        result.id = 100
        backends.push(result)
      } else Object.assign(target, result)
      return json(route, result, method === 'POST' ? 201 : 200)
    }
    return route.fallback()
  })
  return state
}
async function edit(page: Page, name = '个人 Backend') {
  await page
    .locator('.lf-interactive-card')
    .filter({ hasText: name })
    .getByRole('button', { name: '编辑', exact: true })
    .click()
}
async function manager(page: Page) {
  await page.getByRole('button', { name: '管理凭据', exact: true }).click()
  await expect(page.getByTestId('credential-manager')).toBeVisible()
}
async function confirm(page: Page) {
  await page.getByRole('dialog').getByRole('button', { name: '确定', exact: true }).click()
}

async function settlePage(page: Page) {
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  )
}

test('write-only Backend binding keeps existing credentials and temporary probes never enter saving', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await edit(page)
  await expect(drawer(page).getByText('保留当前凭据', { exact: true })).toBeVisible()
  await drawer(page).getByPlaceholder('临时探测密钥').fill('temporary-probe-key')
  await drawer(page).getByRole('button', { name: '探测模型', exact: true }).click()
  await expect(drawer(page).getByPlaceholder('临时探测密钥')).toHaveValue('')
  expect(state.writes[0]?.body).toEqual({ type: 'openai', secret: 'temporary-probe-key' })
  await drawer(page).getByPlaceholder('例如：My OpenAI').fill('Updated backend')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect(drawer(page)).toHaveCount(0)
  const write = state.writes.find((value) => value.method === 'PUT')!
  expect(write.body).not.toHaveProperty('secret')
  expect(write.body).not.toHaveProperty('credential_id')
  expect(write.body?.options).not.toHaveProperty('api_key')
})

test('new and rebound Backends send only the chosen credential mode and clear keys on close', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await page.getByRole('button', { name: '添加 AI 后端', exact: true }).click()
  await drawer(page).getByPlaceholder('例如：My OpenAI').fill('New backend')
  await choose(page, field(page, '后端类型').locator('.n-select'), 'OpenAI')
  await drawer(page).getByPlaceholder('sk-…').fill('new-backend-key')
  const model = field(page, '模型').locator('input')
  await model.fill('model')
  await model.press('Enter')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect(drawer(page)).toHaveCount(0)
  expect(state.writes[0]?.body).toHaveProperty('secret', 'new-backend-key')
  expect(state.writes[0]?.body?.options).not.toHaveProperty('api_key')
  await edit(page)
  await choose(page, field(page, '凭据').locator('.n-select').first(), '为此 Backend 设置新密钥')
  await drawer(page).getByPlaceholder('sk-…').fill('discarded-secret')
  await drawer(page).getByRole('button', { name: '取消', exact: true }).click()
  await edit(page)
  await choose(page, field(page, '凭据').locator('.n-select').first(), '为此 Backend 设置新密钥')
  await expect(drawer(page).getByPlaceholder('sk-…')).toHaveValue('')
  await drawer(page).getByPlaceholder('sk-…').fill('rebound-key')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.writes.filter((value) => value.method === 'PUT').length).toBe(1)
  expect(state.writes.at(-1)?.body).toHaveProperty('secret', 'rebound-key')
})

test('aggregate view binds organization Backends using organization credential lists', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await edit(page, '组织 Backend')
  await choose(page, field(page, '凭据').locator('.n-select').first(), '选择已有凭据')
  await expect.poll(() => state.reads.includes('/orgs/7/credentials')).toBe(true)
  expect(state.reads).not.toContain('/credentials')
  await choose(page, field(page, '凭据').locator('.n-select').nth(1), '#70')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
  expect(state.writes[0]).toMatchObject({
    path: '/orgs/7/backends/7',
    method: 'PUT',
    body: { credential_id: 70 },
  })
  expect(state.writes[0]?.body).not.toHaveProperty('secret')
})

test('changed endpoint blocks keep and clears secret inputs', async ({ page }) => {
  const state = await setup(page)
  await page.goto('/backends')
  await edit(page)
  await drawer(page).getByPlaceholder('临时探测密钥').fill('discard-probe')
  await drawer(page).getByPlaceholder('留空使用官方地址').fill('https://new-endpoint.test/v1')
  await expect(drawer(page).getByPlaceholder('临时探测密钥')).toHaveValue('')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect(
    drawer(page)
      .getByText(/服务商或端点已变更/)
      .first(),
  ).toBeVisible()
  expect(state.writes).toHaveLength(0)
  await drawer(page).getByPlaceholder('留空使用官方地址').fill('')
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.writes.length).toBe(1)
})

test('rotation refresh failure stays committed, clears secret and only retries reads', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await manager(page)
  await drawer(page).getByRole('button', { name: '查看版本', exact: true }).click()
  await drawer(page).getByPlaceholder('输入密钥').fill('rotating-key')
  state.failCredentials = true
  await drawer(page).getByRole('button', { name: '轮换共享凭据', exact: true }).click()
  await expect(page.getByRole('dialog').getByText(/已接受任务继续使用原版本/)).toBeVisible()
  await confirm(page)
  await expect(drawer(page).getByText(/操作成功，刷新失败/)).toBeVisible()
  await expect(drawer(page).getByPlaceholder('输入密钥')).toHaveValue('')
  expect(state.writes.filter((write) => write.path.endsWith('/versions'))).toHaveLength(1)
  expect(state.reads.filter((path) => path === '/backends').length).toBeGreaterThan(1)
  state.failCredentials = false
  await drawer(page).getByRole('button', { name: '重试读取', exact: true }).first().click()
  await expect(drawer(page).getByText('当前版本 2', { exact: true })).toBeVisible()
  expect(state.writes.filter((write) => write.path.endsWith('/versions'))).toHaveLength(1)
})

test('revoking the current version consumes 204 and collection zero is a valid result', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await manager(page)
  await drawer(page).getByRole('button', { name: '查看版本', exact: true }).click()
  await drawer(page).getByRole('button', { name: '撤销版本', exact: true }).click()
  await expect(page.getByRole('dialog').getByText(/^撤销凭据 #10 的版本 1：/)).toBeVisible()
  await confirm(page)
  await expect(drawer(page).getByText('已撤销', { exact: true })).toBeVisible()
  await drawer(page).getByRole('button', { name: '回收旧版本', exact: true }).click()
  await confirm(page)
  await expect(drawer(page).getByText('已回收 0 个旧版本', { exact: true })).toBeVisible()
  expect(state.writes.map((write) => write.path)).toEqual([
    '/credentials/10/versions/1/revoke',
    '/credentials/10/collect',
  ])
})

test('interrupted credential creation remains unknown after refresh and needs explicit new submission', async ({
  page,
}) => {
  const state = await setup(page)
  await page.goto('/backends')
  await manager(page)
  await drawer(page).getByRole('button', { name: '创建凭据', exact: true }).click()
  await drawer(page).getByPlaceholder('输入密钥').fill('unknown-key')
  state.abortCreate = true
  await drawer(page).getByRole('button', { name: '创建凭据', exact: true }).last().click()
  await expect(drawer(page).getByText(/结果未知：/)).toBeVisible()
  await drawer(page).getByRole('button', { name: '刷新', exact: true }).click()
  await expect(drawer(page).getByText(/结果未知：/)).toBeVisible()
  expect(state.writes).toHaveLength(1)
  state.abortCreate = false
  await drawer(page).getByRole('button', { name: '作为新操作重新提交', exact: true }).click()
  await expect(page.getByRole('dialog').getByText(/可能产生额外凭据或版本/)).toBeVisible()
  await confirm(page)
  await expect.poll(() => state.writes.length).toBe(2)
  await expect(drawer(page).getByPlaceholder('输入密钥')).toHaveCount(0)
})

for (const role of ['owner', 'admin'] as const)
  test(`organization ${role} can manage credentials with scoped paths`, async ({ page }) => {
    const state = await setup(page, role)
    await page.goto('/backends?org_id=7')
    await manager(page)
    await expect.poll(() => state.reads.includes('/orgs/7/credentials')).toBe(true)
    await drawer(page).getByRole('button', { name: '查看版本', exact: true }).click()
    await expect.poll(() => state.reads.includes('/credentials/70/versions')).toBe(true)
  })

test('organization members never request credential metadata', async ({ page }) => {
  const state = await setup(page, 'member')
  await page.goto('/backends?org_id=7')
  await expect(page.getByText('组织 Backend', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '管理凭据', exact: true })).toHaveCount(0)
  await page
    .locator('.lf-interactive-card')
    .getByRole('button', { name: '查看', exact: true })
    .click()
  await expect(drawer(page).getByRole('button', { name: '保存', exact: true })).toHaveCount(0)
  expect(state.reads.filter((path) => path.includes('credentials'))).toEqual([])
})

test('a delayed rotation cannot clear a newly selected credential draft', async ({ page }) => {
  await setup(page)
  let releaseRotation!: () => void
  const rotation = new Promise<void>((resolve) => {
    releaseRotation = resolve
  })
  let started = false
  await page.route('**/api/v1/credentials', (route) =>
    json(route, {
      items: [10, 11].map((id) => ({
        id,
        scope: 'user',
        owner_id: 1,
        provider: 'openai',
        endpoint: 'https://api.openai.com/v1',
        current_version: 1,
      })),
    }),
  )
  await page.route('**/api/v1/credentials/11/versions', (route) =>
    json(route, { items: [{ version: 1, revoked: false, created_at: '2026-10-01T00:00:00Z' }] }),
  )
  await page.route('**/api/v1/credentials/10/versions', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback()
    started = true
    await rotation
    await json(route, { id: 10, version: 2 }, 201)
  })
  await page.goto('/backends')
  await manager(page)
  await drawer(page).getByRole('button', { name: '查看版本', exact: true }).first().click()
  await drawer(page).getByPlaceholder('输入密钥').fill('rotate-first')
  await drawer(page).getByRole('button', { name: '轮换共享凭据', exact: true }).click()
  await confirm(page)
  await expect.poll(() => started).toBe(true)
  await page.keyboard.press('Escape')
  await drawer(page).getByRole('button', { name: '查看版本', exact: true }).nth(1).click()
  await drawer(page).getByPlaceholder('输入密钥').fill('second-draft-must-stay')
  releaseRotation()
  await expect(drawer(page).getByText('操作成功', { exact: true })).toBeVisible()
  await expect(drawer(page).getByPlaceholder('输入密钥')).toHaveValue('second-draft-must-stay')
})

for (const outcome of ['success', 'failure'] as const) {
  test(`late model probe ${outcome} cannot replace results or finish a newer request`, async ({
    page,
  }) => {
    await setup(page)
    // Exercise the generation guard even when the transport ignores cancellation.
    await page.addInitScript(() => {
      const originalFetch = window.fetch.bind(window)
      window.fetch = (input, init) => {
        const request = new Request(input, init)
        return request.url.endsWith('/backends/models')
          ? originalFetch(new Request(request, { signal: new AbortController().signal }))
          : originalFetch(input, init)
      }
    })
    const releases: (() => void)[] = []
    await page.route('**/api/v1/backends/models', async (route) => {
      const index = releases.length
      await new Promise<void>((resolve) => releases.push(resolve))
      if (index === 0 && outcome === 'failure')
        return json(route, { title: 'old-private-key', detail: 'old-private-key' }, 401)
      return json(route, {
        items: index === 0 ? [{ id: 'old-model' }] : [{ id: 'new-model' }, { id: 'new-model-2' }],
      })
    })
    await page.goto('/backends')
    await edit(page)
    const secret = drawer(page).getByPlaceholder('临时探测密钥')
    const probe = drawer(page).getByRole('button', { name: /探测模型$/ })
    await secret.fill('old-private-key')
    await probe.click()
    await expect.poll(() => releases.length).toBe(1)
    await drawer(page).getByPlaceholder('留空使用官方地址').fill('https://other.test/v1')
    await expect(secret).toHaveValue('')
    await secret.fill('new-private-key')
    await probe.click()
    await expect.poll(() => releases.length).toBe(2)
    const oldResponse = page.waitForResponse((response) =>
      response.url().endsWith('/backends/models'),
    )
    releases[0]!()
    await (await oldResponse).finished()
    await settlePage(page)
    await expect(probe).toBeDisabled()
    await expect(secret).toHaveValue('new-private-key')
    await expect(page.locator('.n-message')).toHaveCount(0)
    releases[1]!()
    await expect(page.getByText('已加载 2 个可用模型', { exact: true })).toBeVisible()
    await expect(secret).toHaveValue('')
    await field(page, '模型').locator('.n-base-selection').click()
    await expect(
      page.locator('.n-base-select-menu:visible').getByText('new-model', { exact: true }),
    ).toBeVisible()
    await expect(
      page.locator('.n-base-select-menu:visible').getByText('old-model', { exact: true }),
    ).toHaveCount(0)
    expect(
      await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage })),
    ).not.toMatch(/old-private-key|new-private-key/)
  })
}

test('late Backend write failure cannot affect a replacement form', async ({ page }) => {
  await setup(page)
  let release!: () => void
  let pending = false
  const waiting = new Promise<void>((resolve) => {
    release = resolve
  })
  await page.route('**/api/v1/backends/1', async (route) => {
    if (route.request().method() !== 'PUT') return route.fallback()
    pending = true
    await waiting
    return json(route, { title: 'old-private-key', detail: 'old-private-key' }, 500)
  })
  await page.goto('/backends')
  await edit(page)
  await drawer(page).getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => pending).toBe(true)
  await drawer(page).getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '添加 AI 后端', exact: true }).click()
  await expect(drawer(page).getByPlaceholder('例如：My OpenAI')).toHaveValue('')
  const response = page.waitForResponse((value) => value.url().endsWith('/backends/1'))
  release()
  await (await response).finished()
  await settlePage(page)
  await expect(drawer(page).getByPlaceholder('例如：My OpenAI')).toHaveValue('')
  await expect(drawer(page).getByPlaceholder('例如：My OpenAI')).toBeEnabled()
  await expect(drawer(page).getByRole('button', { name: '保存', exact: true })).toBeEnabled()
  await expect(page.locator('.n-message')).toHaveCount(0)
  await expect(drawer(page).locator('.n-alert--error')).toHaveCount(0)
})
