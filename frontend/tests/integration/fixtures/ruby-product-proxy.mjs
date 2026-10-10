import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { access } from 'node:fs/promises'
import { createServer, request as httpRequest } from 'node:http'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const frontend = fileURLToPath(new URL('../../../', import.meta.url))

async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return server.address().port
}

// A transport proxy, never a business-response mock. Node streams preserve EventSource
// delivery and disconnects; Playwright route.fetch would buffer an endless SSE response.
export async function createRubyProductProxy(backend) {
  await access(path.join(frontend, 'dist/index.html')).catch(() => {
    throw new Error('Production build missing. Run task frontend:test:ruby-product.')
  })
  const reservation = createServer()
  const previewPort = await listen(reservation)
  await new Promise((resolve) => reservation.close(resolve))
  let previewError
  let previewOutput = ''
  const preview = spawn(
    process.execPath,
    [
      path.join(frontend, 'node_modules/vite/bin/vite.js'),
      'preview',
      '--host',
      '127.0.0.1',
      '--port',
      String(previewPort),
      '--strictPort',
    ],
    { cwd: frontend, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] },
  )
  preview.on('error', (error) => {
    previewError = error
  })
  preview.stdout.on('data', (chunk) => {
    previewOutput += chunk.toString()
  })
  preview.stderr.on('data', (chunk) => {
    previewOutput += chunk.toString()
  })
  const requests = []
  const streams = new Set()
  let activePageLimit
  const proxy = createServer((req, res) => {
    const url = new URL(req.url, 'http://fixture.test')
    const api = url.pathname.startsWith('/api/v1/')
    if (
      api &&
      url.pathname === '/api/v1/operations' &&
      url.searchParams.get('state') === 'active' &&
      activePageLimit
    )
      url.searchParams.set('limit', String(activePageLimit))
    const destination = new URL(
      `${url.pathname}${url.search}`,
      api ? new URL(backend.apiBase).origin : `http://127.0.0.1:${previewPort}`,
    )
    // Never persist authorization, access_token query values, or response payloads.
    const observation = api
      ? {
          method: req.method,
          path: url.pathname,
          state: url.searchParams.get('state'),
          statusFilter: url.searchParams.get('status'),
          cursor: url.searchParams.get('cursor'),
          limit: url.searchParams.get('limit'),
          openedAt: Date.now(),
          bytes: 0,
        }
      : undefined
    if (observation) requests.push(observation)
    const outgoing = httpRequest(
      destination,
      {
        method: req.method,
        headers: { ...req.headers, host: destination.host },
      },
      (incoming) => {
        if (observation) observation.status = incoming.statusCode
        res.writeHead(incoming.statusCode, incoming.headers)
        res.flushHeaders()
        const stream = String(incoming.headers['content-type']).includes('text/event-stream')
        if (stream) streams.add(res)
        incoming.on('data', (chunk) => {
          if (observation) observation.bytes += chunk.length
        })
        incoming.on('error', () => res.destroy())
        incoming.pipe(res)
        res.on('close', () => {
          streams.delete(res)
          incoming.destroy()
        })
      },
    )
    outgoing.on('error', () => {
      if (!res.headersSent) res.writeHead(502)
      res.end()
    })
    req.on('aborted', () => outgoing.destroy())
    res.on('close', () => outgoing.destroy())
    req.pipe(outgoing)
  })
  async function close() {
    proxy.closeAllConnections()
    if (proxy.listening) await new Promise((resolve) => proxy.close(resolve))
    if (preview.pid && preview.exitCode == null && preview.signalCode == null) {
      const stopped = once(preview, 'exit')
      preview.kill()
      await stopped
    }
  }
  try {
    await backend.until(
      async () => {
        if (previewError) throw previewError
        if (preview.exitCode != null) throw new Error(`Preview exited: ${previewOutput}`)
        try {
          return (await fetch(`http://127.0.0.1:${previewPort}`)).ok
        } catch {
          return false
        }
      },
      Boolean,
      'production preview ready',
    )
    const port = await listen(proxy)
    return {
      origin: `http://127.0.0.1:${port}`,
      requests,
      close,
      capActivePageSize(limit) {
        activePageLimit = limit
      },
      disconnectStreams() {
        for (const stream of streams) stream.destroy()
      },
    }
  } catch (error) {
    await close()
    throw error
  }
}
