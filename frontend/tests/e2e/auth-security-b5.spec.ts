import { expect, test } from '@playwright/test'
import { json, loginMethods, mockApp } from './fixtures'

test('password proof verifies the chosen link action before beginning the external flow', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/users/me/login-methods', (route) =>
    json(route, {
      ...loginMethods,
      available_connections: [
        { connection_id: 'company', display_name: 'Company', can_link: true },
      ],
    }),
  )
  const requests: unknown[] = []
  await page.route('**/api/v1/auth/reauthenticate', (route) => {
    requests.push(route.request().postDataJSON())
    return json(route, {
      reauthentication_proof: 'scoped-link-proof',
      expires_at: new Date(Date.now() + 120_000).toISOString(),
    })
  })
  await page.goto('/settings/security')
  await page.getByRole('button', { name: '绑定', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('使用密码验证').fill('untrimmed password ')
  await expect(dialog.getByLabel('使用密码验证')).toHaveValue('untrimmed password ')
  await expect(dialog.getByLabel('使用密码验证')).toBeFocused()
  await dialog.getByRole('button', { name: '验证', exact: true }).click()
  await expect
    .poll(() => requests)
    .toEqual([
      {
        method: 'password',
        password: 'untrimmed password ',
        action: 'link',
        target_id: 'company',
      },
    ])
  await expect(dialog.getByText('身份已验证，请确认本次操作。', { exact: true })).toBeVisible()
  await expect(dialog.locator('input[autocomplete="current-password"]')).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: '继续', exact: true })).toBeEnabled()
})
