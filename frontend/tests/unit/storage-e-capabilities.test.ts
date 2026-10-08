import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  setApiBaseUrl,
  setLocalMode,
  setUnauthorizedHandler,
  type ApiSchemas,
} from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { clearAuthTokens, setAuthTokens } from '@/api/token-storage'
import {
  getStorageCapabilities,
  getStorageDiagnostics,
  getStorageTask,
  downloadExportArtifact,
  setStoragePolicy,
} from '@/api/storage'
import { uploadProjectResources, uploadProjectResourcesWithProgress } from '@/api/projects'
import { ApiError } from '@/api/utils'
import {
  storageAccessDenied,
  storageAdmissionBlocked,
  storageErrorMessage,
  storageNeedsRefresh,
  storageRequestError,
  storageResultUnknown,
  storageTaskErrorMessage,
  storageTransportFailure,
} from '@/api/storage-errors'
import { organizationRoles } from '@/utils/organization-scope'
import {
  hasConnectionManagementActions,
  hasSpaceManagementActions,
  hasStoragePolicyCapabilities,
  isStorageActionAvailability,
  isStorageCapabilities,
  isStorageRuntime,
  storageConnectionActions,
  storageManagementAllowed,
} from '@/utils/storage-availability'
import { invalidateStorageSnapshots, subscribeStorageRefresh } from '@/utils/storage-snapshots'
import {
  connectionActions,
  policyCapabilities,
  storageAction,
  storageCapabilities,
  storageRuntime,
} from '../storage-fixtures'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const requests: Request[] = []
const fetchMock = vi.fn<typeof fetch>()
const unauthorized = vi.fn()
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
const cleanup: Array<() => void> = []

beforeEach(() => {
  requests.length = 0
  fetchMock.mockReset().mockImplementation(async (request) => {
    requests.push(request as Request)
    return response(storageCapabilities())
  })
  vi.stubGlobal('fetch', fetchMock)
  setApiBaseUrl('https://storage.example/api/v1')
  changeSessionContext('https://storage.example/api/v1', 1, true)
  setLocalMode(false)
  unauthorized.mockClear()
  setUnauthorizedHandler(unauthorized)
  setAuthTokens({ access_token: 'access', refresh_token: 'refresh' })
})
afterEach(() => {
  cleanup.splice(0).forEach((dispose) => dispose())
  vi.useRealTimers()
  vi.unstubAllGlobals()
  clearAuthTokens()
  setUnauthorizedHandler(null)
})

describe('E-T01 capabilities request ownership', () => {
  it('requests personal capabilities with exactly scope=user, including an empty account', async () => {
    await getStorageCapabilities({ kind: 'user' })
    expect(requests).toHaveLength(1)
    const url = new URL(requests[0]!.url)
    expect(url.pathname).toBe('/api/v1/storage/capabilities')
    expect([...url.searchParams.entries()]).toEqual([['scope', 'user']])
    expect(requests[0]!.cache).toBe('no-store')
  })
  it.each(['owner', 'admin'] as const)('requests an organization only as its %s', async (role) => {
    organizationRoles.value = { 7: role }
    await getStorageCapabilities({ kind: 'org', id: 7 })
    expect([...new URL(requests[0]!.url).searchParams.entries()]).toEqual([
      ['scope', 'org'],
      ['organization_id', '7'],
    ])
  })
  it.each(['member', undefined] as const)('rejects organization role %s before HTTP', (role) => {
    organizationRoles.value = role ? { 7: role } : {}
    expect(() => getStorageCapabilities({ kind: 'org', id: 7 })).toThrow()
    expect(requests).toHaveLength(0)
  })
  it.each([0, -1, 1.5, NaN, Number.MAX_SAFE_INTEGER + 1])(
    'rejects unsafe organization ID %s before HTTP',
    (id) => {
      organizationRoles.value = { [id]: 'owner' }
      expect(() => getStorageCapabilities({ kind: 'org', id })).toThrow()
      expect(requests).toHaveLength(0)
    },
  )
  it('does not accept a site capability endpoint at runtime', () => {
    expect(() => getStorageCapabilities({ kind: 'site' } as never)).toThrow()
    expect(requests).toHaveLength(0)
  })
  it('sends only the five policy request fields even when passed a response', async () => {
    const policy: ApiSchemas['StoragePolicy'] = {
      ...policyCapabilities(),
      mode: 'both',
      default_choice: 'user',
      generation: 7,
      logical_limit_bytes: 1000,
      default_space_capacity_bytes: null,
      configuration_needs_update: true,
    }
    await setStoragePolicy(policy)
    expect(await requests[0]!.json()).toEqual({
      mode: 'both',
      default_choice: 'user',
      generation: 7,
      logical_limit_bytes: 1000,
      default_space_capacity_bytes: null,
    })
  })
})

describe('E-T02/E-T10 complete capability contracts', () => {
  it('accepts a complete disabled deployment without claiming its actions are writable', () => {
    const snapshot = storageCapabilities()
    snapshot.runtime = storageRuntime(false)
    snapshot.management_actions.create_connection = storageAction(false)
    expect(isStorageCapabilities(snapshot)).toBe(true)
    expect(storageManagementAllowed(snapshot.management_actions.create_connection, true)).toBe(
      false,
    )
    expect(storageManagementAllowed(storageAction(), false)).toBe(false)
  })
  it.each([
    undefined,
    {},
    { deployment_enabled: false },
    { deployment_enabled: true, maintenance: 'false' },
  ])('rejects incomplete runtime %j', (runtime) => {
    expect(isStorageRuntime(runtime)).toBe(false)
    expect(isStorageCapabilities({ ...storageCapabilities(), runtime })).toBe(false)
  })
  it.each([
    undefined,
    {},
    { allowed: true },
    { allowed: true, reason_codes: ['storage_maintenance'] },
    { allowed: false, reason_codes: [] },
    { allowed: false, reason_codes: [null] },
  ])('rejects incomplete action %j', (action) => {
    expect(isStorageActionAvailability(action)).toBe(false)
    expect(storageManagementAllowed(action as never, true)).toBe(false)
  })
  it('keeps an unknown reason refused and gives it a safe general message', () => {
    const action = { allowed: false, reason_codes: ['future_reason'] }
    expect(isStorageActionAvailability(action)).toBe(true)
    expect(storageManagementAllowed(action as never, true)).toBe(false)
    expect(storageTaskErrorMessage('future_reason')).toBe('storageErrors.requestFailed')
  })
  it.each(storageConnectionActions)(
    'requires the %s action instead of inferring it from another action',
    (action) => {
      const actions: Partial<ReturnType<typeof connectionActions>> = connectionActions()
      delete actions[action]
      expect(hasConnectionManagementActions({ management_actions: actions })).toBe(false)
      expect(hasConnectionManagementActions({ management_actions: connectionActions() })).toBe(true)
    },
  )
  it('requires space status and policy restrictions independently', () => {
    expect(hasSpaceManagementActions({ management_actions: {} })).toBe(false)
    expect(
      hasSpaceManagementActions({ management_actions: { set_status: storageAction(false) } }),
    ).toBe(true)
    const complete = policyCapabilities()
    expect(hasStoragePolicyCapabilities(complete)).toBe(true)
    for (const field of ['runtime', 'allowed_policy_modes', 'policy_restriction_codes']) {
      expect(hasStoragePolicyCapabilities({ ...complete, [field]: undefined })).toBe(false)
    }
  })
  const modes = [
    ['site_only', 'site'],
    ['both', 'site'],
    ['both', 'user'],
    ['user_required', 'user'],
  ] as const
  const matrix = modes.flatMap(([mode, default_choice]) =>
    [true, false].flatMap((deployment_enabled) =>
      [true, false].map((maintenance) => ({
        mode,
        default_choice,
        deployment_enabled,
        maintenance,
      })),
    ),
  )
  it.each(matrix)(
    'validates saved $mode/$default_choice enabled=$deployment_enabled maintenance=$maintenance without rewriting policy',
    (entry) => {
      const policy = {
        ...entry,
        runtime: storageRuntime(entry.deployment_enabled, entry.maintenance),
        allowed_policy_modes: entry.deployment_enabled
          ? ['site_only', 'both', 'user_required']
          : ['site_only'],
        policy_restriction_codes:
          !entry.deployment_enabled && entry.mode !== 'site_only'
            ? ['storage_deployment_disabled']
            : [],
      }
      const original = structuredClone(policy)
      expect(hasStoragePolicyCapabilities(policy)).toBe(true)
      expect(policy).toEqual(original)
    },
  )
})

describe('E-T08 machine-code priority across transports', () => {
  it.each(['storage_deployment_disabled', 'byos_disabled'])(
    'classifies %s as a definite refusal for every legacy status',
    (code) => {
      for (const status of [400, 401, 403, 404, 409, 500, 503, 504]) {
        const error = storageRequestError(
          { status },
          { error_code: code, detail: 'private provider location', task_id: 17 },
        )
        expect(storageErrorMessage(error)).toBe('storageErrors.deploymentDisabled')
        expect(storageAdmissionBlocked(error)).toBe(true)
        expect(storageResultUnknown(error)).toBe(false)
        expect(storageAccessDenied(error)).toBe(false)
        expect(storageNeedsRefresh(error)).toBe(true)
        expect(error.task_id).toBe(17)
        expect(JSON.stringify(error)).not.toContain('private provider location')
      }
    },
  )
  it('retains policy data on 403 while recognizing real forbidden/not-found responses', () => {
    expect(
      storageAccessDenied(
        storageRequestError({ status: 403 }, { error_code: 'storage_policy_violation' }),
      ),
    ).toBe(false)
    expect(
      storageAccessDenied(storageRequestError({ status: 403 }, { error_code: 'forbidden' })),
    ).toBe(true)
    expect(storageAccessDenied(storageRequestError({ status: 404 }))).toBe(true)
  })
  it('normalizes generic API errors before exposing provider details', () => {
    const error = new ApiError('private provider location', 503, {
      title: 'private',
      error_code: 'byos_disabled',
    })
    expect(storageErrorMessage(error)).toBe('storageErrors.deploymentDisabled')
    const normalized = storageTransportFailure(error)
    expect(storageAdmissionBlocked(normalized)).toBe(true)
    expect(storageResultUnknown(normalized)).toBe(false)
  })
  it('recognizes policy refusal wrapped in ApiError.problem before any UI access-denied decision', () => {
    const error = new ApiError('provider text', 403, {
      title: 'private',
      error_code: 'storage_policy_violation',
    })
    expect(storageAdmissionBlocked(error)).toBe(true)
    expect(storageAccessDenied(error)).toBe(false)
    expect(storageNeedsRefresh(error)).toBe(true)
    expect(storageResultUnknown(error)).toBe(false)
    expect(storageErrorMessage(error)).toBe('storageErrors.policyViolation')
  })
  it.each(
    ['storage_deployment_disabled', 'byos_disabled'].flatMap((code) =>
      [401, 409, 503].map((status) => ({ code, status })),
    ),
  )(
    'preserves $code with status $status through task, diagnostics, download and batch Fetch failures without retry',
    async ({ code, status }) => {
      fetchMock.mockImplementation(async (request) => {
        requests.push(request as Request)
        return response({ error_code: code, task_id: 17, detail: 'never expose me' }, status)
      })
      const calls = [
        () => getStorageTask(1, 17),
        () => getStorageDiagnostics(),
        () => downloadExportArtifact(1, 2),
        () =>
          uploadProjectResources(1, [new File(['text'], 'a.txt')], undefined, undefined, {
            idempotencyKey: 'batch-key',
          }),
      ]
      for (const call of calls) {
        await expect(call()).rejects.toMatchObject({
          message: 'storageErrors.deploymentDisabled',
          error_code: code,
        })
      }
      expect(requests).toHaveLength(calls.length)
      expect(unauthorized).not.toHaveBeenCalled()
    },
  )
  it.each(
    ['storage_deployment_disabled', 'byos_disabled'].flatMap((code) =>
      [401, 409, 503].map((status) => ({ code, status })),
    ),
  )(
    'preserves $code with status $status through XHR without replacing the original upload key',
    async ({ code, status }) => {
      class XHR extends EventTarget {
        static instances: XHR[] = []
        upload = new EventTarget()
        status = status
        responseText = JSON.stringify({ error_code: code })
        headers: Record<string, string> = {}
        constructor() {
          super()
          XHR.instances.push(this)
        }
        open() {}
        setRequestHeader(key: string, value: string) {
          this.headers[key] = value
        }
        send() {}
        abort() {
          this.dispatchEvent(new Event('abort'))
        }
      }
      vi.stubGlobal('XMLHttpRequest', XHR)
      const promise = uploadProjectResourcesWithProgress(
        1,
        [new File(['text'], 'a.txt')],
        undefined,
        { idempotencyKey: 'original-key' },
      )
      XHR.instances[0]!.dispatchEvent(new Event('load'))
      await expect(promise).rejects.toMatchObject({
        message: 'storageErrors.deploymentDisabled',
        error_code: code,
      })
      expect(XHR.instances).toHaveLength(1)
      expect(XHR.instances[0]!.headers['Idempotency-Key']).toBe('original-key')
      expect(requests).toHaveLength(0)
      expect(unauthorized).not.toHaveBeenCalled()
    },
  )
})

describe('E-T09 snapshot invalidation ordering', () => {
  beforeEach(() => vi.useFakeTimers())
  it('invalidates matching ownership synchronously and coalesces repeated reads', async () => {
    const personal = {
      scope: () => ({ organizationId: null }),
      invalidate: vi.fn(),
      refresh: vi.fn(),
    }
    const org = { scope: () => ({ organizationId: 7 }), invalidate: vi.fn(), refresh: vi.fn() }
    cleanup.push(subscribeStorageRefresh(personal), subscribeStorageRefresh(org))
    invalidateStorageSnapshots({ organizationId: 7 })
    invalidateStorageSnapshots({ organizationId: 7 })
    expect(org.invalidate).toHaveBeenCalledTimes(1)
    expect(org.refresh).not.toHaveBeenCalled()
    expect(personal.invalidate).not.toHaveBeenCalled()
    await vi.runAllTimersAsync()
    expect(org.refresh).toHaveBeenCalledTimes(1)
    expect(personal.refresh).not.toHaveBeenCalled()
  })
  it('merges focus and visible events across adjacent browser event tasks', async () => {
    const windowEvents = new EventTarget()
    const documentEvents = Object.assign(new EventTarget(), { visibilityState: 'visible' })
    vi.stubGlobal('window', windowEvents)
    vi.stubGlobal('document', documentEvents)
    const consumer = { invalidate: vi.fn(), refresh: vi.fn() }
    cleanup.push(subscribeStorageRefresh(consumer))
    windowEvents.dispatchEvent(new Event('focus'))
    expect(consumer.invalidate).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    documentEvents.dispatchEvent(new Event('visibilitychange'))
    await vi.runAllTimersAsync()
    expect(consumer.invalidate).toHaveBeenCalledTimes(1)
    expect(consumer.refresh).toHaveBeenCalledTimes(1)
    documentEvents.visibilityState = 'hidden'
    documentEvents.dispatchEvent(new Event('visibilitychange'))
    await vi.runAllTimersAsync()
    expect(consumer.refresh).toHaveBeenCalledTimes(1)
  })
  it('drops scheduled reads after session replacement or unsubscription', async () => {
    const first = { invalidate: vi.fn(), refresh: vi.fn() }
    const second = { invalidate: vi.fn(), refresh: vi.fn() }
    cleanup.push(subscribeStorageRefresh(first))
    const unsubscribe = subscribeStorageRefresh(second)
    cleanup.push(unsubscribe)
    invalidateStorageSnapshots()
    unsubscribe()
    changeSessionContext('https://other.example/api/v1', 2, true)
    await vi.runAllTimersAsync()
    expect(first.refresh).not.toHaveBeenCalled()
    expect(second.refresh).not.toHaveBeenCalled()
  })
  it('does not turn a failed read into an automatic retry loop', async () => {
    const consumer = {
      invalidate: vi.fn(),
      refresh: vi.fn().mockRejectedValue(new Error('offline')),
    }
    cleanup.push(subscribeStorageRefresh(consumer))
    invalidateStorageSnapshots()
    await vi.runAllTimersAsync()
    expect(consumer.refresh).toHaveBeenCalledTimes(1)
  })
})
