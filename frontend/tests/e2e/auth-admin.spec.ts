import { expect, test } from '@playwright/test'
import { mockApp, json } from './fixtures'
import type { AuthConnection } from '../../src/api/auth-connections'

const rules = { mode: 'unrestricted' as const }
const diagnostics = {
  login: { status: 'pass', retryable: false, conditions: [] },
  signup: { status: 'pass', retryable: false, conditions: [] },
  link: { status: 'pass', retryable: false, conditions: [] },
}
const fixture = (): AuthConnection => ({
  id: 'company',
  kind: 'oidc',
  aggregate_revision: 3,
  draft_revision: 3,
  archived: false,
  has_client_secret: true,
  draft: {
    revision: 3,
    display_name: 'Company SSO',
    description: 'Work identity',
    display_order: 0,
    has_client_secret: true,
    oidc: {
      issuer: 'https://identity.example.com',
      client_id: 'client',
      scopes: ['openid', 'profile'],
      client_auth_method: 'client_secret_basic',
      userinfo_enabled: true,
    },
    attribute_mappings: [],
    evidence_max_age_seconds: 3600,
    policy: {
      enabled: true,
      login_enabled: true,
      signup_enabled: false,
      link_enabled: false,
      login_rules: rules,
      signup_rules: rules,
      link_rules: rules,
    },
  },
})

test('draft changes invalidate publication and a safety conflict retains remediation', async ({
  page,
}) => {
  await mockApp(page)
  let record = fixture()
  let published = 0
  let saved: Record<string, unknown> | undefined
  await page.route('**/api/v1/admin/auth-connections**', (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/attributes'))
      return json(route, { revision: record.draft_revision, fields: [], operators: {} })
    if (path.endsWith('/check'))
      return json(route, {
        valid: true,
        fresh_supported: true,
        revision: record.draft_revision,
        issues: [],
        rules: diagnostics,
      })
    if (path.endsWith('/impact'))
      return json(route, {
        revision: record.draft_revision,
        affected_users: [],
        blocked_users: [],
        affected_sessions: 0,
        can_publish: true,
      })
    if (path.endsWith('/publish')) {
      published++
      expect(route.request().postDataJSON()).toEqual({ expected_revision: 4 })
      return json(
        route,
        {
          title: 'Publication blocked',
          detail: 'Another administrator changed access',
          impact: {
            affected_users: [42],
            blocked_users: [42],
            affected_sessions: 2,
            can_publish: false,
            remediation: 'Verify an alternative login for user 42',
          },
        },
        409,
      )
    }
    if (route.request().method() === 'PATCH') {
      saved = route.request().postDataJSON()
      expect(saved!.expected_revision).toBe(3)
      record = {
        ...record,
        aggregate_revision: 4,
        draft_revision: 4,
        draft: { ...record.draft!, display_name: saved!.display_name as string, revision: 4 },
      }
      return json(route, record)
    }
    return json(route, path.endsWith('/auth-connections') ? { items: [record] } : record)
  })
  await page.goto('/admin/authentication')
  await page
    .getByTestId('auth-connection-company')
    .getByRole('button', { name: '编辑草稿' })
    .click()
  const publish = page.getByRole('button', { name: '发布', exact: true })
  await expect(publish).toBeDisabled()
  await page.getByRole('button', { name: '检查配置', exact: true }).click()
  await expect(page.getByText('配置检查通过', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '影响评估', exact: true }).click()
  await page.getByRole('checkbox', { name: '确认发布此版本' }).check()
  await expect(publish).toBeEnabled()
  await page.getByRole('textbox', { name: '显示名称', exact: true }).fill('Company SSO updated')
  await expect(publish).toBeDisabled()
  await page.getByRole('button', { name: '保存草稿', exact: true }).click()
  await expect(page.getByText('草稿已保存', { exact: true })).toBeVisible()
  expect(saved).not.toHaveProperty('client_secret')
  await page.getByRole('button', { name: '检查配置', exact: true }).click()
  await expect(page.getByText('配置检查通过', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '影响评估', exact: true }).click()
  await page.getByRole('checkbox', { name: '确认发布此版本' }).check()
  await publish.click()
  await expect(page.getByText('Verify an alternative login for user 42')).toBeVisible()
  await expect(publish).toBeDisabled()
  expect(published).toBe(1)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  )
})

test('unsafe persisted numbers cannot be edited or saved', async ({ page }) => {
  await mockApp(page)
  const record = fixture()
  record.draft!.policy.login_rules = {
    mode: 'conditions',
    match: 'all',
    conditions: [{ field_id: 'integer-id', operator: 'eq', value: Number.MAX_SAFE_INTEGER + 1 }],
  }
  await page.route('**/api/v1/admin/auth-connections**', (route) =>
    new URL(route.request().url()).pathname.endsWith('/attributes')
      ? json(route, { revision: 3, fields: [], operators: {} })
      : json(route, { items: [record] }),
  )
  await page.goto('/admin/authentication')
  await page
    .getByTestId('auth-connection-company')
    .getByRole('button', { name: '编辑草稿' })
    .click()
  await expect(
    page.getByText(
      '此政策包含浏览器无法精确保留的数值，已禁止编辑和保存。请使用支持无损数值处理的管理方式修正后重新读取。',
    ),
  ).toBeVisible()
  await expect(page.getByRole('textbox', { name: '显示名称', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '保存草稿', exact: true })).toBeDisabled()
})
