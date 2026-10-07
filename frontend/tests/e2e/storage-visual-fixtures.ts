import type { Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client-core'
import { json, mockApp } from './fixtures.ts'
import {
  connectionActions,
  spaceActions,
  storageAction,
  storageCapabilities,
  policyCapabilities,
} from '../storage-fixtures.ts'

export const visualTime = new Date('2026-10-05T02:00:00Z')
const GiB = 1024 ** 3

export async function mockStorageVisual(
  page: Page,
  options: { theme?: 'light' | 'dark'; scenario?: string } = {},
) {
  await mockApp(page, { theme: options.theme })
  await page.clock.setFixedTime(visualTime)
  const scenario = options.scenario ?? 'populated'
  const requests: Array<{ path: string; method: string; body: unknown }> = []
  const makeConnection = (id: number) => ({
    id,
    scope: 'user',
    owner_id: 1,
    name:
      id === 1
        ? '翻译项目与源文件 · 亚太存储'
        : id === 2
          ? '历史归档和交付备份存储连接-LongUnbrokenArchiveConnectionName20261005'
          : '团队素材备用连接',
    driver: 's3',
    endpoint: 'https://storage-ap-southeast-1.example.invalid/translation-archive',
    region: 'ap-southeast-1',
    status: 'enabled',
    health:
      scenario === 'unknown'
        ? 'future_health'
        : scenario === 'missing-auth' || id === 3
          ? 'auth_required'
          : 'healthy',
    has_auth: scenario !== 'missing-auth' && id !== 3,
    management_generation: 8,
    auth_generation: 2,
    management_actions: connectionActions(),
  })
  const connections = [1, 2, 3].map(makeConnection)
  const spaces: ApiSchemas['StorageSpace'][] = Array.from({ length: 7 }, (_, index) => ({
    id: index + 11,
    connection_id: 1,
    name:
      index === 1
        ? '待审核译文和多语言交付文件-LongUnbrokenTranslationDeliverables20261005'
        : ['项目原始文件', '', '历史交付归档', '参考文档', '术语库快照', '对照资料', '历史文件'][
            index
          ]!,
    bucket: 'linguaflow-project-assets-ap-southeast-1',
    prefix: `translation-projects/release-2026/space-${index + 1}/`,
    status: index === 2 ? 'read_only' : 'active',
    verified: index !== 3,
    management_generation: 3,
    management_actions: spaceActions(),
    capacity_bytes:
      scenario === 'unlimited'
        ? null
        : scenario === 'over-capacity' && index === 0
          ? GiB
          : 10 * GiB,
    available_bytes:
      scenario === 'unlimited'
        ? null
        : scenario === 'over-capacity' && index === 0
          ? 0
          : 10 * GiB - (128 + 256 + 64) * 1024 ** 2 - 2 * GiB - index * 1000,
    reserved_bytes: scenario === 'unknown' && index === 0 ? undefined! : 128 * 1024 ** 2,
    candidate_bytes: 256 * 1024 ** 2,
    live_bytes: 2 * GiB + index * 1000,
    pending_delete_bytes: 64 * 1024 ** 2,
  }))
  const policy: ApiSchemas['StoragePolicy'] = {
    ...policyCapabilities(),
    mode: 'both',
    default_choice: 'site',
    generation: 6,
    logical_limit_bytes: scenario === 'unlimited' ? null : 100 * GiB,
    default_space_capacity_bytes: null,
  }
  if (scenario === 'maintenance') {
    policy.runtime.maintenance = true
    for (const connection of connections)
      for (const action of ['create_space', 'authorize_write', 'check_write'] as const)
        connection.management_actions[action] = storageAction(false, ['storage_maintenance'])
  }
  const checks = [
    {
      check_id: 10,
      connection_id: 1,
      mode: 'write',
      management_generation: 8,
      created_at: '2026-10-05T01:00:00Z',
      completed_at: '2026-10-05T01:00:01Z',
      status: 'completed',
      cleanup_status: 'blocked',
      accounted_bytes: 32,
      authorization_activated: false,
      results: [
        { space_id: 11, status: 'completed' },
        { space_id: 12, status: 'failed', error_code: 'storage_permission_denied' },
      ],
    },
  ]
  const disks: ApiSchemas['StorageDiskDiagnostic'][] =
    scenario === 'empty'
      ? []
      : [
          {
            roles: ['work', 'objects'],
            state:
              scenario === 'disk-unknown'
                ? 'unknown'
                : scenario === 'disk-low'
                  ? 'low'
                  : 'available',
            total_bytes: scenario === 'disk-unknown' ? null : 512 * GiB,
            available_bytes:
              scenario === 'disk-unknown' ? null : scenario === 'disk-low' ? 0 : 128 * GiB,
            minimum_free_bytes: scenario === 'disk-unknown' ? null : 5 * GiB,
            observed_at: '2026-10-05T01:59:00Z',
          },
        ]
  const state = {
    requests,
    connections,
    spaces,
    policy,
    disks: disks as typeof disks | undefined,
    failed: false,
    pending: false,
    quotaResponse: 'ok' as 'ok' | 'conflict' | 'lost',
    policyResponse: 'ok' as 'ok' | 'conflict' | 'lost',
  }
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace('/api/v1', '')
    if (!path.includes('/storage')) return route.fallback()
    const method = route.request().method()
    requests.push({ path, method, body: method === 'GET' ? null : route.request().postDataJSON() })
    if (scenario === 'loading' || state.pending)
      await new Promise((resolve) => setTimeout(resolve, 3000))
    if (scenario === 'error' || state.failed)
      return json(route, { title: '存储服务暂时不可用' }, 503)
    if (path === '/storage/capabilities') {
      if (scenario === 'permission') return json(route, {}, 403)
      if (scenario === 'unknown') return json(route, {}, 200)
      const capability = storageCapabilities()
      capability.runtime = policy.runtime
      if (scenario === 'maintenance')
        capability.management_actions.create_connection = storageAction(false, [
          'storage_maintenance',
        ])
      return json(route, capability)
    }
    if (path === '/admin/storage/policy') {
      if (method === 'PUT') {
        const body = route.request().postDataJSON()
        if (state.policyResponse === 'conflict' || body.generation !== state.policy.generation)
          return json(route, { error_code: 'storage_generation_conflict' }, 409)
        state.policy = { ...state.policy, ...body, generation: state.policy.generation + 1 }
        if (state.policyResponse === 'lost') return route.abort('connectionfailed')
      }
      return json(route, state.policy)
    }
    if (path === '/admin/storage/diagnostics')
      return json(route, {
        disks: state.disks,
        spaces:
          scenario === 'empty'
            ? []
            : spaces.slice(0, 3).map((space, index) => ({
                id: space.id,
                reserved_bytes: space.reserved_bytes,
                candidate_bytes: space.candidate_bytes,
                live_bytes: space.live_bytes,
                pending_delete_bytes: space.pending_delete_bytes,
                unchecked_objects: index === 0 ? 5 : 0,
                missing_objects: index === 1 ? 1 : 0,
                corrupt_objects: 0,
                last_checked_at: '2026-10-04T23:00:00Z',
              })),
        temporary_bytes: 128 * 1024 ** 2,
        oldest_intent_at: '2026-10-04T23:00:00Z',
        recovery_backlog: 2,
        blocked_cleanup_by_code: { storage_permission_denied: 2 },
        migrations_by_phase: { copying: 1 },
        latest_backup: { id: 42, status: 'complete', created_at: '2026-10-04T22:00:00Z' },
      })
    if (path.endsWith('/connections'))
      return json(route, {
        items:
          scenario === 'empty'
            ? []
            : path.startsWith('/admin')
              ? [
                  {
                    ...connections[0],
                    scope: 'site',
                    owner_id: 0,
                    name: '站点托管存储',
                    driver: 'local',
                  },
                ]
              : connections,
      })
    const connectionId = Number(path.match(/connections\/(\d+)/)?.[1] ?? 1)
    if (path.endsWith('/quota') && method === 'PUT') {
      const body = route.request().postDataJSON()
      const space = spaces.find((item) => item.id === Number(path.match(/spaces\/(\d+)/)?.[1]))
      if (!space) return json(route, {}, 404)
      if (
        state.quotaResponse === 'conflict' ||
        body.expected_generation !== space.management_generation
      )
        return json(route, { error_code: 'storage_generation_conflict' }, 409)
      space.capacity_bytes = body.capacity_bytes
      space.available_bytes =
        body.capacity_bytes === null
          ? null
          : Math.max(
              0,
              body.capacity_bytes -
                space.reserved_bytes -
                space.candidate_bytes -
                space.live_bytes -
                space.pending_delete_bytes,
            )
      space.management_generation++
      if (state.quotaResponse === 'lost') return route.abort('connectionfailed')
      return json(route, space)
    }
    if (path.endsWith('/spaces')) {
      if (method === 'POST') {
        const body = route.request().postDataJSON()
        const space = {
          ...spaces[0]!,
          ...body,
          id: 100 + spaces.length,
          connection_id: connectionId,
          management_generation: 0,
          reserved_bytes: 0,
          candidate_bytes: 0,
          live_bytes: 0,
          pending_delete_bytes: 0,
          available_bytes: body.capacity_bytes,
        }
        spaces.push(space)
        return json(route, space, 201)
      }
      return json(route, {
        items:
          scenario === 'empty'
            ? []
            : connectionId === 1
              ? spaces
              : connectionId === 2
                ? [{ ...spaces[0], id: 21, connection_id: 2, name: '发布版本归档' }]
                : [],
      })
    }
    if (path.endsWith('/checks')) return json(route, { items: checks })
    if (path.endsWith('/check')) return json(route, { ...connections[0], check_id: 10 })
    return json(route, { title: 'Unexpected visual fixture route' }, 404)
  })
  return state
}
