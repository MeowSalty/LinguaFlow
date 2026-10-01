import { expect, test } from '@playwright/test'
import { json, mockApp, runtimeSample } from './fixtures'

for (const theme of ['light', 'dark'] as const) {
  test(`runtime shows an instance snapshot and null gauges in ${theme} theme`, async ({
    page,
  }, testInfo) => {
    await mockApp(page, { theme })
    await page.goto('/admin/runtime')
    await expect(page.getByTestId('runtime-instance')).toHaveText('instance-alpha')
    await expect(page.locator('html')).toHaveAttribute('data-theme', theme)
    await expect(page.getByText('不可用', { exact: true }).first()).toBeVisible()
    await expect(page.getByText('运行降级', { exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: '外部 HTTP 请求' })).toBeVisible()
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true)
    await page.screenshot({
      path: `tests/artifacts/runtime-${theme}-${testInfo.project.name}.png`,
      fullPage: true,
    })
  })
}

test('temporary failure keeps an old sample; a subsequent 403 removes it and stops sampling', async ({
  page,
}) => {
  await mockApp(page)
  let status = 200
  let requests = 0
  await page.route('**/admin/runtime/summary', (route) => {
    requests++
    return json(route, status === 200 ? runtimeSample() : { title: '测试错误', status }, status)
  })
  await page.goto('/admin/runtime')
  await expect(page.getByTestId('runtime-instance')).toHaveText('instance-alpha')
  status = 503
  await page.getByRole('button', { name: '立即刷新' }).click()
  await expect(page.getByText('刷新失败，以下为上次成功采集的快照。')).toBeVisible()
  await expect(page.getByTestId('runtime-instance')).toHaveText('instance-alpha')
  status = 403
  await page.getByRole('button', { name: '立即刷新' }).click()
  await expect(page.getByTestId('runtime-instance')).toHaveCount(0)
  await expect(page.getByRole('button', { name: '立即刷新' })).toBeDisabled()
  const count = requests
  await page.clock.install()
  await page.clock.fastForward(15_000)
  expect(requests).toBe(count)
})

test('changing instance replaces the displayed identifier and counters', async ({ page }) => {
  await mockApp(page)
  let next = runtimeSample()
  await page.route('**/admin/runtime/summary', (route) => json(route, next))
  await page.goto('/admin/runtime')
  await expect(page.getByTestId('runtime-instance')).toHaveText('instance-alpha')
  next = { ...runtimeSample('instance-beta'), external_requests: [], runners: [] }
  await page.getByRole('button', { name: '立即刷新' }).click()
  await expect(page.getByTestId('runtime-instance')).toHaveText('instance-beta')
  await expect(page.getByText('当前实例尚无外部请求记录')).toBeVisible()
  await expect(page.getByText('openai · 生成')).toHaveCount(0)
})

test('ordinary users cannot request a runtime snapshot', async ({ page }) => {
  await mockApp(page, { role: 'user' })
  let requests = 0
  await page.route('**/admin/runtime/summary', (route) => {
    requests++
    return json(route, runtimeSample())
  })
  await page.goto('/admin/runtime')
  await expect(page.getByTestId('runtime-instance')).toHaveCount(0)
  await expect(page).not.toHaveURL(/\/admin\/runtime$/)
  expect(requests).toBe(0)
})

test('a hidden page stops sampling and visibility resumes it immediately', async ({ page }) => {
  await mockApp(page)
  let requests = 0
  await page.route('**/admin/runtime/summary', (route) => {
    requests++
    return json(route, runtimeSample())
  })
  await page.clock.install()
  await page.goto('/admin/runtime')
  await expect(page.getByTestId('runtime-instance')).toBeVisible()
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  const hiddenCount = requests
  await page.clock.fastForward(20_000)
  expect(requests).toBe(hiddenCount)
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
  })
  await expect.poll(() => requests).toBe(hiddenCount + 1)
})
