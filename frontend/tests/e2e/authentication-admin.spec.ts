import { expect, test } from '@playwright/test'
import { json, mockApp } from './fixtures'

const rules = { mode: 'unrestricted' }
const diagnostics = Object.fromEntries(
  ['login', 'signup', 'link'].map((purpose) => [
    purpose,
    { status: 'pass', retryable: false, conditions: [] },
  ]),
)
const record = {
  id: 'company',
  kind: 'generic_oauth2',
  aggregate_revision: 3,
  draft_revision: 2,
  published_revision: null,
  archived: false,
  has_client_secret: true,
  identity_authority_id: 'authority-company',
  draft: {
    display_name: 'Company OAuth',
    description: '',
    display_order: 0,
    revision: 2,
    has_client_secret: true,
    attribute_mappings: [],
    evidence_max_age_seconds: 3600,
    oauth2: {
      authorization_endpoint: 'https://idp.example/authorize',
      token_endpoint: 'https://idp.example/token',
      profile_endpoint: 'https://idp.example/user',
      client_id: 'application',
      client_auth_method: 'client_secret_basic',
      scopes: [],
      subject_mapping: { pointer: '/id', type: 'integer' },
    },
    policy: {
      enabled: true,
      login_enabled: true,
      signup_enabled: true,
      link_enabled: true,
      login_rules: rules,
      signup_rules: rules,
      link_rules: rules,
    },
  },
  validation: { fresh_supported: false, fingerprint_matches: true },
}

test('a publication conflict retains the draft and exposes the required remediation', async ({
  page,
}) => {
  await mockApp(page)
  let publications = 0
  await page.route('**/api/v1/admin/auth-connections**', (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.endsWith('/attributes'))
      return json(route, { revision: 2, fields: [], operators: {} })
    if (url.pathname.endsWith('/check'))
      return json(route, {
        valid: true,
        fresh_supported: false,
        issues: [],
        revision: 2,
        rules: diagnostics,
      })
    if (url.pathname.endsWith('/impact'))
      return json(route, {
        revision: 2,
        affected_users: [2],
        blocked_users: [],
        affected_sessions: 2,
        can_publish: true,
      })
    if (url.pathname.endsWith('/publish')) {
      publications++
      return json(
        route,
        {
          type: 'urn:linguaflow:authentication-publish-blocked',
          title: '发布受阻',
          detail: '请先为用户 2 验证备用登录方式',
          status: 409,
          impact: {
            revision: 3,
            affected_users: [2],
            blocked_users: [2],
            affected_sessions: 2,
            can_publish: false,
            remediation: '请先为用户 2 验证备用登录方式',
          },
        },
        409,
      )
    }
    return json(route, url.pathname.endsWith('/company') ? record : { items: [record] })
  })
  await page.goto('/admin/authentication')
  await page
    .getByTestId('auth-connection-company')
    .getByRole('button', { name: '编辑草稿', exact: true })
    .click()
  await page.getByRole('button', { name: '检查配置', exact: true }).click()
  await expect(page.getByText('配置检查通过', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '影响评估', exact: true }).click()
  await page.getByRole('checkbox', { name: '确认发布此版本' }).check()
  await page.getByRole('button', { name: '发布', exact: true }).click()
  await expect.poll(() => publications).toBe(1)
  await expect(page.getByText('请先为用户 2 验证备用登录方式').first()).toBeVisible()
  await expect(page.getByRole('button', { name: '发布', exact: true })).toBeDisabled()
  await expect(page.getByRole('textbox', { name: '显示名称', exact: true })).toHaveValue(
    'Company OAuth',
  )
})

test('a non-administrator never fetches authentication configuration', async ({ page }) => {
  await mockApp(page, { role: 'user' })
  let reads = 0
  await page.route('**/api/v1/admin/auth-connections**', (route) => {
    reads++
    return json(route, { items: [] })
  })
  await page.goto('/admin/authentication')
  await expect(page).toHaveURL(/\/$/)
  expect(reads).toBe(0)
})

test('the built-in password policy stays read-only without calling unsupported draft endpoints', async ({
  page,
}) => {
  await mockApp(page)
  const calls: string[] = []
  await page.route('**/api/v1/admin/auth-connections**', (route) => {
    calls.push(new URL(route.request().url()).pathname)
    return json(route, {
      items: [
        {
          id: 'password',
          kind: 'password',
          aggregate_revision: 1,
          published_revision: 1,
          draft_revision: null,
          archived: false,
          has_client_secret: false,
          published: { ...record.draft, display_name: 'Password', revision: 1, oauth2: undefined },
        },
      ],
    })
  })
  await page.goto('/admin/authentication')
  await page
    .getByTestId('auth-connection-password')
    .getByRole('button', { name: '查看政策' })
    .click()
  await expect(
    page.getByText('当前后端仅开放内置密码政策的读取。公开注册总开关可在系统设置中调整。'),
  ).toBeVisible()
  await expect(page.getByRole('textbox', { name: '显示名称', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '保存草稿', exact: true })).toHaveCount(0)
  expect(calls).toEqual(['/api/v1/admin/auth-connections'])
})
