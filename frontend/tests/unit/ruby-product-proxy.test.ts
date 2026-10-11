import { once } from 'node:events'
import { createServer, request, type IncomingMessage, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'
import { afterEach, describe, expect, it } from 'vitest'
import { createRubyTransportProxy } from '../integration/fixtures/ruby-product-proxy.mjs'

const servers: Server[] = []

async function listen(server: Server) {
  servers.push(server)
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return `http://127.0.0.1:${(server.address() as AddressInfo).port}`
}

afterEach(async () => {
  for (const server of servers.splice(0).reverse()) {
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) => {
      server.close((error) => (error ? reject(error) : resolve()))
    })
  }
})

async function fixture() {
  const upstreamRequests: { target: string; path: string; host: string; body: string }[] = []
  const upstream = (target: string) =>
    createServer(async (req, res) => {
      let body = ''
      for await (const chunk of req) body += chunk.toString()
      upstreamRequests.push({ target, path: req.url!, host: req.headers.host!, body })
      if (req.url?.startsWith('/api/v1/events')) {
        res.writeHead(200, { 'content-type': 'text/event-stream' })
        res.write('data: ready\n\n')
        return
      }
      res.end(target)
    })
  const backendOrigin = await listen(upstream('backend'))
  const previewOrigin = await listen(upstream('preview'))
  const otherOrigin = await listen(upstream('other'))
  const transport = createRubyTransportProxy(backendOrigin, previewOrigin)
  const origin = await listen(transport.server)
  return { ...transport, origin, backendOrigin, previewOrigin, otherOrigin, upstreamRequests }
}

function send(origin: string, path: string, body?: string) {
  return new Promise<IncomingMessage>((resolve, reject) => {
    // Preserve the raw request target; fetch would normalize the exploit before sending it.
    const outgoing = request(
      origin,
      { path, method: body === undefined ? 'GET' : 'POST', headers: { host: 'untrusted.test' } },
      resolve,
    )
    outgoing.on('error', reject)
    outgoing.end(body)
  })
}

async function read(response: IncomingMessage) {
  let body = ''
  for await (const chunk of response) body += chunk.toString()
  return body
}

describe('ruby product transport proxy', () => {
  it.each(['/.//', '/%2e//', '/api/v1/../.././/', '/.\\/'])(
    'keeps the configured upstream for a request starting with %s',
    async (prefix) => {
      const fixtureData = await fixture()
      const target = `${prefix}${new URL(fixtureData.otherOrigin).host}/capture?value=1`
      const response = await send(fixtureData.origin, target)

      expect(await read(response)).toBe('preview')
      expect(fixtureData.upstreamRequests).toEqual([
        {
          target: 'preview',
          path: `//${new URL(fixtureData.otherOrigin).host}/capture?value=1`,
          host: new URL(fixtureData.previewOrigin).host,
          body: '',
        },
      ])
    },
  )

  it('forwards API bodies, queries and preview paths to their configured upstreams', async () => {
    const proxy = await fixture()
    const body = JSON.stringify({ name: 'proxy regression' })
    expect(await read(await send(proxy.origin, '/api/v1/jobs?project_id=1', body))).toBe('backend')
    expect(await read(await send(proxy.origin, '/assets/app.js?v=1'))).toBe('preview')
    expect(proxy.upstreamRequests).toEqual([
      {
        target: 'backend',
        path: '/api/v1/jobs?project_id=1',
        host: new URL(proxy.backendOrigin).host,
        body,
      },
      {
        target: 'preview',
        path: '/assets/app.js?v=1',
        host: new URL(proxy.previewOrigin).host,
        body: '',
      },
    ])
  })

  it('preserves the active page cap without recording query credentials', async () => {
    const proxy = await fixture()
    proxy.capActivePageSize(2)
    const response = await send(
      proxy.origin,
      '/api/v1/operations?state=active&limit=50&access_token=fixture-secret',
    )
    expect(await read(response)).toBe('backend')
    expect(proxy.upstreamRequests[0]?.path).toBe(
      '/api/v1/operations?state=active&limit=2&access_token=fixture-secret',
    )
    expect(proxy.requests).toHaveLength(1)
    expect(proxy.requests[0]).toMatchObject({ path: '/api/v1/operations', limit: '2', status: 200 })
    expect(JSON.stringify(proxy.requests)).not.toContain('fixture-secret')
  })

  it('streams SSE before the upstream closes and supports forced disconnects', async () => {
    const proxy = await fixture()
    const response = await send(proxy.origin, '/api/v1/events')
    expect(response.headers['content-type']).toBe('text/event-stream')
    const [chunk] = await once(response, 'data')
    expect(chunk.toString()).toBe('data: ready\n\n')
    expect(response.complete).toBe(false)
    expect(proxy.requests[0]?.bytes).toBe(Buffer.byteLength('data: ready\n\n'))
    const closed = new Promise<void>((resolve) => response.once('close', resolve))
    proxy.disconnectStreams()
    await closed
    expect(response.destroyed).toBe(true)
  })
})
