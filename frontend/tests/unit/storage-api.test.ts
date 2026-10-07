import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setApiBaseUrl, setLocalMode } from '@/api/client-core'
import { changeSessionContext, StaleSessionError } from '@/api/session-context'
import { clearAuthTokens, setAuthTokens } from '@/api/token-storage'
import {
  previewSourceUpdate,
  receiveStorageContent,
  commitSourceUpdate,
  getStorageTask,
  downloadExportArtifact,
  authorizeStorage,
  setStorageConnectionState,
  createExportArtifact,
  listStorageConnections,
  deleteExportArtifact,
} from '@/api/storage'
import {
  createProject,
  createOrgProject,
  deleteProject,
  downloadResourceResult,
  uploadProjectResources,
  uploadProjectResourcesWithProgress,
} from '@/api/projects'
import { storageTaskErrorMessage, storageResultUnknown } from '@/api/storage-errors'
import { getStorageContractGate, requireStorageGeneration } from '@/utils/storage-contract'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const task = {
  id: 9,
  operation_id: 'op',
  kind: 'source_update',
  status: 'running',
  phase: 'prepared',
  cleanup_status: 'cleanup_pending',
  allowed_actions: [],
}
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
const fetchMock = vi.fn<typeof fetch>()
const requests: Request[] = []
beforeEach(() => {
  requests.length = 0
  fetchMock.mockReset()
  fetchMock.mockImplementation(async (request) => {
    requests.push(request as Request)
    return json(task)
  })
  vi.stubGlobal('fetch', fetchMock)
  setApiBaseUrl('https://storage.example/api/v1')
  changeSessionContext('https://storage.example/api/v1', 1, true)
  setLocalMode(false)
  setAuthTokens({ access_token: 'test-access', refresh_token: 'test-refresh' })
})
afterEach(() => {
  vi.unstubAllGlobals()
  clearAuthTokens()
})

describe('storage transport boundaries', () => {
  it.each([
    ['storage_disk_insufficient', 507, 'diskInsufficient'],
    ['storage_disk_probe_failed', 503, 'diskProbeFailed'],
  ] as const)(
    'retains Fetch disk failure %s and refuses to download a Problem',
    async (error_code, status, key) => {
      fetchMock.mockImplementation(async (request) => {
        requests.push(request as Request)
        return json(
          { error_code, task_id: 9, operation_id: 'original-op', detail: '/private/disk' },
          status,
        )
      })
      await expect(downloadExportArtifact(1, 9)).rejects.toMatchObject({
        message: `storageErrors.${key}`,
        task_id: 9,
        operation_id: 'original-op',
      })
      expect(requests).toHaveLength(1)
      const result = uploadProjectResources(
        1,
        [new File(['data'], 'a.txt')],
        ['a.txt'],
        undefined,
        { idempotencyKey: 'original-batch' },
      )
      await expect(result).rejects.toMatchObject({
        error_code,
        task_id: 9,
        operation_id: 'original-op',
      })
      expect(requests).toHaveLength(2)
    },
  )
  it('keeps per-item disk errors and original batch operation identity', async () => {
    fetchMock.mockResolvedValueOnce(
      json({
        operation_id: 'batch-op',
        items: [
          {
            path: 'a.txt',
            action: 'failed',
            error_code: 'storage_disk_insufficient',
            error: '/private/disk',
          },
          {
            path: 'b.txt',
            action: 'failed',
            error_code: 'storage_disk_probe_failed',
            error: '/private/disk',
          },
        ],
      }),
    )
    const result = await uploadProjectResources(
      1,
      [new File(['a'], 'a.txt'), new File(['b'], 'b.txt')],
      ['a.txt', 'b.txt'],
      undefined,
      { idempotencyKey: 'batch-original' },
    )
    expect(result.operation_id).toBe('batch-op')
    expect(result.items.map((item) => item.error)).toEqual([
      'storageErrors.diskInsufficient',
      'storageErrors.diskProbeFailed',
    ])
    expect(JSON.stringify(result)).not.toContain('/private/disk')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each(['create', 'org-create', 'delete'] as const)(
    'keeps %s policy refusal safe without replay',
    async (operation) => {
      fetchMock.mockImplementation(async (request) => {
        requests.push(request as Request)
        return json(
          {
            title: 'provider response',
            detail: 'private provider location',
            error_code: 'storage_policy_violation',
          },
          403,
        )
      })
      const payload = {
        name: 'Policy test',
        source_lang: 'en',
        target_lang: 'zh',
        storage_space_id: 1,
      }
      const action =
        operation === 'delete'
          ? deleteProject(1)
          : operation === 'org-create'
            ? createOrgProject(2, payload)
            : createProject(payload)
      await expect(action).rejects.toMatchObject({
        status: 403,
        error_code: 'storage_policy_violation',
        message: 'storageErrors.policyViolation',
      })
      expect(requests).toHaveLength(1)
    },
  )
  it('sends exact binary bytes without JSON conversion or a forbidden length header', async () => {
    const bytes = new Uint8Array([0, 34, 10, 128, 255])
    await previewSourceUpdate(1, 2, new Blob([bytes]), 'candidate-key')
    await receiveStorageContent(1, 9, new Blob([bytes]))
    for (const request of requests) {
      expect(new Uint8Array(await request.arrayBuffer())).toEqual(bytes)
      expect(request.headers.get('Content-Type')).toBe('application/octet-stream')
      expect(request.headers.has('Content-Length')).toBe(false)
    }
    expect(requests[0]!.headers.get('Idempotency-Key')).toBe('candidate-key')
  })
  it('uses the exact preview identity and accepts generation zero without inventing a default', async () => {
    await commitSourceUpdate(1, 2, {
      task_id: 9,
      expected_source_generation: 0,
      expected_translation_generation: 17,
    })
    expect(await requests[0]!.json()).toEqual({
      task_id: 9,
      expected_source_generation: 0,
      expected_translation_generation: 17,
    })
    expect(() => requireStorageGeneration(undefined)).toThrow()
    expect(() =>
      commitSourceUpdate(1, 2, {
        task_id: 9,
        expected_source_generation: -1,
        expected_translation_generation: 17,
      }),
    ).toThrow()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
  it.each([0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1, NaN])(
    'rejects unsafe task id %s before sending',
    (id) => {
      expect(() => getStorageTask(1, id)).toThrow()
      expect(fetchMock).not.toHaveBeenCalled()
    },
  )
  it('never sends a space-only state to a connection or auth generation instead of management generation', async () => {
    expect(() =>
      setStorageConnectionState(4, { status: 'read_only', expected_generation: 2 }),
    ).toThrow()
    await authorizeStorage(4, {
      access_key_id: 'key',
      secret_access_key: 'secret',
      write_check: false,
      expected_management_generation: 12,
    })
    expect(await requests[0]!.json()).toEqual({
      access_key_id: 'key',
      secret_access_key: 'secret',
      write_check: false,
      expected_management_generation: 12,
    })
  })
  it('isolates personal, organization and site list routes', async () => {
    await listStorageConnections({ kind: 'user' })
    await listStorageConnections({ kind: 'org', id: 7 })
    await listStorageConnections({ kind: 'site' })
    expect(requests.map((r) => new URL(r.url).pathname)).toEqual([
      '/api/v1/storage/connections',
      '/api/v1/orgs/7/storage/connections',
      '/api/v1/admin/storage/connections',
    ])
  })
  it('keeps idempotency in the header without inventing export request fields', async () => {
    await createExportArtifact(1, 2, 'snapshot-key')
    expect(requests[0]!.headers.get('Idempotency-Key')).toBe('snapshot-key')
    expect(await requests[0]!.text()).toBe('')
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(deleteExportArtifact(1, 5)).resolves.toBeUndefined()
  })
  it('redacts provider failures and never turns them into file downloads', async () => {
    fetchMock.mockImplementation(async () =>
      json({ title: 'provider-key-secret', detail: '/private/objects/signed-url' }, 503),
    )
    await expect(downloadExportArtifact(1, 9)).rejects.toMatchObject({
      message: 'storageErrors.unavailable',
      status: 503,
      problem: undefined,
    })
    await expect(downloadResourceResult(1, 2)).rejects.toMatchObject({
      message: 'storageErrors.unavailable',
      isDownloadTranslatedError: true,
    })
    fetchMock.mockResolvedValue(json({ title: 'unexpected success Problem' }))
    await expect(downloadExportArtifact(1, 9)).rejects.toThrow('storageErrors.invalidDownload')
  })
  it('downloads successful bytes and tolerates malformed filename encoding', async () => {
    fetchMock.mockResolvedValue(
      new Response('complete-snapshot', {
        headers: {
          'Content-Type': 'application/octet-stream',
          'Content-Disposition': "attachment; filename*=UTF-8''%xx",
        },
      }),
    )
    const result = await downloadExportArtifact(1, 9)
    expect(await result.blob.text()).toBe('complete-snapshot')
    expect(result.filename).toBeUndefined()
  })
  it('drops a response received after changing the active service', async () => {
    let release!: (response: Response) => void
    fetchMock.mockReturnValueOnce(
      new Promise<Response>((resolve) => {
        release = resolve
      }),
    )
    const pending = getStorageTask(1, 9)
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    changeSessionContext('https://another.example/api/v1', 1)
    release(json(task))
    await expect(pending).rejects.toBeInstanceOf(StaleSessionError)
  })
  it('enables implemented contracts while retaining safe error fallbacks', () => {
    for (const capability of [
      'sourceUpdate',
      'targetDiscovery',
      'repair',
      'migration',
      'exportMutation',
    ] as const)
      expect(getStorageContractGate(capability).available).toBe(true)
    expect(storageTaskErrorMessage('provider-private-key')).toBe('storageErrors.requestFailed')
  })
  it('keeps multipart files ordered and redacts item error text', async () => {
    fetchMock.mockImplementationOnce(async (request) => {
      requests.push(request as Request)
      return json({
        items: [
          { path: 'a/a.txt', action: 'created' },
          { path: 'b/b.txt', action: 'failed', error: 'private endpoint and key' },
        ],
      })
    })
    const result = await uploadProjectResources(
      1,
      [new File(['a'], 'a.txt'), new File(['b'], 'b.txt')],
      ['a/a.txt', 'b/b.txt'],
      undefined,
      { idempotencyKey: 'batch-fixed' },
    )
    expect(requests[0]!.headers.get('Idempotency-Key')).toBe('batch-fixed')
    const body = await requests[0]!.formData()
    expect(body.getAll('files').map((file) => (file as File).name)).toEqual(['a.txt', 'b.txt'])
    expect(result.items[1]!.error).toBe('storageErrors.requestFailed')
  })
})

class FakeXHR extends EventTarget {
  static instances: FakeXHR[] = []
  upload = new EventTarget()
  headers: Record<string, string> = {}
  status = 0
  responseText = ''
  body: FormData | undefined
  aborted = false
  url = ''
  constructor() {
    super()
    FakeXHR.instances.push(this)
  }
  open(_method: string, url: string) {
    this.url = url
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value
  }
  send(body: FormData) {
    this.body = body
  }
  abort() {
    this.aborted = true
    this.dispatchEvent(new Event('abort'))
  }
  respond(status: number, body: unknown) {
    this.status = status
    this.responseText = JSON.stringify(body)
    this.dispatchEvent(new Event('load'))
  }
}
describe('XHR session and ordered batch recovery', () => {
  beforeEach(() => {
    FakeXHR.instances = []
    vi.stubGlobal('XMLHttpRequest', FakeXHR)
  })
  it.each([
    ['storage_disk_insufficient', 507],
    ['storage_disk_probe_failed', 503],
  ] as const)(
    'does not replay XHR disk failure %s or discard its task identity',
    async (error_code, status) => {
      const pending = uploadProjectResourcesWithProgress(
        1,
        [new File(['data'], 'a.txt')],
        ['a.txt'],
        { idempotencyKey: 'disk-original' },
      )
      FakeXHR.instances[0]!.respond(status, { error_code, task_id: 9, operation_id: 'original-op' })
      const error = await pending.catch((cause: unknown) => cause)
      expect(error).toMatchObject({ error_code, task_id: 9, operation_id: 'original-op' })
      expect(storageResultUnknown(error)).toBe(true)
      expect(FakeXHR.instances).toHaveLength(1)
      expect(FakeXHR.instances[0]!.headers['Idempotency-Key']).toBe('disk-original')
      expect(fetchMock).not.toHaveBeenCalled()
    },
  )
  it('replays the same batch once after an explicit 401 and keeps the original key', async () => {
    fetchMock.mockResolvedValueOnce(
      json({ access_token: 'rotated', refresh_token: 'rotated-refresh' }),
    )
    const files = [new File(['one'], 'one.txt'), new File(['two'], 'two.txt')]
    const pending = uploadProjectResourcesWithProgress(1, files, ['a/one.txt', 'b/two.txt'], {
      idempotencyKey: 'unchanged',
    })
    const first = FakeXHR.instances[0]!
    first.respond(401, {})
    await vi.waitFor(() => expect(FakeXHR.instances).toHaveLength(2))
    const second = FakeXHR.instances[1]!
    expect(first.headers['Idempotency-Key']).toBe(second.headers['Idempotency-Key'])
    expect(second.headers.Authorization).toBe('Bearer rotated')
    expect(second.body!.getAll('files')).toEqual(first.body!.getAll('files'))
    expect([...second.body!.keys()]).toEqual([...first.body!.keys()])
    second.respond(200, {
      items: [
        { path: 'a/one.txt', action: 'created' },
        { path: 'b/two.txt', action: 'conflict' },
      ],
    })
    await expect(pending).resolves.toMatchObject({
      items: [{ action: 'created' }, { action: 'conflict' }],
    })
  })
  it('aborts and suppresses progress or success from the old account', async () => {
    const onProgress = vi.fn()
    const pending = uploadProjectResourcesWithProgress(
      1,
      [new File(['data'], 'a.txt')],
      undefined,
      { idempotencyKey: 'once', onProgress },
    )
    const rejected = expect(pending).rejects.toBeInstanceOf(StaleSessionError)
    changeSessionContext('https://storage.example/api/v1', 2)
    const xhr = FakeXHR.instances[0]!
    expect(xhr.aborted).toBe(true)
    xhr.upload.dispatchEvent(new Event('progress'))
    xhr.respond(200, { items: [{ path: 'a.txt', action: 'created' }] })
    await rejected
    expect(onProgress).not.toHaveBeenCalled()
  })
  it('does not retry an uncertain network result or a conflict', async () => {
    const pending = uploadProjectResourcesWithProgress(
      1,
      [new File(['data'], 'a.txt')],
      undefined,
      { idempotencyKey: 'once' },
    )
    FakeXHR.instances[0]!.dispatchEvent(new Event('error'))
    await expect(pending).rejects.toThrow('storageErrors.resultUnknown')
    expect(FakeXHR.instances).toHaveLength(1)
    expect(fetchMock).not.toHaveBeenCalled()
  })
  it('reports sent bytes without treating 100 percent as a completed upload', async () => {
    const onProgress = vi.fn(),
      onServerProcessing = vi.fn()
    let completed = false
    const pending = uploadProjectResourcesWithProgress(
      1,
      [new File(['data'], 'a.txt')],
      undefined,
      {
        idempotencyKey: 'once',
        onProgress,
        onServerProcessing,
      },
    ).then((result) => {
      completed = true
      return result
    })
    const xhr = FakeXHR.instances[0]!
    xhr.upload.dispatchEvent(
      Object.assign(new Event('progress'), { lengthComputable: true, loaded: 4, total: 4 }),
    )
    expect(onProgress).toHaveBeenCalledWith(100)
    expect(onServerProcessing).toHaveBeenCalledTimes(1)
    expect(completed).toBe(false)
    xhr.respond(200, { items: [{ path: 'a.txt', action: 'created' }] })
    await pending
    expect(completed).toBe(true)
  })
})
