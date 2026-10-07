import { expect, test, type Page } from '@playwright/test'
import { mockStorageVisual } from './storage-visual-fixtures'
import { storageAdminTab } from './storage-management-helpers'

const logicalLabel = '每个用户或组织的内容配额'
const capacityLabel = '新站点空间默认配额'
const save = (page: Page) => page.getByRole('button', { name: '保存策略', exact: true })
const input = (page: Page, label = logicalLabel) =>
  page.getByRole('textbox', { name: label, exact: true })
const field = (page: Page, label: string) => page.locator('.n-form-item').filter({ hasText: label })
async function choose(page: Page, label: string, mode: '不限额' | '设置额度') {
  await field(page, label).locator('.n-radio').filter({ hasText: mode }).click()
  await expect(field(page, label).getByRole('radio', { name: mode, exact: true })).toBeChecked()
}
async function unit(page: Page, label: string, value: string) {
  await expect(page.locator('.n-base-select-option:visible')).toHaveCount(0)
  await field(page, label).locator('.n-base-selection').click()
  await page
    .locator('.n-base-select-option:visible')
    .filter({ hasText: new RegExp(`^${value}$`) })
    .click()
  await expect(field(page, label).locator('.n-base-selection-label')).toHaveText(value)
  await expect(page.locator('.n-base-select-option:visible')).toHaveCount(0)
}
async function openPolicy(page: Page) {
  await page.goto('/admin/storage')
  await storageAdminTab(page, '存储策略')
  await expect(input(page)).toBeVisible()
}

for (const logical of [null, 1]) {
  for (const capacity of [null, 512]) {
    test(`QF-T04 saves both quotas atomically: ${logical}/${capacity}`, async ({ page }) => {
      const state = await mockStorageVisual(page)
      await openPolicy(page)
      await choose(page, logicalLabel, logical === null ? '不限额' : '设置额度')
      if (logical !== null) {
        await unit(page, logicalLabel, 'B')
        await input(page).fill('1')
      }
      await choose(page, capacityLabel, capacity === null ? '不限额' : '设置额度')
      if (capacity !== null) {
        await unit(page, capacityLabel, 'KiB')
        await input(page, capacityLabel).fill('0.5')
      }
      await expect(save(page)).toBeEnabled()
      await save(page).click()
      const puts = () =>
        state.requests.filter(
          (request) => request.path === '/admin/storage/policy' && request.method === 'PUT',
        )
      await expect.poll(() => puts().length).toBe(1)
      expect(puts()[0]!.body).toEqual({
        mode: 'both',
        default_choice: 'site',
        generation: 6,
        logical_limit_bytes: logical,
        default_space_capacity_bytes: capacity,
      })
      await expect(save(page)).toBeDisabled()
    })
  }
}

test('QF-T03 invalid text survives mode changes, unit changes and a focus refresh', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await openPolicy(page)
  await unit(page, logicalLabel, 'B')
  await input(page).fill('1e3')
  await expect(input(page)).toHaveAttribute('aria-invalid', 'true')
  await choose(page, logicalLabel, '不限额')
  await expect(input(page)).toHaveCount(0)
  await choose(page, logicalLabel, '设置额度')
  await expect(input(page)).toHaveValue('1e3')
  await unit(page, logicalLabel, 'KiB')
  await expect(input(page)).toHaveValue('1e3')
  state.policy = { ...state.policy, generation: 7, logical_limit_bytes: 1024 }
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByRole('button', { name: '核对差异', exact: true })).toBeVisible()
  await expect(input(page)).toHaveValue('1e3')
  await expect(save(page)).toBeDisabled()
  expect(state.requests.filter((request) => request.method === 'PUT')).toHaveLength(0)
})

test('QF-T04 restricted mode prevents saving a default-quota-only change', async ({ page }) => {
  const state = await mockStorageVisual(page)
  state.policy.allowed_policy_modes = ['site_only']
  await openPolicy(page)
  await choose(page, capacityLabel, '设置额度')
  await input(page, capacityLabel).fill('200')
  await expect(save(page)).toBeDisabled()
  expect(state.requests.filter((request) => request.method === 'PUT')).toHaveLength(0)
  expect(state.policy.mode).toBe('both')
})

test('QF-T03 retains inactive finite input across policy tab changes without mounting connection management', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await openPolicy(page)
  await unit(page, logicalLabel, 'KiB')
  await input(page).fill('1e3')
  await choose(page, logicalLabel, '不限额')
  await storageAdminTab(page, '概览')
  await storageAdminTab(page, '存储策略')
  await choose(page, logicalLabel, '设置额度')
  await expect(input(page)).toHaveValue('1e3')
  await expect(field(page, logicalLabel).locator('.n-base-selection-label')).toHaveText('KiB')
  await expect(save(page)).toBeDisabled()
  expect(state.requests.some((request) => request.path.endsWith('/connections'))).toBe(false)
  expect(state.requests.some((request) => request.method !== 'GET')).toBe(false)
})

test('QF-T06 a lost policy response requires reading, review and a separate save', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  state.policyResponse = 'lost'
  await openPolicy(page)
  await input(page).fill('101')
  await save(page).click()
  const puts = () =>
    state.requests.filter(
      (request) => request.path === '/admin/storage/policy' && request.method === 'PUT',
    )
  await expect(page.getByTestId('policy-unknown-review')).toBeVisible()
  expect(puts()).toHaveLength(1)
  await expect(save(page)).toBeDisabled()
  await storageAdminTab(page, '概览')
  await storageAdminTab(page, '存储策略')
  await expect(page.getByTestId('policy-unknown-review')).toBeVisible()
  state.policy = { ...state.policy, generation: 8, logical_limit_bytes: 102 * 1024 ** 3 }
  await page.getByRole('button', { name: '读取最新策略', exact: true }).click()
  const keep = page.getByRole('button', { name: '核对并保留草稿', exact: true })
  await expect(keep).toBeEnabled()
  await expect(save(page)).toBeDisabled()
  expect(puts()).toHaveLength(1)
  await keep.click()
  await page
    .locator('.n-dialog:visible')
    .getByRole('button', { name: '确认已核对', exact: true })
    .click()
  await expect(page.getByTestId('policy-unknown-review')).toHaveCount(0)
  await expect(input(page)).toHaveValue('101')
  await expect(save(page)).toBeEnabled()
  expect(puts()).toHaveLength(1)
  state.policyResponse = 'ok'
  await save(page).click()
  await expect.poll(() => puts().length).toBe(2)
  expect(puts()[1]!.body).toMatchObject({ generation: 8, logical_limit_bytes: 101 * 1024 ** 3 })
})
