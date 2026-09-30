import { expect, test } from '@playwright/test'
import { json, mockApp, summaryFixture, testUser } from './fixtures'

const privateTask = (name: string) => ({
  task_id: '77',
  task_type: 'translation',
  project_id: 7,
  project_name: name,
  status: 'running',
  trigger_type: 'manual',
  created_at: '2026-09-30T00:00:00Z',
  updated_at: '2026-09-30T00:01:00Z',
  started_at: null,
  supported_actions: ['view', 'cancel'],
  progress: {
    total_resources: 1,
    completed_resources: 0,
    failed_resources: 0,
    progress_total: 100,
    progress_completed: 25,
    queue_position: null,
    queue_size: null,
  },
})

test('switching accounts clears the previous account task collection and refreshes its identity', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/operations?**', (route) => {
    const isNewUser = route.request().headers().authorization === 'Bearer second-access'
    const isTerminal = new URL(route.request().url()).searchParams.get('state') === 'terminal'
    return json(route, {
      items: isTerminal
        ? []
        : [privateTask(isNewUser ? 'Second account task' : 'First account private task')],
    })
  })
  await page.route('**/api/v1/auth/logout', (route) => route.fulfill({ status: 204 }))
  await page.route('**/api/v1/auth/login', (route) =>
    json(route, {
      user: testUser('user', 2),
      access_token: 'second-access',
      refresh_token: 'second-refresh',
      token_type: 'Bearer',
      expires_at: '2026-09-30T01:00:00Z',
      refresh_expires_at: '2026-10-30T01:00:00Z',
    }),
  )
  await page.goto('/settings/security')
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await expect(page.getByRole('button', { name: /First account private task/ })).toBeVisible()
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await page.getByRole('button', { name: '退出当前登录', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  await page.locator('input[autocomplete="username"]').fill('tester-2')
  await page.locator('input[autocomplete="current-password"]').fill('password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await expect(page.getByRole('button', { name: /Second account task/ })).toBeVisible()
  await expect(page.getByText('First account private task', { exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBe(
    'second-access',
  )
})

test('switching services clears credentials and private tasks before loading the new local identity', async ({
  page,
}) => {
  await mockApp(page)
  await page.route('**/api/v1/operations?**', (route) =>
    json(route, {
      items:
        new URL(route.request().url()).searchParams.get('state') === 'terminal'
          ? []
          : [privateTask('Previous service private task')],
    }),
  )
  const newHeaders: (string | undefined)[] = []
  await page.route('**/other-api/v1/**', (route) => {
    newHeaders.push(route.request().headers().authorization)
    const path = new URL(route.request().url()).pathname.replace('/other-api/v1', '')
    if (path === '/ping') return json(route, { status: 'ok', service: 'Second service' })
    if (path === '/mode') return json(route, { mode: 'local' })
    if (path === '/users/me') return json(route, testUser('admin', 2))
    if (path === '/operations/summary') return json(route, summaryFixture)
    return json(route, { items: [], total: 0 })
  })
  await page.goto('/settings/preferences')
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await expect(page.getByRole('button', { name: /Previous service private task/ })).toBeVisible()
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await page.getByRole('button', { name: 'Test 1', exact: true }).click()
  await page.getByText('切换服务器', { exact: true }).click()
  await expect(page).toHaveURL(/\/service$/)
  await page.locator('input[autocomplete="off"]').fill('/other-api/v1')
  await page.getByRole('button', { name: '连接', exact: true }).click()
  await expect(page.getByRole('heading', { name: '工作台', exact: true })).toBeVisible()
  await page.getByRole('button', { name: /^当前任务/ }).click()
  await expect(page.getByText('Previous service private task', { exact: true })).toHaveCount(0)
  expect(await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))).toBeNull()
  expect(newHeaders.every((value) => value === undefined)).toBe(true)
})
