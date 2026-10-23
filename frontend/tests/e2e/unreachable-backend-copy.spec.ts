import { expect, test, type Page } from '@playwright/test'
import { mockApp } from './fixtures'

/** 浏览器请求未送达时抛出的英文原文，任何界面都不应该把它当文案展示。 */
const browserOriginal = 'Failed to fetch'

/** 登录页只对匿名访问可见：清掉 mockApp 预置的令牌以免被守卫重定向到工作台。 */
async function anonymous(page: Page) {
  await mockApp(page)
  await page.addInitScript(() => {
    localStorage.removeItem('linguaflow.access_token')
    localStorage.removeItem('linguaflow.refresh_token')
  })
}

test('the login page keeps Chinese retry copy when capabilities are unreachable', async ({
  page,
}) => {
  await anonymous(page)
  await page.route('**/api/v1/auth/capabilities', (route) => route.abort('failed'))
  await page.goto('/login')
  await expect(page.getByText('暂时无法获取登录方式，请重试。')).toBeVisible()
  await expect(page.getByText(browserOriginal)).toHaveCount(0)
})

test('the admin page keeps Chinese copy on the connection list and the local account card', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/admin/auth-connections', (route) => route.abort('failed'))
  await page.route('**/api/v1/admin/local-account', (route) => route.abort('failed'))
  await page.route('**/api/v1/admin/settings', (route) => route.abort('failed'))
  await page.goto('/admin/authentication')
  await expect(page.getByText('认证连接操作失败')).toBeVisible()
  await expect(page.getByTestId('auth-connections-retry')).toBeVisible()
  await expect(page.getByTestId('auth-local-account-error')).toContainText('请求失败，请稍后重试。')
  await expect(page.getByTestId('auth-local-account-retry')).toBeVisible()
  await expect(page.getByTestId('auth-public-registration-error')).toContainText(
    '请求失败，请稍后重试。',
  )
  await expect(page.getByText(browserOriginal)).toHaveCount(0)
})
