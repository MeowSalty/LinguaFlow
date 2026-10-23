import { expect, test, type Page, type Route } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { authCapabilities, json, localAccountPolicy, mockApp } from './fixtures'

const impactResult: ApiSchemas['AuthConnectionImpactResult'] = {
  affected_users: [],
  blocked_users: [],
  affected_sessions: 0,
  can_save: true,
}

const localRecord = (
  revision = 1,
  policy: ApiSchemas['LocalAccountPolicyInput'] = localAccountPolicy,
): ApiSchemas['LocalAccount'] => ({
  revision,
  policy,
  capabilities: authCapabilities.local_account,
  issues: [],
})

const attributeCatalog = (revision: number): ApiSchemas['AuthAttributeCatalog'] => ({
  revision,
  purpose: 'signup',
  fields: [
    {
      field_id: 'registration.email_domain',
      label: '注册邮箱域名',
      type: 'string',
      source: 'derived',
      pointer: '/email_domain',
      user_editable: false,
      allowed_operators: ['eq', 'ne', 'in', 'not_in'],
      semantic_format: 'email_domain',
      normalization_version: 'idna-v1',
      derived_from: ['registration.email'],
    },
  ],
  operators: {},
})

const chooseSelect = async (page: Page, select: ReturnType<Page['locator']>, label: string) => {
  await select.locator('.n-base-selection').click()
  await page
    .locator('.n-base-select-menu:visible .n-base-select-option')
    .filter({ hasText: label })
    .first()
    .click()
}

async function openLocalEditor(page: Page, conditions = false) {
  await page.goto('/admin/authentication')
  await expect(page.getByTestId('auth-public-registration-state')).toHaveText('已开启')
  await expect(page.getByTestId('auth-local-account-summary')).toContainText('密码登录')
  await page.getByTestId('auth-local-account-edit').click()
  await expect(page.getByTestId('auth-local-account-editor')).toBeVisible()
  if (conditions) await expect(page.getByRole('button', { name: '添加条件' })).toBeEnabled()
}

async function routeCatalog(page: Page, requests: number[] = [], log: string[] = []) {
  await page.route('**/api/v1/admin/local-account/attributes**', (route: Route) => {
    const revision = Number(new URL(route.request().url()).searchParams.get('expected_revision'))
    requests.push(revision)
    log.push(`catalog:${revision}`)
    expect(new URL(route.request().url()).searchParams.get('purpose')).toBe(null)
    return json(route, attributeCatalog(revision))
  })
  return requests
}

test('keeps an unknown rule condition blocked from saving', async ({ page }) => {
  await mockApp(page)
  const record = localRecord(1, {
    ...localAccountPolicy,
    signup: {
      ...localAccountPolicy.signup,
      rules: {
        mode: 'conditions',
        match: 'all',
        conditions: [{ field_id: 'removed.attribute', operator: 'eq', value: 'kept' }],
      },
    },
  })
  await page.route('**/api/v1/admin/local-account', (route) => json(route, record))
  await routeCatalog(page)

  await openLocalEditor(page, true)
  await expect(page.getByTestId('auth-local-account-editor')).toContainText('未知字段')
  await expect(page.getByTestId('auth-local-account-save')).toBeDisabled()
  await expect(page.getByTestId('auth-local-account-impact')).toBeDisabled()
  await expect(page.getByTestId('auth-local-account-preview')).toBeDisabled()
})

test('runs preview through the dialog and lets the server diagnose a missing email', async ({
  page,
}) => {
  await mockApp(page)
  await routeCatalog(page)
  const previews: Array<Record<string, unknown>> = []
  let failed = false
  await page.route('**/api/v1/admin/local-account/preview', (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>
    previews.push(body)
    if (!failed)
      return json(
        route,
        {
          type: 'urn:linguaflow:validation-failed',
          title: 'registration.email is required',
          status: 422,
          issues: [],
        },
        422,
      )
    return json(route, {
      revision: 1,
      simulated: true,
      attributes: {},
      signup: { status: 'pass', retryable: false, conditions: [] },
    })
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-preview').click()
  const dialog = page.locator('.n-modal:visible')
  await expect(dialog).toBeVisible()
  await expect(page.getByTestId('auth-local-account-preview-email')).toBeVisible()
  expect(previews).toHaveLength(0)
  await page.getByTestId('auth-local-account-preview-run').click()

  await expect(page.getByText('registration.email is required')).toBeVisible()
  expect(previews).toHaveLength(1)
  expect(previews[0]).not.toHaveProperty('registration')

  failed = true
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await page.getByTestId('auth-local-account-signup').click()
  await page.getByTestId('auth-local-account-preview').click()
  await expect(dialog).toBeVisible()
  // The draft moved on, so the stale diagnosis must not return with the reopened dialog.
  await expect(dialog).not.toContainText('registration.email is required')
  await page
    .getByTestId('auth-local-account-preview-email')
    .locator('input')
    .fill('Person@Example.com')
  await page.getByTestId('auth-local-account-preview-run').click()

  await expect(dialog).toContainText('通过')
  expect(previews).toHaveLength(2)
  expect(previews[1]).toMatchObject({
    registration: { email: 'person@example.com' },
  })
})

test('locks the preview email while running and invalidates the result when it changes', async ({
  page,
}) => {
  await mockApp(page)
  await routeCatalog(page)
  let releasePreview!: () => void
  const held = new Promise<void>((resolve) => {
    releasePreview = resolve
  })
  const requests: unknown[] = []
  await page.route('**/api/v1/admin/local-account/preview', async (route) => {
    requests.push(route.request().postDataJSON())
    await held
    return json(route, {
      revision: 1,
      simulated: true,
      attributes: {},
      signup: { status: 'pass', retryable: false, conditions: [] },
    })
  })
  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-preview').click()
  const dialog = page.locator('.n-modal:visible')
  const email = page.getByTestId('auth-local-account-preview-email').locator('input')
  await email.fill('Alice@Example.com')
  await page.getByTestId('auth-local-account-preview-run').click()
  await expect.poll(() => requests.length).toBe(1)
  await expect(email).toBeDisabled()
  releasePreview()
  await expect(dialog).toContainText('通过')
  await expect(email).toBeEnabled()
  await email.fill('bob@example.com')
  await expect(dialog.getByText('通过', { exact: true })).toHaveCount(0)
  expect(requests[0]).toMatchObject({ registration: { email: 'alice@example.com' } })
})

test('invalidates an impact result when the draft changes', async ({ page }) => {
  await mockApp(page)
  await routeCatalog(page)
  let releaseImpact: (() => void) | null = null
  const impactRequests: Array<Record<string, unknown>> = []
  let patchCalls = 0
  await page.route('**/api/v1/admin/local-account/impact', async (route) => {
    impactRequests.push(route.request().postDataJSON() as Record<string, unknown>)
    await new Promise<void>((resolve) => {
      releaseImpact = resolve
    })
    return json(route, impactResult)
  })
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() === 'PATCH') patchCalls += 1
    return route.fallback()
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-impact').click()
  await expect(page.getByTestId('auth-local-account-impact')).toBeDisabled()

  await page.getByTestId('auth-local-account-signup').click()
  await expect(page.getByTestId('auth-local-account-impact-result')).toBeHidden()
  releaseImpact?.()
  await expect.poll(async () => impactRequests.length).toBe(1)
  await expect(page.getByTestId('auth-local-account-impact')).toBeEnabled()
  await expect(page.getByTestId('auth-local-account-impact-result')).toBeHidden()
  expect(patchCalls).toBe(0)
})

test('readbacks an aborted PATCH and confirms the current policy matches', async ({ page }) => {
  await mockApp(page)
  await routeCatalog(page)
  let record = localRecord()
  const impactRequests: Array<Record<string, unknown>> = []
  let patchCalls = 0
  await page.route('**/api/v1/admin/local-account/impact', (route) => {
    impactRequests.push(route.request().postDataJSON() as Record<string, unknown>)
    return json(route, impactResult)
  })
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() === 'PATCH') {
      patchCalls += 1
      const body = route.request().postDataJSON() as Record<string, unknown>
      record = localRecord(2, body.policy as ApiSchemas['LocalAccountPolicyInput'])
      return route.abort('connectionreset')
    }
    return json(route, record)
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-signup').click()
  await page.getByTestId('auth-local-account-save').click()

  await expect(page.getByTestId('auth-local-account-unknown-write')).toContainText('目标值一致')
  await expect(page.getByTestId('auth-local-account-save')).toBeDisabled()
  expect(patchCalls).toBe(1)
  expect(impactRequests).toHaveLength(1)
})

test('treats an impact failure on save as an ordinary error, never an unknown write', async ({
  page,
}) => {
  await mockApp(page)
  await routeCatalog(page)
  const impactRequests: Array<Record<string, unknown>> = []
  let patchCalls = 0
  let reads = 0
  await page.route('**/api/v1/admin/local-account/impact', (route) => {
    impactRequests.push(route.request().postDataJSON() as Record<string, unknown>)
    return json(
      route,
      { type: 'urn:linguaflow:impact-unavailable', title: 'impact unavailable', status: 502 },
      502,
    )
  })
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() === 'PATCH') {
      patchCalls += 1
      return route.fallback()
    }
    reads += 1
    return route.fallback()
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-signup').click()
  const readsBefore = reads
  await page.getByTestId('auth-local-account-save').click()

  await expect.poll(() => impactRequests.length).toBe(1)
  await expect(page.getByTestId('auth-local-account-impact-error')).toContainText(
    'impact unavailable',
  )
  await expect(page.getByTestId('auth-local-account-unknown-write')).toBeHidden()
  await expect(page.getByTestId('auth-local-account-save')).toBeEnabled()
  expect(patchCalls).toBe(0)
  expect(reads).toBe(readsBefore)
})

test('does not PATCH when the impact check fails', async ({ page }) => {
  await mockApp(page)
  await routeCatalog(page)
  let patchCalls = 0
  await page.route('**/api/v1/admin/local-account/impact', (route) =>
    json(
      route,
      { type: 'urn:linguaflow:impact-unavailable', title: 'impact unavailable', status: 502 },
      502,
    ),
  )
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() === 'PATCH') patchCalls += 1
    return route.fallback()
  })

  await openLocalEditor(page)
  await page.getByTestId('auth-local-account-signup').click()
  await page.getByTestId('auth-local-account-impact').click()
  await expect(page.getByTestId('auth-local-account-impact-error')).toContainText(
    'impact unavailable',
  )
  expect(patchCalls).toBe(0)
  await expect(page.getByTestId('auth-local-account-save')).toBeEnabled()
})

test('reapplies my draft at revision two after a 409 conflict', async ({ page }) => {
  await mockApp(page)
  const calls: string[] = []
  const catalogRequests = await routeCatalog(page, [], calls)
  let record = localRecord()
  const impactRequests: Array<Record<string, unknown>> = []
  let patchCalls = 0
  await page.route('**/api/v1/admin/local-account/impact', (route) => {
    const body = route.request().postDataJSON() as Record<string, unknown>
    impactRequests.push(body)
    calls.push(`impact:${body.expected_revision}`)
    return json(route, impactResult)
  })
  await page.route('**/api/v1/admin/local-account', (route) => {
    if (route.request().method() !== 'PATCH') return json(route, record)
    patchCalls += 1
    calls.push('patch')
    record = localRecord(2, record.policy as ApiSchemas['LocalAccountPolicyInput'])
    return json(
      route,
      {
        type: 'urn:linguaflow:auth-connection-revision-conflict',
        title: 'revision conflict',
        status: 409,
        issues: [],
        impact: impactResult,
      },
      409,
    )
  })

  await openLocalEditor(page)
  await chooseSelect(page, page.getByTestId('auth-local-account-email-requirement'), '必填')
  await page.getByTestId('auth-local-account-save').click()
  await expect(page.getByTestId('auth-local-account-conflict')).toBeVisible()
  expect(patchCalls).toBe(1)
  expect(catalogRequests).toEqual([1])

  await page.getByTestId('auth-local-account-conflict-reapply').click()
  await expect(page.getByTestId('auth-local-account-conflict')).toBeHidden()
  await expect.poll(() => calls.length).toBe(5)
  expect(calls).toEqual(['catalog:1', 'impact:1', 'patch', 'catalog:2', 'impact:2'])
  expect(impactRequests.at(-1)).toMatchObject({
    expected_revision: 2,
    policy: {
      password_login_enabled: true,
      username_login_enabled: true,
      email_login_enabled: false,
      password_enrollment_enabled: true,
      signup: { enabled: true, email_requirement: 'required', rules: { mode: 'unrestricted' } },
    },
  })
  expect(patchCalls).toBe(1)
  await expect(page.getByTestId('auth-local-account-save')).toBeEnabled()
})

test('exposes disabled and labelled switches for accessibility', async ({ page }) => {
  await mockApp(page)
  await routeCatalog(page)
  await openLocalEditor(page)
  const emailSwitch = page.getByTestId('auth-local-account-email-login')
  const passwordSwitch = page.getByTestId('auth-local-account-password-login')
  await expect(emailSwitch).toHaveAttribute('aria-disabled', 'true')
  await expect(emailSwitch).toHaveAttribute('aria-label', '允许邮箱定位')
  await expect(passwordSwitch).toHaveAttribute('aria-label', '允许密码登录')
  await expect(emailSwitch).not.toHaveAttribute('aria-disabled', 'false')
})
