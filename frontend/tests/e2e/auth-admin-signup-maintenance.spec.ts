import { expect, test, type Page } from '@playwright/test'
import type { AuthConnection, AuthConnectionPolicy } from '../../src/api/auth-connections'
import { json } from './fixtures'
import { authDiagnostics, mockB4Connection, oidcConnection } from './auth-admin-b4-fixtures'

const maintenanceMessage =
  '当前 OAuth 连接不支持近期认证，开启注册时注册密码要求必须为“必填”，或关闭允许注册。'

function signupConnection(
  kind: AuthConnection['kind'],
  password: 'none' | 'optional',
  freshSupported = false,
): AuthConnection {
  const record = oidcConnection()
  record.kind = kind
  record.validation.fresh_supported = freshSupported
  record.configuration.policy.signup_enabled = true
  record.configuration.policy.signup!.password_requirement = password
  if (kind === 'generic_oauth2') {
    record.configuration.oidc = undefined
    record.configuration.oauth2 = {
      authorization_endpoint: 'https://identity.example.com/authorize',
      token_endpoint: 'https://identity.example.com/token',
      profile_endpoint: 'https://identity.example.com/profile',
      client_id: 'client-id',
      scopes: [],
      client_auth_method: 'client_secret_basic',
      subject_mapping: { pointer: '/id', type: 'string' },
    }
  }
  return record
}

async function openEditor(page: Page) {
  await page.goto('/admin/authentication')
  await page.getByTestId('auth-connection-company').getByRole('button', { name: '编辑' }).click()
  return page.getByTestId('auth-connection-editor')
}

async function choosePassword(page: Page, label: string) {
  await page
    .getByTestId('auth-connection-signup-password-requirement')
    .locator('.n-base-selection')
    .click()
  await page
    .locator('.n-base-select-menu:visible .n-base-select-option:visible')
    .filter({ hasText: label })
    .click()
}

for (const password of ['none', 'optional'] as const) {
  test(
    'OAuth ' + password + ' keeps the selection and requires a password or closed signup',
    async ({ page }) => {
      let record = signupConnection('generic_oauth2', password)
      const calls = await mockB4Connection(page, () => record)
      const policies: AuthConnectionPolicy[] = []
      await page.route('**/api/v1/admin/auth-connections/company', (route) => {
        if (route.request().method() !== 'PATCH') return json(route, record)
        const body = route.request().postDataJSON()
        policies.push(body.policy)
        record = {
          ...record,
          aggregate_revision: record.aggregate_revision + 1,
          configuration: { ...record.configuration, policy: body.policy },
        }
        return json(route, record)
      })
      const editor = await openEditor(page)
      const warning = editor.getByTestId('auth-connection-signup-maintenance-warning')
      const passwordSelect = editor.getByTestId('auth-connection-signup-password-requirement')
      const selectedLabel = password === 'none' ? '不需要' : '可选'
      await expect(warning).toHaveText(maintenanceMessage)
      await expect(passwordSelect).toContainText(selectedLabel)
      await expect(editor.getByTestId('auth-connection-enable')).toBeDisabled()
      expect(policies).toHaveLength(0)
      expect(calls.enable).toBe(0)

      await choosePassword(page, '必填')
      await expect(warning).toBeHidden()
      await expect(editor.getByTestId('auth-connection-save-configuration')).toBeEnabled()
      await editor.getByTestId('auth-connection-save-configuration').click()
      await expect(editor.getByTestId('auth-connection-enable')).toBeEnabled()
      expect(policies[0]?.signup?.password_requirement).toBe('required')

      await choosePassword(page, selectedLabel)
      await expect(warning).toHaveText(maintenanceMessage)
      await editor.getByTestId('auth-connection-purpose-signup').click()
      await expect(warning).toBeHidden()
      await expect(passwordSelect).toContainText(selectedLabel)
      await expect(editor.getByTestId('auth-connection-save-configuration')).toBeEnabled()
      await editor.getByTestId('auth-connection-save-configuration').click()
      await expect(editor.getByTestId('auth-connection-enable')).toBeEnabled()
      expect(policies[1]).toMatchObject({
        signup_enabled: false,
        signup: { password_requirement: password },
      })
      expect(calls.enable).toBe(0)
    },
  )

  test(
    'OIDC ' + password + ' with fresh support keeps passwordless signup available',
    async ({ page }) => {
      await mockB4Connection(page, signupConnection('oidc', password, true))
      const editor = await openEditor(page)
      const warning = editor.getByTestId('auth-connection-signup-maintenance-warning')
      await expect(warning).toBeHidden()
      await expect(editor.getByTestId('auth-connection-signup-password-requirement')).toContainText(
        password === 'none' ? '不需要' : '可选',
      )
      await expect(editor.getByTestId('auth-connection-enable')).toBeEnabled()

      await editor.getByTestId('auth-connection-check').click()
      await expect(page.getByTestId('auth-connection-check-result')).toContainText('配置检查通过')
      await page.getByTestId('auth-connection-close-check').click()
      await expect(editor.getByTestId('auth-connection-enable')).toBeEnabled()

      await editor.getByLabel('Issuer 地址').fill('https://changed.example.com')
      await expect(warning).toBeVisible()
      await expect(editor.getByTestId('auth-connection-enable')).toBeDisabled()
    },
  )
}

test('OIDC waits for a real fresh result even after configuration check passes', async ({
  page,
}) => {
  const record = signupConnection('oidc', 'none')
  const calls = await mockB4Connection(page, record, undefined, {
    start() {
      return {
        transaction_id: 'transaction-e2e',
        authorization_url: 'http://127.0.0.1:4173/auth/complete?ticket=ticket-e2e',
        expires_at: new Date(Date.now() + 120_000).toISOString(),
      }
    },
  })
  await page.route('**/api/v1/admin/auth-connections/company/check', (route) =>
    json(route, {
      revision: record.aggregate_revision,
      valid: true,
      fresh_supported: false,
      issues: [],
      rules: authDiagnostics,
    }),
  )
  const editor = await openEditor(page)
  const warning = editor.getByTestId('auth-connection-signup-maintenance-warning')
  await expect(warning).toContainText('请先通过真实测试确认此 OIDC 连接支持近期认证')
  await expect(editor.getByTestId('auth-connection-enable')).toBeDisabled()
  await editor.getByTestId('auth-connection-check').click()
  await expect(page.getByTestId('auth-connection-check-result')).toContainText('配置检查通过')
  await page.getByTestId('auth-connection-close-check').click()
  await expect(warning).toBeVisible()
  await expect(editor.getByTestId('auth-connection-enable')).toBeDisabled()

  await editor.getByTestId('auth-connection-test').click()
  await expect.poll(() => calls.exchange).toBe(1)
  await expect(warning).toBeHidden()
  await expect(editor.getByTestId('auth-connection-enable')).toBeEnabled()
})

test('a server rejection without detail shows a localized message and password field', async ({
  page,
}) => {
  const record = signupConnection('generic_oauth2', 'none')
  record.state = 'enabled'
  await mockB4Connection(page, record)
  await page.route('**/api/v1/admin/auth-connections/company', (route) => {
    if (route.request().method() !== 'PATCH') return json(route, record)
    return json(
      route,
      {
        type: 'urn:linguaflow:auth-connection-configuration-incomplete',
        title: 'auth_connection_configuration-incomplete',
        status: 409,
        issues: [
          {
            code: 'maintenance-path-required',
            field: 'policy.signup.password_requirement',
            message: 'Registration requires a maintenance path.',
          },
        ],
      },
      409,
    )
  })
  const editor = await openEditor(page)
  await choosePassword(page, '可选')
  await expect(editor.getByTestId('auth-connection-save-configuration')).toBeEnabled()
  await editor.getByTestId('auth-connection-save-configuration').click()

  await expect(page.locator('.n-message')).toContainText(maintenanceMessage)
  const issue = editor.locator('.n-alert').filter({ hasText: '配置问题' })
  await expect(issue).toContainText('注册密码要求')
  await expect(issue).toContainText(maintenanceMessage)
  await expect(editor).not.toContainText('policy.signup.password_requirement')
  await expect(page.locator('.n-message')).not.toContainText(
    'auth_connection_configuration-incomplete',
  )
})
