import { expect, test } from '@playwright/test'
import { mockStorageVisual } from './storage-visual-fixtures'
import { json } from './fixtures'

test('QF-T08 disks distinguish unknown, zero, roles and missing observations without writes', async ({
  page,
}) => {
  const state = await mockStorageVisual(page, { scenario: 'disk-unknown' })
  await page.goto('/admin/storage')
  const disks = page.getByTestId('storage-disk-diagnostics')
  await expect(disks).toContainText('余量未知')
  await expect(disks).toContainText('工作与临时文件、本地对象')
  await expect(disks).not.toContainText('不限额')
  state.disks = [
    {
      roles: ['work', 'future_role'] as never,
      state: 'low',
      available_bytes: 0,
      total_bytes: 1024,
      minimum_free_bytes: 10,
      observed_at: '2026-10-05T01:59:00Z',
    },
  ]
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(disks).toContainText('空间偏低')
  await expect(disks).toContainText('0 B')
  await expect(disks).toContainText('其他用途')
  await expect(disks).not.toContainText('future_role')
  state.failed = true
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(disks).toContainText('当前状态待刷新')
  await expect(disks).toContainText('0 B')
  state.failed = false
  state.disks = []
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(disks).toContainText('暂无本地磁盘观测')
  state.disks = undefined
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(disks).toContainText('后端未提供磁盘观测信息')
  expect(state.requests.every((request) => request.method === 'GET')).toBe(true)
})

test('QF-T08 pagination replaces disks and preserves separate records with shared purposes', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.route('**/api/v1/admin/storage/diagnostics*', (route) => {
    const next = new URL(route.request().url()).searchParams.has('cursor')
    return json(route, {
      spaces: [],
      temporary_bytes: 0,
      recovery_backlog: 0,
      blocked_cleanup_by_code: {},
      migrations_by_phase: {},
      disks: next
        ? [state.disks![0], { ...state.disks![0], available_bytes: 0, state: 'low' }]
        : state.disks,
      ...(next ? {} : { next_cursor: 7 }),
    })
  })
  await page.goto('/admin/storage')
  const disks = page.getByTestId('storage-disk-diagnostics')
  await expect(disks.locator('tbody tr')).toHaveCount(1)
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(disks.locator('tbody tr')).toHaveCount(2)
  await page.getByRole('button', { name: '上一页', exact: true }).click()
  await expect(disks.locator('tbody tr')).toHaveCount(1)
})

test('QF-T08 disk observations remain administrator-only', async ({ page }) => {
  const state = await mockStorageVisual(page)
  await page.goto('/settings/storage')
  await expect(page.getByTestId('storage-manager')).toBeVisible()
  await expect(page.getByTestId('storage-disk-diagnostics')).toHaveCount(0)
  expect(state.requests.some((request) => request.path.includes('/admin/'))).toBe(false)
})
