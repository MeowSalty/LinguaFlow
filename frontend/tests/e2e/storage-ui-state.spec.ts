import { expect, test } from '@playwright/test'
import { mockStorageVisual } from './storage-visual-fixtures'
import { json } from './fixtures'

test('a delayed space read cannot reopen a creation form after closing and reopening its drawer', async ({
  page,
}) => {
  await mockStorageVisual(page)
  await page.goto('/settings/storage')
  const group = page.locator('[data-connection-id="3"]')
  await expect(group.getByText('尚未配置空间', { exact: true })).toBeVisible()
  let release!: () => void
  let started = false
  await page.route('**/api/v1/storage/connections/3/spaces', async (route) => {
    started = true
    await new Promise<void>((resolve) => {
      release = resolve
    })
    return json(route, { items: [] })
  })
  await page.getByRole('button', { name: '刷新存储', exact: true }).click()
  await expect.poll(() => started).toBe(true)
  await group.getByRole('button', { name: '新建空间', exact: true }).click()
  await expect(page.locator('.n-drawer:visible')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.locator('.n-drawer:visible')).toHaveCount(0)
  await group.getByRole('button', { name: '查看详情', exact: true }).click()
  await expect(page.locator('.n-drawer:visible')).toBeVisible()
  release()
  await expect(
    page.locator('.n-drawer:visible').getByText('尚未配置空间', { exact: true }),
  ).toBeVisible()
  await expect(page.locator('.n-modal:visible')).toHaveCount(0)
})
