import type { Page, Route } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'

export const testUser = (role = 'admin', id = 1): ApiSchemas['User'] => ({
  id,
  username: `tester-${id}`,
  email: `tester-${id}@example.com`,
  display_name: `Test ${id}`,
  role,
  active: true,
})

export const runtimeSample = (id = 'instance-alpha'): ApiSchemas['RuntimeSummary'] => ({
  instance_id: id,
  started_at: '2026-09-30T00:00:00Z',
  as_of: '2026-09-30T00:01:00.123456789Z',
  uptime_seconds: 60,
  scope: 'instance',
  runners: [
    {
      task_type: 'translation',
      state: 'running',
      recovered_total: 4,
      recovery_errors_total: 0,
      queue_capacity: 100,
      queue_waiting: 2,
      enqueue_waiters: 1,
      worker_capacity: 4,
      workers_alive: 4,
      workers_busy: 2,
    },
    {
      task_type: 'glossary_sync',
      state: 'degraded',
      recovered_total: 1,
      recovery_errors_total: 1,
      queue_capacity: null,
      queue_waiting: null,
      enqueue_waiters: null,
      worker_capacity: null,
      workers_alive: null,
      workers_busy: null,
    },
  ],
  limiters: null,
  external_requests: [
    {
      provider: 'openai',
      operation: 'generate',
      http_attempts_inflight: 1,
      http_attempts_total: 15,
      outcomes: [
        {
          outcome: 'success',
          http_attempts_finished_total: 11,
          http_attempt_duration_seconds_sum: 40.5,
          http_attempt_duration_seconds_count: 11,
        },
        {
          outcome: 'http_error',
          http_attempts_finished_total: 3,
          http_attempt_duration_seconds_sum: 4,
          http_attempt_duration_seconds_count: 3,
        },
      ],
    },
  ],
})

export const emptyCounts = { pending: 0, running: 0, paused: 0, recent_failed: 0 }
export const summaryFixture: ApiSchemas['OperationsSummaryResponse'] = {
  total: emptyCounts,
  by_type: { translation: emptyCounts, glossary_sync: emptyCounts },
  recent_failed_since: '2026-09-29T00:01:00.123456789Z',
  as_of: '2026-09-30T00:01:00.123456789Z',
}

export const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, json: body })

/** Stable API baseline; later page.route registrations override individual endpoints. */
export async function mockApp(
  page: Page,
  options: { role?: string; mode?: 'local' | 'server'; theme?: 'light' | 'dark' } = {},
) {
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem('linguaflow.api_base_url', '/api/v1')
      localStorage.setItem('linguaflow.access_token', 'test-access')
      localStorage.setItem('linguaflow.refresh_token', 'test-refresh')
      localStorage.setItem('linguaflow.theme', theme)
    },
    { theme: options.theme ?? 'light' },
  )
  await page.route('**/api/v1/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace('/api/v1', '')
    switch (path) {
      case '/ping':
        return json(route, { status: 'ok', service: 'Test service' })
      case '/mode':
        return json(route, { mode: options.mode ?? 'server' })
      case '/users/me':
        return json(route, testUser(options.role))
      case '/operations/summary':
        return json(route, summaryFixture)
      case '/admin/runtime/summary':
        return json(route, runtimeSample())
      case '/admin/stats':
        return json(route, {
          total_users: 2,
          active_users: 2,
          total_projects: 0,
          total_organizations: 0,
          total_jobs: 0,
          total_resources: 0,
        })
      default:
        return json(route, { items: [], total: 0 })
    }
  })
}
