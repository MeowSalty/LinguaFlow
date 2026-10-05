import { expect, test, type Page } from '@playwright/test'
import { mockStorageVisual } from './storage-visual-fixtures'
import { storageAdminTab, storageDrawerTab } from './storage-management-helpers'
import { json } from './fixtures'

async function noHorizontalOverflow(page: Page) {
  expect(
    await page.evaluate(() => ({
      viewport: innerWidth,
      scroll: document.documentElement.scrollWidth,
    })),
  ).toEqual({
    viewport: await page.evaluate(() => innerWidth),
    scroll: await page.evaluate(() => innerWidth),
  })
}

test('storage groups preview three spaces, preserve focus and load history only on demand', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.goto('/settings/storage')
  const group = page.locator('[data-connection-id="1"]')
  await expect(group.locator('[data-storage-space-id]')).toHaveCount(3)
  expect(state.requests.filter((request) => request.path.endsWith('/checks'))).toHaveLength(0)
  await group.getByRole('button', { name: /查看其余|展开.*空间|查看.*空间/ }).click()
  await expect(group.locator('[data-storage-space-id]')).toHaveCount(7)
  const trigger = group.getByRole('button', { name: '查看详情', exact: true })
  await trigger.focus()
  await page.keyboard.press('Enter')
  const drawer = page.locator('.n-drawer:visible')
  await expect(drawer.getByRole('tab', { name: '存储空间', exact: true })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  await expect(drawer.locator('[data-storage-space-id]')).toHaveCount(7)
  await storageDrawerTab(page, '连接与授权')
  expect(state.requests.filter((request) => request.path.endsWith('/checks'))).toHaveLength(0)
  await storageDrawerTab(page, '检测历史')
  await expect(drawer.locator('summary').filter({ hasText: '#10' })).toBeVisible()
  expect(state.requests.filter((request) => request.path.endsWith('/checks'))).toHaveLength(1)
  await storageDrawerTab(page, '空间')
  await storageDrawerTab(page, '检测历史')
  expect(state.requests.filter((request) => request.path.endsWith('/checks'))).toHaveLength(1)
  await page.keyboard.press('Escape')
  await expect(drawer).toHaveCount(0)
  await expect(trigger).toBeFocused()
  expect(state.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

test('administrator tabs retain policy bytes and drafts while diagnostics stay read-only', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.goto('/admin/storage')
  await expect(page.getByRole('tab', { name: '概览', exact: true })).toHaveAttribute(
    'aria-selected',
    'true',
  )
  await expect(page.getByText('诊断摘要', { exact: true })).toBeVisible()
  await expect(page.getByRole('progressbar')).toHaveCount(0)
  await storageAdminTab(page, '存储策略')
  const quota = page.getByRole('textbox', { name: '逻辑配额', exact: true })
  await expect(quota).toHaveValue('100')
  await quota.fill('125')
  await storageAdminTab(page, '存储连接')
  await expect(page.getByRole('button', { name: '查看详情', exact: true })).toBeVisible()
  await storageAdminTab(page, '概览')
  await expect(page.getByTestId('diagnostic-space-11')).toContainText('项目原始文件')
  await storageAdminTab(page, '存储策略')
  await expect(quota).toHaveValue('125')
  await expect(page.getByText('134,217,728,000 字节', { exact: true })).toBeVisible()
  await page.locator('.n-base-selection').last().click()
  await page.locator('.n-base-select-option').filter({ hasText: /^MiB$/ }).click()
  await expect(quota).toHaveValue('128000')
  await expect(page.getByText('134,217,728,000 字节', { exact: true })).toBeVisible()
  expect(state.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
  expect(state.requests.filter((request) => request.path.endsWith('/check'))).toHaveLength(0)
})

test('storage tabs support roving keyboard focus and expose the active panel', async ({ page }) => {
  const state = await mockStorageVisual(page)
  await page.goto('/admin/storage')
  const overview = page.getByRole('tab', { name: '概览', exact: true })
  await overview.focus()
  await page.keyboard.press('End')
  await expect(page.getByRole('tab', { name: '存储策略', exact: true })).toBeFocused()
  await expect(page.getByRole('tabpanel', { name: '存储策略', exact: true })).toBeVisible()
  await page.keyboard.press('Home')
  await expect(overview).toBeFocused()
  await page.keyboard.press('ArrowRight')
  await expect(page.getByRole('tab', { name: '存储连接', exact: true })).toBeFocused()
  await expect(page.getByRole('tab', { name: '存储策略', exact: true })).toHaveAttribute(
    'tabindex',
    '-1',
  )
  await page.getByRole('button', { name: '查看详情', exact: true }).click()
  const drawer = page.locator('.n-drawer:visible')
  const spaces = drawer.getByRole('tab', { name: '存储空间', exact: true })
  await spaces.focus()
  await page.keyboard.press('ArrowRight')
  await expect(drawer.getByRole('tab', { name: '连接与授权', exact: true })).toBeFocused()
  await page.keyboard.press('End')
  await expect(drawer.getByRole('tabpanel', { name: '检测历史', exact: true })).toBeVisible()
  await page.keyboard.press('Home')
  await expect(spaces).toBeFocused()
  await page.keyboard.press('ArrowLeft')
  await expect(drawer.getByRole('tab', { name: '检测历史', exact: true })).toBeFocused()
  expect(state.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

test('main refresh invalidates previously loaded history before its next visit', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.goto('/settings/storage')
  await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
  await storageDrawerTab(page, '检测历史')
  await expect(page.locator('.n-drawer:visible summary').filter({ hasText: '#10' })).toBeVisible()
  await storageDrawerTab(page, '空间')
  await page.keyboard.press('Escape')
  await expect(page.locator('.n-drawer:visible')).toHaveCount(0)
  await page.getByRole('button', { name: '刷新存储', exact: true }).click()
  await expect
    .poll(() => state.requests.filter((request) => request.path === '/storage/connections').length)
    .toBe(2)
  await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
  expect(state.requests.filter((request) => request.path.endsWith('/checks'))).toHaveLength(1)
  await storageDrawerTab(page, '检测历史')
  await expect
    .poll(() => state.requests.filter((request) => request.path.endsWith('/checks')).length)
    .toBe(2)
  expect(state.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})

test('unknown capacity is distinct from zero and does not invent a usage percentage', async ({
  page,
}) => {
  const state = await mockStorageVisual(page, { scenario: 'unknown' })
  state.spaces[1]!.reserved_bytes = 0
  state.spaces[1]!.candidate_bytes = 0
  state.spaces[1]!.live_bytes = 0
  state.spaces[1]!.pending_delete_bytes = 0
  await page.goto('/settings/storage')
  const unknown = page.locator('[data-storage-space-id="11"]')
  const zero = page.locator('[data-storage-space-id="12"]')
  await expect(unknown.getByText('—', { exact: true })).toBeVisible()
  await expect(unknown.getByRole('progressbar')).toHaveCount(0)
  await expect(zero.getByText('0 B', { exact: true })).toBeVisible()
  await expect(zero.getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
})

test('diagnostic cursor navigation revisits known pages without inventing a total', async ({
  page,
}) => {
  await mockStorageVisual(page)
  const cursors: Array<string | null> = []
  await page.route('**/api/v1/admin/storage/diagnostics**', (route) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor')
    cursors.push(cursor)
    return json(route, {
      spaces: [
        {
          id: cursor ? 77 : 11,
          reserved_bytes: 0,
          candidate_bytes: 0,
          live_bytes: 1024,
          pending_delete_bytes: 0,
          unchecked_objects: 0,
          missing_objects: 0,
          corrupt_objects: 0,
        },
      ],
      next_cursor: cursor ? undefined : 55,
      temporary_bytes: 0,
      recovery_backlog: 0,
      blocked_cleanup_by_code: {},
      migrations_by_phase: {},
    })
  })
  await page.goto('/admin/storage')
  await expect(page.getByTestId('diagnostic-space-11')).toBeVisible()
  await expect(page.getByRole('button', { name: '上一页', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '下一页', exact: true }).click()
  await expect(page.getByTestId('diagnostic-space-77')).toBeVisible()
  await expect(page.getByRole('button', { name: '下一页', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: '上一页', exact: true }).click()
  await expect(page.getByTestId('diagnostic-space-11')).toBeVisible()
  expect(cursors).toEqual([null, '55', null])
  await expect(page.getByText(/共\s*\d+\s*页/)).toHaveCount(0)
  await expect(page.getByRole('progressbar')).toHaveCount(0)
})

for (const width of [1440, 1024, 720, 390, 320]) {
  test(`storage reflows at ${width} CSS pixels with long names and exact byte values`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: width < 720 ? 844 : 960 })
    await mockStorageVisual(page, {
      theme: width === 390 ? 'dark' : 'light',
      scenario: 'over-capacity',
    })
    await page.goto('/settings/storage')
    await expect(page.getByText('登记占用已超过空间配额', { exact: true }).first()).toBeVisible()
    await noHorizontalOverflow(page)
    await page.getByRole('button', { name: '查看详情', exact: true }).first().click()
    const drawer = page.locator('.n-drawer:visible')
    await expect(drawer).toBeVisible()
    const bounds = await drawer.boundingBox()
    expect(bounds?.width).toBeCloseTo(Math.min(width, 640), 0)
    await drawer.locator('summary').filter({ hasText: '容量明细' }).first().click()
    await expect(drawer.getByText('2,617,245,696 字节', { exact: true }).first()).toBeVisible()
    await noHorizontalOverflow(page)
    await drawer.getByRole('button', { name: '新建空间', exact: true }).click()
    await expect(page.locator('.n-modal:visible')).toBeVisible()
    await noHorizontalOverflow(page)
    await page.goto('/admin/storage')
    await expect(page.getByText('诊断摘要', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: '展开空间 #11 的占用分项', exact: true }).click()
    await noHorizontalOverflow(page)
    await storageAdminTab(page, '存储策略')
    await noHorizontalOverflow(page)
  })
}

test('storage keeps one task entry in the header and its mobile panel stays within the viewport', async ({
  page,
}) => {
  await page.setViewportSize({ width: 320, height: 844 })
  await mockStorageVisual(page)
  for (const path of ['/settings/storage', '/admin/storage']) {
    await page.goto(path)
    const tracker = page.getByTestId('global-job-tracker')
    await expect(tracker).toHaveCount(1)
    await expect(page.locator('header [data-testid="global-job-tracker"]')).toHaveCount(1)
    const trigger = tracker.getByTestId('global-job-tracker-trigger')
    await expect(trigger).toHaveAccessibleName('当前任务 0')
    await expect(trigger).toBeVisible()
    const bounds = await trigger.boundingBox()
    expect(bounds?.y).toBeLessThan(80)
    await trigger.click()
    await expect(trigger).toHaveAttribute('aria-expanded', 'true')
    const taskPanel = page.getByTestId('global-job-tracker-panel')
    await expect(taskPanel).toBeVisible()
    const panel = await taskPanel.boundingBox()
    expect(panel!.y).toBeGreaterThanOrEqual(bounds!.y + bounds!.height)
    // Naive UI placement transforms can round by a fraction of a CSS pixel.
    expect(panel!.x).toBeGreaterThanOrEqual(15.5)
    expect(panel!.x + panel!.width).toBeLessThanOrEqual(304.5)
    expect(panel!.y + panel!.height).toBeLessThanOrEqual(844 - 16)
    await noHorizontalOverflow(page)
    await trigger.click()
  }
})

test('fractional bytes remain invalid and capacity unit changes never silently round a draft', async ({
  page,
}) => {
  const state = await mockStorageVisual(page)
  await page.goto('/admin/storage')
  await storageAdminTab(page, '存储策略')
  await page.locator('.n-base-selection').last().click()
  await page.locator('.n-base-select-option').filter({ hasText: /^B$/ }).click()
  const quota = page.getByRole('textbox', { name: '逻辑配额', exact: true })
  await quota.fill('1.5')
  await expect(quota).toHaveAttribute('aria-invalid', 'true')
  await expect(page.getByRole('button', { name: '保存更改', exact: true })).toBeDisabled()
  await quota.fill('9007199254740991')
  await page.locator('.n-base-selection').last().click()
  await page.locator('.n-base-select-option').filter({ hasText: /^TiB$/ }).click()
  await expect(page.getByText('9,007,199,254,740,991 字节', { exact: true })).toBeVisible()
  expect(state.requests.filter((request) => request.method !== 'GET')).toHaveLength(0)
})
