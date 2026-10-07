import { expect, test, type Page } from '@playwright/test'
import { mockStorageVisual } from './storage-visual-fixtures'
import { storageAction } from '../storage-fixtures'

const GiB = 1024 ** 3
async function openQuota(page: Page) {
  await page.goto('/settings/storage')
  await page
    .locator('[data-connection-id="1"]')
    .getByRole('button', { name: '查看详情', exact: true })
    .click()
  const row = page.locator('.n-drawer:visible [data-storage-space-id="11"]')
  await row.getByRole('button', { name: '调整配额', exact: true }).click()
  const modal = page
    .locator('.n-modal:visible')
    .filter({ has: page.locator('[data-storage-quota-dialog]') })
  await expect(modal).toBeVisible()
  return { row, modal }
}

test('manual creation starts with no quota selection and sends explicit unlimited without reading admin policy', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.goto('/settings/storage')
  await page
    .locator('[data-connection-id="1"]')
    .getByRole('button', { name: '查看详情', exact: true })
    .click()
  await page
    .locator('.n-drawer:visible')
    .getByRole('button', { name: '新建空间', exact: true })
    .click()
  const modal = page.locator('.n-modal:visible')
  await expect(modal.getByRole('radio', { checked: true })).toHaveCount(0)
  await expect(modal.getByRole('button', { name: '保存', exact: true })).toBeDisabled()
  const inputs = modal.locator('input')
  await inputs.nth(0).fill('Explicit unlimited space')
  await inputs.nth(1).fill('quota-bucket')
  await modal.locator('label.n-radio').filter({ hasText: '不限额' }).click()
  await modal.getByRole('button', { name: '保存', exact: true }).click()
  await expect.poll(() => state.requests.filter((item) => item.method === 'POST').length).toBe(1)
  expect(state.requests.find((item) => item.method === 'POST')?.body).toMatchObject({
    capacity_bytes: null,
  })
  expect(state.requests.some((item) => item.path === '/admin/storage/policy')).toBe(false)
})

test('lowering below 128 GiB preserves four accounts and saves the space generation', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  Object.assign(state.spaces[0]!, {
    capacity_bytes: 200 * GiB,
    reserved_bytes: 10 * GiB,
    candidate_bytes: 8 * GiB,
    live_bytes: 100 * GiB,
    pending_delete_bytes: 10 * GiB,
    available_bytes: 72 * GiB,
  })
  const { modal } = await openQuota(page)
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('100')
  await expect(modal).toContainText('拟保存后将超额 28 GiB（30,064,771,072 B）。')
  await modal.getByRole('button', { name: '保存配额', exact: true }).click()
  await expect.poll(() => state.requests.filter((item) => item.method === 'PUT').length).toBe(1)
  expect(state.requests.find((item) => item.method === 'PUT')?.body).toEqual({
    capacity_bytes: 100 * GiB,
    expected_generation: 3,
  })
  expect(state.spaces[0]).toMatchObject({
    live_bytes: 100 * GiB,
    pending_delete_bytes: 10 * GiB,
    available_bytes: 0,
  })
  await expect(modal).toContainText('128 GiB')
  await expect(modal).toContainText('28 GiB')
})

test('a stale space generation keeps the draft and needs explicit baseline review before another PUT', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  const { modal } = await openQuota(page)
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('5')
  Object.assign(state.spaces[0]!, { management_generation: 4, status: 'read_only' })
  await modal.getByRole('button', { name: '保存配额', exact: true }).click()
  await expect(
    modal.getByText('空间信息已更新。请核对原配额、当前配额和拟保存配额，再选择下一步。'),
  ).toBeVisible()
  await expect(modal.getByRole('textbox', { name: '空间配额', exact: true })).toHaveValue('5')
  expect(state.requests.filter((item) => item.method === 'PUT')).toHaveLength(1)
  await modal.getByRole('button', { name: '保留草稿并采用当前基线', exact: true }).click()
  await modal.getByRole('button', { name: '保存配额', exact: true }).click()
  await expect.poll(() => state.requests.filter((item) => item.method === 'PUT').length).toBe(2)
  expect(state.requests.filter((item) => item.method === 'PUT')[1]?.body).toEqual({
    capacity_bytes: 5 * GiB,
    expected_generation: 4,
  })
})

test('lost quota responses survive closing and reopening until the latest GET is explicitly acknowledged', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  state.quotaResponse = 'lost'
  const { modal, row } = await openQuota(page)
  await modal.locator('label.n-radio').filter({ hasText: '不限额' }).click()
  await modal.getByRole('button', { name: '保存配额', exact: true }).click()
  await expect(
    modal.getByRole('button', { name: '已核对，采用当前基线', exact: true }),
  ).toBeEnabled()
  expect(state.requests.filter((item) => item.method === 'PUT')).toHaveLength(1)
  await modal.getByRole('button', { name: '取消', exact: true }).click()
  await expect(modal).toHaveCount(0)
  await row.getByRole('button', { name: '核对配额结果', exact: true }).click()
  await expect(modal).toContainText('上次尝试值')
  await expect(modal.getByRole('button', { name: '保存配额', exact: true })).toBeDisabled()
  await modal.getByRole('button', { name: '已核对，采用当前基线', exact: true }).click()
  await expect(modal.getByRole('button', { name: '保存配额', exact: true })).toBeEnabled()
  expect(state.requests.filter((item) => item.method === 'PUT')).toHaveLength(1)
  state.quotaResponse = 'ok'
  await modal.locator('label.n-radio').filter({ hasText: '设置额度' }).click()
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('20')
  await modal.getByRole('button', { name: '保存配额', exact: true }).click()
  await expect.poll(() => state.requests.filter((item) => item.method === 'PUT').length).toBe(2)
  expect(state.requests.filter((item) => item.method === 'PUT')[1]?.body).toEqual({
    capacity_bytes: 20 * GiB,
    expected_generation: 4,
  })
})

test('invalid quota survives refresh, dirty closing asks for confirmation, and focus returns to its trigger', async ({
  page,
}) => {
  await mockStorageVisual(page)
  const { modal, row } = await openQuota(page)
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('1e3')
  await modal.getByRole('button', { name: '读取当前配额', exact: true }).click()
  await expect(modal.getByRole('textbox', { name: '空间配额', exact: true })).toHaveValue('1e3')
  await expect(modal.getByRole('button', { name: '保存配额', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: '继续编辑', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '继续编辑', exact: true }).click()
  await expect(modal.getByRole('textbox', { name: '空间配额', exact: true })).toHaveValue('1e3')
  await modal.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '放弃修改', exact: true }).click()
  await expect(row.getByRole('button', { name: '调整配额', exact: true })).toBeFocused()
})

test('space state and quota permissions remain independent', async ({ page }) => {
  const state = await mockStorageVisual(page)
  state.spaces[0]!.management_actions.set_status = storageAction(false)
  state.spaces[1]!.management_actions.set_quota = storageAction(false)
  const { modal, row } = await openQuota(page)
  await modal.getByRole('button', { name: '取消', exact: true }).click()
  await expect(row.getByRole('button', { name: '设为只读', exact: true })).toBeDisabled()
  await expect(row.getByRole('button', { name: '调整配额', exact: true })).toBeEnabled()
  const second = page.locator('.n-drawer:visible [data-storage-space-id="12"]')
  await expect(second.getByRole('button', { name: '设为只读', exact: true })).toBeEnabled()
  await expect(second.getByRole('button', { name: '调整配额', exact: true })).toBeDisabled()
})

test('losing the quota action clears the open draft without making any write', async ({ page }) => {
  const state = await mockStorageVisual(page)
  const { modal, row } = await openQuota(page)
  await modal.getByRole('textbox', { name: '空间配额', exact: true }).fill('7')
  state.spaces[0]!.management_actions.set_quota = storageAction(false)
  await modal.getByRole('button', { name: '读取当前配额', exact: true }).click()
  await expect(modal).toHaveCount(0)
  await expect(row.getByRole('button', { name: '调整配额', exact: true })).toBeDisabled()
  expect(state.requests.filter((item) => item.method === 'PUT')).toHaveLength(0)
  state.spaces[0]!.management_actions.set_quota = storageAction(true)
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: '刷新存储', exact: true }).click()
  await page
    .locator('[data-connection-id="1"]')
    .getByRole('button', { name: '查看详情', exact: true })
    .click()
  await row.getByRole('button', { name: '调整配额', exact: true }).click()
  await expect(modal.getByRole('textbox', { name: '空间配额', exact: true })).toHaveValue('10')
})
