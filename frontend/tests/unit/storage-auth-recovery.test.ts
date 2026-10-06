import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createApiClient,
  setApiBaseUrl,
  setLocalMode,
  setUnauthorizedHandler,
} from '@/api/client-core'
import { uploadProjectResourcesWithProgress } from '@/api/projects'
import { deleteExportArtifact, getStorageTask } from '@/api/storage'
import { changeSessionContext, StaleSessionError } from '@/api/session-context'
import { clearAuthTokens, setAuthTokens } from '@/api/token-storage'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
const rotated = { access_token: 'new-access', refresh_token: 'new-refresh' }
const fetchMock = vi.fn<typeof fetch>()
const unauthorized = vi.fn()
class RecoveryXHR extends EventTarget {
  static instances: RecoveryXHR[] = []
  upload = new EventTarget()
  headers: Record<string, string> = {}
  status = 0
  responseText = ''
  body?: FormData
  constructor() {
    super()
    RecoveryXHR.instances.push(this)
  }
  open() {}
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value
  }
  send(body: FormData) {
    this.body = body
  }
  abort() {
    this.dispatchEvent(new Event('abort'))
  }
  respond(status: number) {
    this.status = status
    this.responseText = JSON.stringify({ title: 'private provider detail' })
    this.dispatchEvent(new Event('load'))
  }
}
const upload = (signal?: AbortSignal) =>
  uploadProjectResourcesWithProgress(1, [new File(['original bytes'], 'a.txt')], ['dir/a.txt'], {
    idempotencyKey: 'original-key',
    signal,
  })
beforeEach(() => {
  RecoveryXHR.instances = []
  fetchMock.mockReset()
  unauthorized.mockClear()
  vi.stubGlobal('fetch', fetchMock)
  vi.stubGlobal('XMLHttpRequest', RecoveryXHR)
  setApiBaseUrl('https://recovery.example/api/v1')
  changeSessionContext('https://recovery.example/api/v1', 1, true)
  setAuthTokens({ access_token: 'old-access', refresh_token: 'old-refresh' })
  setLocalMode(false)
  setUnauthorizedHandler(unauthorized)
})
afterEach(() => {
  setUnauthorizedHandler(null)
  clearAuthTokens()
  vi.unstubAllGlobals()
})

describe('shared fetch and XHR authentication recovery', () => {
  it.each([400, 401])(
    'invalidates an XHR session when token rotation is rejected with %s',
    async (status) => {
      fetchMock.mockResolvedValue(json({ title: 'secret refresh problem' }, status))
      const pending = upload()
      const rejected = expect(pending).rejects.toMatchObject({ status, problem: undefined })
      RecoveryXHR.instances[0]!.respond(401)
      await rejected
      expect(unauthorized).toHaveBeenCalledTimes(1)
      expect(RecoveryXHR.instances).toHaveLength(1)
      expect(fetchMock).toHaveBeenCalledTimes(1)
    },
  )
  it('replays once with identical content and invalidates after a second 401', async () => {
    fetchMock.mockResolvedValue(json(rotated))
    const pending = upload()
    const rejected = expect(pending).rejects.toMatchObject({ status: 401 })
    const first = RecoveryXHR.instances[0]!
    first.respond(401)
    await vi.waitFor(() => expect(RecoveryXHR.instances).toHaveLength(2))
    const second = RecoveryXHR.instances[1]!
    expect(second.headers['Idempotency-Key']).toBe(first.headers['Idempotency-Key'])
    expect(second.headers.Authorization).toBe('Bearer new-access')
    expect(second.body!.getAll('files')).toEqual(first.body!.getAll('files'))
    expect(second.body!.getAll('paths')).toEqual(first.body!.getAll('paths'))
    second.respond(401)
    await rejected
    expect(RecoveryXHR.instances).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
  it('invalidates a local 401 without rotating or adding a bearer token', async () => {
    setLocalMode(true)
    const pending = upload()
    const rejected = expect(pending).rejects.toMatchObject({ status: 401 })
    expect(RecoveryXHR.instances[0]!.headers.Authorization).toBeUndefined()
    RecoveryXHR.instances[0]!.respond(401)
    await rejected
    expect(fetchMock).not.toHaveBeenCalled()
    expect(unauthorized).toHaveBeenCalledTimes(1)
    expect(RecoveryXHR.instances).toHaveLength(1)
  })
  it('keeps the session on a temporary refresh failure and redacts its Problem', async () => {
    fetchMock.mockResolvedValue(
      json({ title: 'provider secret', detail: 'private signed URL' }, 503),
    )
    const pending = upload()
    const rejected = expect(pending).rejects.toMatchObject({
      message: 'storageErrors.unavailable',
      status: 503,
      problem: undefined,
    })
    RecoveryXHR.instances[0]!.respond(401)
    await rejected
    expect(unauthorized).not.toHaveBeenCalled()
    expect(RecoveryXHR.instances).toHaveLength(1)
  })
  it('deduplicates invalidation across a concurrent fetch and XHR sharing one rotation', async () => {
    let finishRefresh!: (value: Response) => void
    fetchMock.mockImplementation(async (input) =>
      typeof input === 'string'
        ? new Promise<Response>((resolve) => {
            finishRefresh = resolve
          })
        : json({}, 401),
    )
    const client = createApiClient({ baseUrl: 'https://recovery.example/api/v1' })
    const reading = client.GET('/users/me')
    const readingRejected = expect(reading).rejects.toMatchObject({ status: 401 })
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const uploading = upload()
    const uploadRejected = expect(uploading).rejects.toMatchObject({ status: 401 })
    RecoveryXHR.instances[0]!.respond(401)
    await Promise.resolve()
    finishRefresh(json({}, 401))
    await Promise.all([readingRejected, uploadRejected])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
  it.each(['abort', 'account'] as const)(
    'does not replay after %s changes while refresh is pending',
    async (change) => {
      let finishRefresh!: (value: Response) => void
      fetchMock.mockReturnValue(
        new Promise<Response>((resolve) => {
          finishRefresh = resolve
        }),
      )
      const controller = new AbortController()
      const pending = upload(controller.signal)
      const rejected =
        change === 'account'
          ? expect(pending).rejects.toBeInstanceOf(StaleSessionError)
          : expect(pending).rejects.toMatchObject({ name: 'AbortError' })
      RecoveryXHR.instances[0]!.respond(401)
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
      if (change === 'account') changeSessionContext('https://recovery.example/api/v1', 2, true)
      else controller.abort()
      finishRefresh(json(rotated))
      await rejected
      expect(RecoveryXHR.instances).toHaveLength(1)
      expect(unauthorized).not.toHaveBeenCalled()
    },
  )
  it.each(['read', 'empty'] as const)(
    'sanitizes middleware-thrown ApiError for storage %s responses',
    async (kind) => {
      fetchMock.mockImplementation(async (input) =>
        typeof input === 'string'
          ? json({ title: 'refresh private endpoint', detail: 'secret' }, 503)
          : json({}, 401),
      )
      const request = kind === 'read' ? getStorageTask(1, 2) : deleteExportArtifact(1, 2)
      await expect(request).rejects.toMatchObject({
        message: 'storageErrors.unavailable',
        status: 503,
        problem: undefined,
      })
      expect(unauthorized).not.toHaveBeenCalled()
    },
  )
})
