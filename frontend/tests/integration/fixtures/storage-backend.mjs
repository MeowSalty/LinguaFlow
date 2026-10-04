import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { mkdir, readFile, readdir, realpath, rename, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { buildConfigurationBackend, createConfigurationBackend } from './configuration-backend.mjs'
import { setApiBaseUrl, setLocalMode } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { clearAuthTokens, setAuthSession } from '@/api/token-storage'
import { createProject, uploadProjectResources } from '@/api/projects'
import {
  listStorageConnections,
  listStorageSpaces,
  getStorageTask,
  commitSourceUpdate,
  getStorageOptions,
} from '@/api/storage'

export const buildStorageBackend = buildConfigurationBackend
export async function createStorageBackend(metadata, signal) {
  const backend = await createConfigurationBackend(
    {
      ...metadata,
      storageDriver: 'Local in isolated test directory',
      contractStatus:
        'C01-C09 synchronized working-tree contract; Local evidence only, not S3/PostgreSQL deployment acceptance',
    },
    signal,
    { storage: true },
  )
  const start = async () => {
    await backend.start()
    setApiBaseUrl(backend.apiBase)
    setLocalMode(false)
    changeSessionContext(backend.apiBase, backend.authSession.user.id, true)
    setAuthSession(backend.authSession)
  }
  const close = async () => {
    clearAuthTokens()
    changeSessionContext('/api/v1', null, true)
    await backend.close()
  }
  const createStoredProject = async (name) => {
    const { items: connections } = await listStorageConnections({ kind: 'site' })
    const local = connections.find((connection) => connection.driver === 'local')
    assert.ok(local, 'administrator discovery must expose configured Local connection')
    const { items: spaces } = await listStorageSpaces(local.id)
    assert.ok(spaces.length > 0, 'Local connection must expose a space')
    const discovery = await getStorageOptions({ kind: 'user' })
    const target = discovery.items.find(
      (item) => item.selectable && item.space_id === spaces[0]?.id,
    )
    assert.ok(target, 'project creation uses ordinary-user options, not the management list')
    const space = spaces.find((item) => item.id === target.space_id)
    const project = await createProject({
      name,
      source_lang: 'en',
      target_lang: 'zh',
      glossary_enabled: false,
      storage_space_id: space.id,
    })
    const source = JSON.stringify({ first: 'First source line', second: 'Second source line' })
    const uploaded = await uploadProjectResources(
      project.id,
      [new File([source], 'source.json', { type: 'application/json' })],
      ['source.json'],
      undefined,
      { idempotencyKey: randomUUID() },
    )
    assert.equal(uploaded.items[0]?.action, 'created')
    assert.ok(uploaded.items[0]?.resource)
    return { project, resource: uploaded.items[0].resource, source, space, connection: local }
  }
  const terminalTask = (id) =>
    backend.until(
      () => getStorageTask(id.projectId, id.taskId),
      (task) => ['completed', 'failed', 'cancelled'].includes(task.status),
      'storage task terminal',
    )
  const isolatedPath = async (relative) => {
    const root = await realpath(backend.runDir)
    const artifacts = await realpath(path.resolve('tests/artifacts/storage-integration'))
    assert.ok(
      root.startsWith(`${artifacts}${path.sep}`),
      'fault injection must remain inside this test artifacts directory',
    )
    const target = path.resolve(root, relative)
    assert.ok(
      target.startsWith(`${root}${path.sep}`),
      'target must remain inside the current isolated fixture',
    )
    return target
  }
  const removeOriginalObject = async (source) => {
    const objects = await realpath(await isolatedPath('objects'))
    const matches = []
    const walk = async (directory) => {
      for (const entry of await readdir(directory, { withFileTypes: true })) {
        const target = path.join(directory, entry.name)
        if (entry.isDirectory()) await walk(target)
        else if (entry.isFile() && (await readFile(target)).equals(Buffer.from(source)))
          matches.push(target)
      }
    }
    await walk(objects)
    assert.equal(
      matches.length,
      1,
      'a fresh source fixture must contain exactly one matching original object',
    )
    const original = await realpath(matches[0])
    assert.ok(original.startsWith(`${objects}${path.sep}`))
    const backup = await isolatedPath(`fault-backups/${randomUUID()}.original`)
    await mkdir(path.dirname(backup), { recursive: true })
    await rename(original, backup)
    return { removedMatchingOriginal: true, originalBytes: Buffer.byteLength(source) }
  }
  const enablePromptCleanup = async () => {
    const configPath = await isolatedPath('server.yaml')
    const config = JSON.parse(await readFile(configPath, 'utf8'))
    config.server.storage.deletion_grace = '1ms'
    config.server.storage.signed_url_ttl = '1ms'
    config.server.storage.signed_url_max_ttl = '1ms'
    config.server.storage.reconcile_interval = '100ms'
    await writeFile(configPath, JSON.stringify(config, null, 2))
    await backend.restart()
  }
  const dropCommitResponse = async (projectId, resourceId, payload) => {
    let forwardedRequests = 0
    let upstreamStatus
    let proxyError
    const expectedPath = `/api/v1/projects/${projectId}/resources/${resourceId}/source-commit`
    const proxy = createServer(async (request, response) => {
      try {
        assert.equal(request.method, 'POST')
        assert.equal(request.url, expectedPath)
        const chunks = []
        for await (const chunk of request) chunks.push(chunk)
        forwardedRequests++
        const upstream = await fetch(`${backend.apiBase}${expectedPath.slice('/api/v1'.length)}`, {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${backend.authSession.access_token}`,
            'Content-Type': 'application/json',
          },
          body: Buffer.concat(chunks),
          signal: AbortSignal.any([AbortSignal.timeout(15000), ...(signal ? [signal] : [])]),
        })
        upstreamStatus = upstream.status
        await upstream.arrayBuffer()
        // Upstream has committed. Cut the real TCP response before the client receives headers.
        response.destroy()
      } catch (error) {
        proxyError = error
        response.destroy()
      }
    })
    proxy.listen(0, '127.0.0.1')
    await once(proxy, 'listening')
    try {
      const proxyBase = `http://127.0.0.1:${proxy.address().port}/api/v1`
      setApiBaseUrl(proxyBase)
      changeSessionContext(proxyBase, backend.authSession.user.id, true)
      setAuthSession(backend.authSession)
      await assert.rejects(
        commitSourceUpdate(projectId, resourceId, payload),
        (error) => error.status === undefined,
      )
      if (proxyError) throw proxyError
      assert.equal(upstreamStatus, 200)
      assert.equal(forwardedRequests, 1, 'lost response must not trigger another commit')
      return { upstreamStatus, forwardedRequests }
    } finally {
      setApiBaseUrl(backend.apiBase)
      changeSessionContext(backend.apiBase, backend.authSession.user.id, true)
      setAuthSession(backend.authSession)
      proxy.closeAllConnections()
      await new Promise((resolve) => proxy.close(resolve))
    }
  }
  return {
    ...backend,
    start,
    close,
    createStoredProject,
    terminalTask,
    removeOriginalObject,
    enablePromptCleanup,
    dropCommitResponse,
    get apiBase() {
      return backend.apiBase
    },
    get authSession() {
      return backend.authSession
    },
  }
}
