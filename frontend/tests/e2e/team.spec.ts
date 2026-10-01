import { expect, test, type Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json, mockApp, testUser } from './fixtures'

type Role = ApiSchemas['Organization']['current_user_role']
async function mockTeam(page: Page, role: Role = 'owner') {
  await mockApp(page, { role: 'user' })
  const state = {
    org: {
      id: 7,
      name: 'team-seven',
      slug: 'team-seven',
      display_name: 'Team Seven',
      current_user_role: role,
    } as ApiSchemas['Organization'],
    members: [
      { id: 1, role, user: testUser('user', 1) },
      { id: 2, role: 'member', user: testUser('user', 2) },
    ] as ApiSchemas['OrganizationMember'][],
    leaveStatus: 204,
    removed: false,
    memberPayload: null as unknown,
  }
  await page.route('**/api/v1/orgs**', (route) => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/v1/orgs') return json(route, { items: state.removed ? [] : [state.org] })
    if (path === '/api/v1/orgs/7') return json(route, state.org)
    if (path === '/api/v1/orgs/7/members' && route.request().method() === 'POST') {
      state.memberPayload = route.request().postDataJSON()
      const added = {
        id: 3,
        role: 'member',
        user: { ...testUser('user', 3), username: 'exact-user' },
      }
      state.members.push(added as ApiSchemas['OrganizationMember'])
      return json(route, added, 201)
    }
    if (path === '/api/v1/orgs/7/members') return json(route, { items: state.members })
    if (path === '/api/v1/orgs/7/members/1' && route.request().method() === 'DELETE') {
      if (state.leaveStatus === 204) {
        state.removed = true
        return route.fulfill({ status: 204 })
      }
      return json(
        route,
        { title: '最后一位所有者不能退出组织', status: state.leaveStatus },
        state.leaveStatus,
      )
    }
    return json(route, { items: [], total: 0 })
  })
  return state
}

test('team does not automatically select the first organization', async ({ page }) => {
  await mockTeam(page)
  await page.goto('/settings/team')
  await expect(page.getByText('选择或创建组织以开始协作')).toBeVisible()
  await expect(page).not.toHaveURL(/org_id=/)
  await expect(page.getByRole('button', { name: '添加成员', exact: true })).toHaveCount(0)
})

test('members see shared resources and can leave, without management controls', async ({
  page,
}) => {
  await mockTeam(page, 'member')
  await page.goto('/settings/team?org_id=7')
  await expect(
    page.getByText('你可以查看并使用共享资源；仅组织管理员和所有者可以修改。'),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: '添加成员', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '组织资料', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '移除成员', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '退出组织', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'AI 后端', exact: true })).toBeVisible()
})

test('organization admins add an exact existing username as a member', async ({ page }) => {
  const state = await mockTeam(page, 'admin')
  await page.goto('/settings/team?org_id=7')
  await page.getByRole('textbox', { name: '精确用户名' }).fill('  exact-user  ')
  await page.getByRole('button', { name: '添加成员', exact: true }).click()
  await expect.poll(() => state.memberPayload).toEqual({ username: 'exact-user', role: 'member' })
  await expect(page.getByText('exact-user', { exact: true })).toBeVisible()
})

test('last-owner conflict preserves membership and displays the server reason', async ({
  page,
}) => {
  const state = await mockTeam(page, 'owner')
  state.leaveStatus = 409
  await page.goto('/settings/team?org_id=7')
  await page.getByRole('button', { name: '退出组织', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '退出组织', exact: true }).click()
  await expect(page.getByText('最后一位所有者不能退出组织')).toBeVisible()
  await expect(page).toHaveURL(/org_id=7/)
  await expect(page.getByRole('button', { name: '组织资料', exact: true })).toBeVisible()
  expect(state.removed).toBe(false)
})

test('successful leave clears organization details and URL selection', async ({ page }) => {
  const state = await mockTeam(page, 'member')
  await page.goto('/settings/team?org_id=7')
  await page.getByRole('button', { name: '退出组织', exact: true }).click()
  await page.getByRole('dialog').getByRole('button', { name: '退出组织', exact: true }).click()
  await expect(page).not.toHaveURL(/org_id=/)
  await expect(page.getByText('选择或创建组织以开始协作')).toBeVisible()
  expect(state.removed).toBe(true)
})

test('role downgrade refreshes available controls', async ({ page }) => {
  const state = await mockTeam(page, 'owner')
  let orgRequests = 0
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/api/v1/orgs') orgRequests++
  })
  await page.goto('/settings/team?org_id=7')
  await expect(page.getByRole('button', { name: '添加成员', exact: true })).toBeVisible()
  state.org = { ...state.org, current_user_role: 'member' }
  state.members = state.members.map((member) =>
    member.user.id === 1 ? { ...member, role: 'member' } : member,
  )
  await page.getByRole('button', { name: '刷新', exact: true }).click()
  await expect(page.getByRole('button', { name: '添加成员', exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '组织资料', exact: true })).toHaveCount(0)
  await page.clock.install()
  const requestsAfterDowngrade = orgRequests
  await page.clock.fastForward(20_000)
  expect(orgRequests).toBe(requestsAfterDowngrade)
})

const scopedResources = [
  { page: '/projects', endpoint: '/orgs/7/projects', query: false },
  { page: '/backends', endpoint: '/orgs/7/backends', query: false },
  { page: '/prompt-templates', endpoint: '/translation-prompt-templates', query: true },
  { page: '/bootstrap-prompt-templates', endpoint: '/bootstrap-prompt-templates', query: true },
  { page: '/prune-prompt-templates', endpoint: '/prune-prompt-templates', query: true },
  { page: '/execution-profiles', endpoint: '/execution-profiles', query: true },
  { page: '/execution-plan-templates', endpoint: '/execution-plan-templates', query: true },
] as const

for (const resource of scopedResources) {
  test(`${resource.page} loads resources in explicit organization scope`, async ({ page }) => {
    await mockTeam(page)
    const scopedRequest = page.waitForRequest((request) => {
      const url = new URL(request.url())
      return (
        url.pathname === `/api/v1${resource.endpoint}` &&
        (!resource.query || url.searchParams.get('org_id') === '7')
      )
    })
    await page.goto(`${resource.page}?org_id=7`)
    await scopedRequest
    await expect(page).toHaveURL(/org_id=7/)
    await expect(page.getByText('Team Seven', { exact: true }).first()).toBeVisible()
  })
}
