import { expect, test, type Page, type Route } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json } from './fixtures'
import { mockB4Connection, oidcConnection } from './auth-admin-b4-fixtures'

type Purpose = ApiSchemas['AuthAttributeCatalog']['purpose']
type RuleSet = ApiSchemas['AuthRuleSet']
type CatalogCall = {
  method: 'GET' | 'POST'
  purpose: Purpose
  body?: Record<string, unknown>
}

const purposes: readonly Purpose[] = ['login', 'signup', 'link']

const domainField = (
  fieldId: 'registration.email_domain' | 'external.email_domain',
): ApiSchemas['AuthAttributeField'] => ({
  field_id: fieldId,
  label: fieldId === 'registration.email_domain' ? '注册邮箱域名' : '外部邮箱域名',
  type: 'string',
  source: fieldId === 'registration.email_domain' ? 'derived' : 'oidc_userinfo',
  pointer: fieldId === 'registration.email_domain' ? '/email_domain' : '/email_domain',
  user_editable: false,
  allowed_operators: ['eq', 'ne', 'in', 'not_in'],
  semantic_format: 'email_domain',
  normalization_version: 'idna-v1',
  ...(fieldId === 'registration.email_domain' ? { derived_from: ['registration.email'] } : {}),
})

const emailField: ApiSchemas['AuthAttributeField'] = {
  field_id: 'external.email',
  label: '外部邮箱',
  type: 'string',
  source: 'oidc_userinfo',
  pointer: '/email',
  user_editable: false,
  allowed_operators: ['eq', 'ne'],
}

const ruleSet = (match: 'all' | 'any', conditions: ApiSchemas['AuthRuleCondition'][]): RuleSet => ({
  mode: 'conditions',
  match,
  conditions,
})

function externalRecord(
  policy: Partial<ApiSchemas['AuthConnectionPolicy']> = {},
): ApiSchemas['AuthConnectionRecord'] {
  const base = oidcConnection()
  return oidcConnection({
    capabilities: { can_login: true, can_signup: true, can_link: true, can_test: true },
    configuration: {
      ...base.configuration,
      policy: {
        ...base.configuration.policy,
        signup_enabled: true,
        link_enabled: true,
        ...policy,
      },
    },
  })
}

async function routeExternalRules(
  page: Page,
  input: ApiSchemas['AuthConnectionRecord'],
  fields: Partial<Record<Purpose, ApiSchemas['AuthAttributeField'][]>> = {},
  handleCandidate?: (route: Route, call: CatalogCall) => Promise<boolean>,
) {
  let current = input
  const catalogs: CatalogCall[] = []
  const patches: Record<string, unknown>[] = []

  await mockB4Connection(page, () => current)
  await page.route(
    '**/api/v1/admin/auth-connections/company/attributes**',
    async (route: Route) => {
      const request = route.request()
      const url = new URL(request.url())
      const body =
        request.method() === 'POST'
          ? (request.postDataJSON() as Record<string, unknown>)
          : undefined
      const purpose = (body?.purpose ?? url.searchParams.get('purpose')) as Purpose
      expect(purposes).toContain(purpose)
      catalogs.push({ method: request.method() as 'GET' | 'POST', purpose, body })
      if (body) {
        const changes = body.changes as { policy?: Partial<ApiSchemas['AuthConnectionPolicy']> }
        const policy = { ...current.configuration.policy, ...changes?.policy }
        if (policy.signup_enabled && !policy.login_enabled)
          return json(route, { detail: 'Signup requires login' }, 400)
        if (await handleCandidate?.(route, { method: 'POST', purpose, body })) return
      }
      return json(route, {
        revision: body?.expected_revision ?? Number(url.searchParams.get('expected_revision')),
        purpose,
        fields: fields[purpose] ?? [],
        operators: {},
      })
    },
  )
  await page.route('**/api/v1/admin/auth-connections/company', (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback()
    const body = route.request().postDataJSON() as Record<string, unknown>
    patches.push(body)
    const policy = body.policy as ApiSchemas['AuthConnectionPolicy'] | undefined
    current = {
      ...current,
      aggregate_revision: current.aggregate_revision + 1,
      configuration: { ...current.configuration, ...(policy ? { policy } : {}) },
    }
    return json(route, current)
  })

  return { catalogs, patches }
}

async function waitForCatalogs(page: Page, catalogs: CatalogCall[], count = 3) {
  await expect.poll(() => catalogs.length).toBeGreaterThanOrEqual(count)
  await expect(page.getByTestId('auth-connection-catalog-purpose')).toBeVisible()
}

async function openExternalEditor(page: Page) {
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  const drawer = page.locator('.n-drawer-content')
  await expect(drawer).toBeVisible()
  await drawer.locator('[data-anchor="mappings"] .n-collapse-item__header').click()
  await expect(drawer.getByTestId('auth-connection-catalog-purpose')).toBeVisible()
  return drawer
}

async function chooseSelect(page: Page, select: ReturnType<Page['locator']>, label: string) {
  await expect(page.locator('.n-base-select-menu:visible')).toHaveCount(0)
  await select.locator('.n-base-selection').click()
  const option = page
    .locator('.n-base-select-menu:visible .n-base-select-option:visible')
    .filter({ hasText: label })
    .first()
  await expect(option).toBeVisible()
  await option.click()
  await expect(page.locator('.n-base-select-menu:visible')).toHaveCount(0)
}

test('blocks signup while login is off without requesting candidate catalogs or saving', async ({
  page,
}) => {
  const current = externalRecord()
  const calls = await routeExternalRules(page, current, {
    login: [emailField],
    signup: [domainField('registration.email_domain')],
    link: [domainField('external.email_domain')],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  const initialCatalogCount = calls.catalogs.length

  await drawer.getByTestId('auth-connection-purpose-login').click()

  const warning = drawer.getByTestId('auth-connection-signup-login-warning')
  await expect(warning).toHaveText(
    '允许注册需要同时开启“允许登录”。请开启“允许登录”，或关闭“允许注册”。当前修改尚未保存。',
  )
  await expect(drawer.getByTestId('auth-connection-purpose-login')).toHaveAttribute(
    'aria-checked',
    'false',
  )
  await expect(drawer.getByTestId('auth-connection-purpose-signup')).toHaveAttribute(
    'aria-checked',
    'true',
  )
  await expect(drawer.getByTestId('auth-connection-policy-details-signup')).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-policy-details-login')).toBeHidden()
  expect(calls.catalogs).toHaveLength(initialCatalogCount)
  expect(calls.patches).toHaveLength(0)
  for (const action of ['save-configuration', 'check', 'impact', 'test'] as const)
    await expect(drawer.getByTestId(`auth-connection-${action}`)).toBeDisabled()

  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(warning).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()
  for (const action of ['check', 'test'] as const)
    await expect(drawer.getByTestId(`auth-connection-${action}`)).toBeEnabled()
})

test('restores a separately changed draft by closing signup after login', async ({ page }) => {
  const current = externalRecord()
  const calls = await routeExternalRules(page, current, {
    login: [emailField],
    signup: [domainField('registration.email_domain')],
    link: [domainField('external.email_domain')],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect.poll(() => calls.catalogs.filter((call) => call.method === 'POST').length).toBe(3)
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()

  const candidateCount = calls.catalogs.filter((call) => call.method === 'POST').length
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(drawer.getByTestId('auth-connection-signup-login-warning')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()
  expect(calls.catalogs.filter((call) => call.method === 'POST')).toHaveLength(candidateCount)

  await drawer.getByTestId('auth-connection-purpose-signup').click()
  await expect(drawer.getByTestId('auth-connection-signup-login-warning')).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  expect(calls.patches).toHaveLength(0)
  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toEqual({
    expected_revision: current.aggregate_revision,
    policy: {
      ...current.configuration.policy,
      login_enabled: false,
      signup_enabled: false,
      signup: { ...current.configuration.policy.signup, email_requirement: 'required' },
    },
  })
})

test('reveals dependent policies progressively and saves their preserved values', async ({
  page,
}) => {
  const current = externalRecord({
    login_enabled: false,
    signup_enabled: false,
    link_enabled: false,
    signup: { email_requirement: 'required', password_requirement: 'optional' },
    login_rules: ruleSet('all', [
      { field_id: 'external.email', operator: 'eq', value: 'member@example.com' },
    ]),
    signup_rules: ruleSet('all', [
      { field_id: 'registration.email_domain', operator: 'in', value: ['example.com'] },
    ]),
    link_rules: ruleSet('all', [
      { field_id: 'external.email_domain', operator: 'not_in', value: ['blocked.example'] },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [emailField],
    signup: [domainField('registration.email_domain')],
    link: [domainField('external.email_domain')],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  const details = (purpose: Purpose) =>
    drawer.getByTestId(`auth-connection-policy-details-${purpose}`)
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  for (const purpose of purposes) await expect(details(purpose)).toBeHidden()
  await expect(drawer.locator('.n-spin-body')).toHaveCount(0)
  await drawer.locator('[data-anchor="policy"]').screenshot({
    path: test.info().outputPath('policy-disabled.png'),
    animations: 'disabled',
  })

  await drawer.getByTestId('auth-connection-purpose-link').click()
  await expect(details('link')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  await expect(details('login')).toBeHidden()
  await drawer.getByTestId('auth-connection-purpose-link').click()
  await expect(details('link')).toBeHidden()

  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(details('login')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-purpose-signup')).toHaveAttribute(
    'aria-checked',
    'false',
  )
  await expect(details('signup')).toBeHidden()
  await drawer.getByTestId('auth-connection-purpose-signup').click()
  await expect(details('signup')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-signup-email-requirement')).toContainText('必填')
  await expect(drawer.getByTestId('auth-connection-signup-password-requirement')).toContainText(
    '可选',
  )

  await drawer.getByTestId('auth-connection-purpose-signup').click()
  await expect(details('signup')).toBeHidden()
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await drawer.getByTestId('auth-connection-purpose-signup').click()
  await expect(details('signup')).toBeVisible()
  await expect(details('signup')).toContainText('example.com')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await expect(drawer.getByTestId('auth-connection-purpose-signup')).toHaveAttribute(
    'aria-checked',
    'true',
  )
  await expect(drawer.locator('.n-spin-body')).toHaveCount(0)
  await drawer
    .getByTestId('auth-connection-policy-signup')
    .evaluate((element) => element.scrollIntoView({ block: 'start' }))
  await page.screenshot({
    path: test.info().outputPath('policy-enabled.png'),
    animations: 'disabled',
  })
  expect(calls.patches).toHaveLength(0)

  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toEqual({
    expected_revision: current.aggregate_revision,
    policy: { ...current.configuration.policy, login_enabled: true, signup_enabled: true },
  })
})

test('preserves invalid numeric drafts while hidden and repairs them without enabling the purpose', async ({
  page,
}) => {
  const numberField: ApiSchemas['AuthAttributeField'] = {
    field_id: 'external.score',
    label: '分数',
    type: 'number',
    source: 'oidc_userinfo',
    pointer: '/score',
    user_editable: false,
    allowed_operators: ['eq'],
  }
  const current = externalRecord({
    signup_enabled: false,
    link_enabled: false,
    login_rules: ruleSet('all', [{ field_id: 'external.score', operator: 'eq', value: 10 }]),
  })
  const calls = await routeExternalRules(page, current, { login: [numberField] })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  const details = drawer.getByTestId('auth-connection-policy-details-login')
  const input = details.getByPlaceholder('输入数字，如 3.14')
  await input.fill('1e')
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(details).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-inactive-policy-warning-login')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()

  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(input).toBeVisible()
  await expect(input).toHaveValue('1e')
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await drawer.getByTestId('auth-connection-edit-inactive-policy-login').click()
  await expect(input).toHaveValue('1e')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()
  await input.fill('12.5')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await expect(input).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-purpose-login')).toHaveAttribute(
    'aria-checked',
    'false',
  )
  await drawer.getByTestId('auth-connection-hide-inactive-policy-login').click()
  await expect(details).toBeHidden()
  expect(calls.patches).toHaveLength(0)

  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toMatchObject({
    policy: {
      login_enabled: false,
      login_rules: ruleSet('all', [{ field_id: 'external.score', operator: 'eq', value: 12.5 }]),
    },
  })
})

test('repairs rejected signup requirements while keeping signup and login closed', async ({
  page,
}) => {
  const current = externalRecord({
    link_enabled: false,
    signup: { email_requirement: 'required', password_requirement: 'none' },
    signup_rules: ruleSet('all', [
      { field_id: 'registration.email_domain', operator: 'in', value: ['example.com'] },
    ]),
  })
  const calls = await routeExternalRules(
    page,
    current,
    { signup: [domainField('registration.email_domain')] },
    async (route, call) => {
      const changes = call.body?.changes as { policy?: ApiSchemas['AuthConnectionPolicy'] }
      if (changes.policy?.signup?.email_requirement !== 'optional') return false
      await json(route, { detail: '注册邮箱必须设为必填' }, 400)
      return true
    },
  )
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  const details = drawer.getByTestId('auth-connection-policy-details-signup')
  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '可选')
  await expect(drawer.getByTestId('auth-connection-catalog-error-signup')).toContainText(
    '注册邮箱必须设为必填',
  )
  await drawer.getByTestId('auth-connection-purpose-signup').click()
  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect(drawer.getByTestId('auth-connection-inactive-policy-warning-signup')).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-purpose-signup')).toBeDisabled()
  await expect(details).toBeHidden()
  await drawer.getByTestId('auth-connection-edit-inactive-policy-signup').click()
  await expect(details).toBeVisible()
  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await expect(details).toBeVisible()
  await drawer.getByTestId('auth-connection-hide-inactive-policy-signup').click()
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  expect(calls.patches).toHaveLength(0)

  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toEqual({
    expected_revision: current.aggregate_revision,
    policy: {
      ...current.configuration.policy,
      login_enabled: false,
      signup_enabled: false,
    },
  })
})

test('keeps catalog failures reachable for hidden signup and hides it again after retry', async ({
  page,
}) => {
  const current = externalRecord({
    login_enabled: false,
    signup_enabled: false,
    link_enabled: false,
  })
  const calls = await routeExternalRules(page, current)
  let failSignupCatalog = true
  await page.route('**/api/v1/admin/auth-connections/company/attributes**', (route) => {
    const request = route.request()
    const purpose =
      request.method() === 'POST'
        ? request.postDataJSON().purpose
        : new URL(request.url()).searchParams.get('purpose')
    if (purpose === 'signup' && failSignupCatalog)
      return json(route, { detail: '注册属性暂时不可用' }, 503)
    return route.fallback()
  })
  const drawer = await openExternalEditor(page)
  const error = drawer.getByTestId('auth-connection-catalog-error-signup')
  await expect(error).toContainText('注册属性暂时不可用')
  await expect(drawer.getByTestId('auth-connection-purpose-signup')).toBeDisabled()
  await expect(drawer.getByTestId('auth-connection-policy-details-signup')).toBeHidden()
  await drawer.getByTestId('auth-connection-purpose-link').click()
  await expect(error).toBeVisible()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()

  failSignupCatalog = false
  await drawer.getByTestId('auth-connection-catalog-retry-signup').click()
  await expect(error).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-policy-signup')).toBeHidden()
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  expect(calls.patches).toHaveLength(0)
  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toMatchObject({
    policy: { login_enabled: false, signup_enabled: false, link_enabled: true },
  })
})

test('ignores stale candidate failures while signup without login is invalid and recovers', async ({
  page,
}) => {
  const current = externalRecord()
  let releaseFailures!: () => void
  const failureGate = new Promise<void>((resolve) => {
    releaseFailures = resolve
  })
  let pendingCount = 0
  const calls = await routeExternalRules(
    page,
    current,
    {
      login: [emailField],
      signup: [domainField('registration.email_domain')],
      link: [domainField('external.email_domain')],
    },
    async (route) => {
      if (++pendingCount > 3) return false
      await failureGate
      await json(route, { detail: '过时错误' }, 400)
      return true
    },
  )
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect.poll(() => pendingCount).toBe(3)
  const candidateCalls = calls.catalogs.filter((call) => call.method === 'POST')
  expect(candidateCalls.map((call) => call.purpose).sort()).toEqual(['link', 'login', 'signup'])
  for (const call of candidateCalls)
    expect(call.body).toMatchObject({
      expected_revision: current.aggregate_revision,
      purpose: call.purpose,
      changes: { policy: { signup: { email_requirement: 'required' } } },
    })

  await drawer.getByTestId('auth-connection-purpose-login').click()
  const warning = drawer.getByTestId('auth-connection-signup-login-warning')
  await expect(warning).toHaveText(
    '允许注册需要同时开启“允许登录”。请开启“允许登录”，或关闭“允许注册”。当前修改尚未保存。',
  )
  await expect(drawer.locator('.n-spin-body')).toHaveCount(0)
  expect(calls.catalogs.filter((call) => call.method === 'POST')).toHaveLength(3)
  const staleResponses = purposes.map((purpose) =>
    page.waitForResponse((response) => {
      const request = response.request()
      return (
        request.method() === 'POST' &&
        request.url().includes('/company/attributes') &&
        request.postDataJSON().purpose === purpose &&
        response.status() === 400
      )
    }),
  )
  releaseFailures()
  await Promise.all((await Promise.all(staleResponses)).map((response) => response.finished()))
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
      }),
  )
  for (const purpose of purposes)
    await expect(drawer.getByTestId(`auth-connection-catalog-error-${purpose}`)).toHaveCount(0)
  await expect(drawer.locator('.n-spin-body')).toHaveCount(0)
  await expect(warning).toBeVisible()
  expect(calls.catalogs.filter((call) => call.method === 'POST')).toHaveLength(3)
  expect(calls.patches).toHaveLength(0)

  await drawer.getByTestId('auth-connection-purpose-login').click()
  await expect.poll(() => pendingCount).toBe(6)
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await expect(warning).toBeHidden()
  for (const purpose of purposes)
    await expect(drawer.getByTestId(`auth-connection-catalog-error-${purpose}`)).toHaveCount(0)
  await expect(drawer.locator('.n-spin-body')).toHaveCount(0)
  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toEqual({
    expected_revision: current.aggregate_revision,
    policy: {
      ...current.configuration.policy,
      signup: { ...current.configuration.policy.signup, email_requirement: 'required' },
    },
  })
})

test('posts every purpose catalog with the actual candidate after signup requirements change', async ({
  page,
}) => {
  const current = externalRecord()
  const fields = {
    login: [emailField],
    signup: [domainField('registration.email_domain')],
    link: [domainField('external.email_domain')],
  }
  const calls = await routeExternalRules(page, current, fields)
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect.poll(() => calls.catalogs.filter((call) => call.method === 'POST').length).toBe(3)

  const candidateCalls = calls.catalogs.filter((call) => call.method === 'POST')
  expect(candidateCalls.map((call) => call.purpose).sort()).toEqual(['link', 'login', 'signup'])
  for (const call of candidateCalls) {
    expect(call.body).toMatchObject({
      expected_revision: current.aggregate_revision,
      purpose: call.purpose,
      changes: {
        policy: {
          signup: { email_requirement: 'required', password_requirement: 'none' },
        },
      },
    })
  }
})

test('saves rule policy through a flat expected-revision PATCH without changes', async ({
  page,
}) => {
  const current = externalRecord()
  const fields = {
    login: [emailField],
    signup: [domainField('registration.email_domain')],
    link: [domainField('external.email_domain')],
  }
  const calls = await routeExternalRules(page, current, fields)
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)
  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect.poll(() => calls.catalogs.filter((call) => call.method === 'POST').length).toBe(3)
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()

  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(Object.keys(calls.patches[0]!)).toEqual(['expected_revision', 'policy'])
  expect(calls.patches[0]).toMatchObject({
    expected_revision: current.aggregate_revision,
    policy: {
      signup: { email_requirement: 'required', password_requirement: 'none' },
    },
  })
  expect(calls.patches[0]).not.toHaveProperty('changes')
})

test('keeps unknown fields and operators visible while disabling save', async ({ page }) => {
  const current = externalRecord({
    login_rules: ruleSet('all', [
      { field_id: 'removed.attribute', operator: 'eq', value: 'kept' },
      {
        field_id: 'external.email',
        operator: 'future_operator' as ApiSchemas['AuthRuleCondition']['operator'],
        value: 'kept',
      },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [emailField],
    signup: [],
    link: [],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await expect(drawer).toContainText('未知字段：removed.attribute')
  await expect(drawer).toContainText('future_operator')
  await expect(drawer).toContainText('当前条件使用了客户端不认识的运算符')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()
  expect(calls.patches).toHaveLength(0)
})

test('requires explicit registration email before saving registration.email_domain rules', async ({
  page,
}) => {
  const current = externalRecord({
    signup: { email_requirement: 'optional', password_requirement: 'none' },
    signup_rules: ruleSet('all', [
      { field_id: 'registration.email_domain', operator: 'in', value: ['example.com'] },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [],
    signup: [domainField('registration.email_domain')],
    link: [],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await expect(drawer).toContainText('该规则引用平台注册邮箱域名')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeDisabled()
  await chooseSelect(page, drawer.getByTestId('auth-connection-signup-email-requirement'), '必填')
  await expect.poll(() => calls.catalogs.filter((call) => call.method === 'POST').length).toBe(3)
  await expect(drawer).not.toContainText('该规则引用平台注册邮箱域名')
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
})

test('does not require registration email for external.email_domain rules', async ({ page }) => {
  const current = externalRecord({
    link_rules: ruleSet('all', [
      { field_id: 'external.email_domain', operator: 'not_in', value: ['blocked.example'] },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [],
    signup: [],
    link: [domainField('external.email_domain')],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await expect(drawer).not.toContainText('该规则引用平台注册邮箱域名')
  await chooseSelect(
    page,
    drawer.getByTestId('auth-connection-signup-password-requirement'),
    '可选',
  )
  await expect.poll(() => calls.catalogs.filter((call) => call.method === 'POST').length).toBe(3)
  await expect(drawer.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await drawer.getByTestId('auth-connection-save-configuration').click()
  await expect.poll(() => calls.patches.length).toBe(1)
  expect(calls.patches[0]).toMatchObject({
    policy: {
      signup: { email_requirement: 'optional', password_requirement: 'optional' },
      link_rules: ruleSet('all', [
        { field_id: 'external.email_domain', operator: 'not_in', value: ['blocked.example'] },
      ]),
    },
  })
})

test('keeps any when adding the allow-domain template to an existing any rule', async ({
  page,
}) => {
  const current = externalRecord({
    login_rules: ruleSet('any', [
      { field_id: 'external.email_domain', operator: 'eq', value: 'legacy.example' },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [domainField('external.email_domain')],
    signup: [],
    link: [],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await drawer.getByRole('button', { name: '添加允许域名' }).click()
  const dialog = page.locator('.n-dialog:visible')
  await expect(dialog).toContainText('当前规则任一条件满足即可通过')
  await dialog.getByRole('button', { name: '添加并保留任一满足' }).click()
  await expect(dialog).toBeHidden()
  await expect(
    drawer.locator('.n-radio-button--checked').filter({ hasText: '任一满足' }),
  ).toBeVisible()
  await expect(drawer.getByText('条件 2', { exact: true })).toBeVisible()
  expect(calls.patches).toHaveLength(0)
})

test('converts any to all when adding the block-domain template', async ({ page }) => {
  const current = externalRecord({
    login_rules: ruleSet('any', [
      { field_id: 'external.email_domain', operator: 'eq', value: 'legacy.example' },
    ]),
  })
  const calls = await routeExternalRules(page, current, {
    login: [domainField('external.email_domain')],
    signup: [],
    link: [],
  })
  const drawer = await openExternalEditor(page)
  await waitForCatalogs(page, calls.catalogs)

  await drawer.getByRole('button', { name: '添加拒绝域名' }).click()
  const dialog = page.locator('.n-dialog:visible')
  await dialog.getByRole('button', { name: '添加并改为全部满足' }).click()
  await expect(dialog).toBeHidden()
  await expect(
    drawer.locator('.n-radio-button--checked').filter({ hasText: '全部满足' }),
  ).toBeVisible()
  await expect(drawer.getByText('条件 2', { exact: true })).toBeVisible()
  expect(calls.patches).toHaveLength(0)
})
